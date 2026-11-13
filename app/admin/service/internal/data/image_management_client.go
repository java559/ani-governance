package data

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ImageManagementClient is the platform-scope Resource adapter. It shares the
// managed Resource listener and mTLS material with ImageClient, but never sends
// a tenant header: the platform scope is carried by x-ani-scope=platform and the
// operator identity alone.
type ImageManagementClient struct {
	platform imagev1.PlatformImageServiceClient
	managed  imagev1.AdminTenantImageServiceClient
	timeout  time.Duration
}

// NewImageManagementClient reuses the existing Image mTLS configuration so no
// second certificate system is introduced.
func NewImageManagementClient(c ImageClientConfig) (*ImageManagementClient, func(), error) {
	invalid := func() (*ImageManagementClient, func(), error) {
		return nil, nil, status.Error(codes.InvalidArgument, "invalid Image management client configuration")
	}
	if c.Address == "" || c.Timeout <= 0 || c.Timeout > 45*time.Second {
		return invalid()
	}
	security, err := resourceClientTLS(c.CAFile, c.CertFile, c.KeyFile)
	if err != nil {
		return nil, nil, status.Error(codes.InvalidArgument, "invalid Image management mTLS configuration")
	}
	conn, err := grpc.NewClient(c.Address, outboundDial(peerResourceService, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(security)))...)
	if err != nil {
		return nil, nil, status.Error(codes.Unavailable, "Image management client unavailable")
	}
	return &ImageManagementClient{platform: imagev1.NewPlatformImageServiceClient(conn), managed: imagev1.NewAdminTenantImageServiceClient(conn), timeout: c.Timeout}, func() { _ = conn.Close() }, nil
}

// platformImageIdentity validates a platform operator actor and returns the
// pair that Resource accepts: scope=platform plus the governance actor.
func platformImageIdentity(actor string) (string, string, bool) {
	for _, prefix := range []string{"governance:user:", "governance:access-key:"} {
		if strings.HasPrefix(actor, prefix) {
			id, err := strconv.ParseUint(strings.TrimPrefix(actor, prefix), 10, 32)
			if err != nil || id == 0 || actor != prefix+strconv.FormatUint(id, 10) {
				return "", "", false
			}
			return "platform", actor, true
		}
	}
	return "", "", false
}

// managedImageIdentity additionally requires the already resolved canonical
// tenant UUID that Resource will bind. The manager never sends its own tenant.
func managedImageIdentity(tenant, actor string) (string, string, bool) {
	if !validResourceIdentity(tenant, actor) {
		return "", "", false
	}
	return tenant, actor, true
}

func imageManagementCall[T any](c *ImageManagementClient, ctx context.Context, tenant, actor string, call func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.platform == nil || c.managed == nil || c.timeout <= 0 {
		return zero, status.Error(codes.Unavailable, "Image management unavailable")
	}
	_, identity, ok := platformImageIdentity(actor)
	if tenant != "" {
		// 代管调用仍以平台作用域出站：目标租户只写进请求体的
		// target_tenant_id，并且这里先校验它确实是规范 UUID。Res 的
		// 治理解析器把 x-ani-scope=platform 与 x-ani-tenant-id 视为互斥，
		// 代管方法本身落在平台作用域白名单里，由服务门面校验目标租户。
		_, identity, ok = managedImageIdentity(tenant, actor)
	}
	if !ok {
		return zero, status.Error(codes.Unauthenticated, "invalid trusted Image management identity")
	}
	if err := ctx.Err(); err != nil {
		return zero, status.FromContextError(err).Err()
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	pairs := []string{"x-ani-scope", "platform", "x-ani-actor", identity, "x-ani-request-id", uuid.NewString()}
	// NewOutgoingContext replaces every inherited value, so no Authorization,
	// cookie or public user identity can cross this hop.
	callCtx = metadata.NewOutgoingContext(callCtx, metadata.Pairs(pairs...))
	return call(callCtx)
}

func (c *ImageManagementClient) GetPlatformSpace(ctx context.Context, actor string) (*imagev1.GetPlatformSpaceResponse, error) {
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.GetPlatformSpaceResponse, error) {
		return c.platform.GetPlatformSpace(call, &imagev1.GetPlatformSpaceRequest{})
	})
}

func (c *ImageManagementClient) InitializePlatform(ctx context.Context, actor string, r *imagev1.InitializePlatformRequest) (*imagev1.InitializePlatformResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.InitializePlatformRequest)
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.InitializePlatformResponse, error) {
		return c.platform.InitializePlatform(call, req)
	})
}

func (c *ImageManagementClient) GetPlatformPublisherCredential(ctx context.Context, actor string) (*imagev1.GetPlatformPublisherCredentialResponse, error) {
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.GetPlatformPublisherCredentialResponse, error) {
		return c.platform.GetPlatformPublisherCredential(call, &imagev1.GetPlatformPublisherCredentialRequest{})
	})
}

func (c *ImageManagementClient) IssuePlatformPublisher(ctx context.Context, actor string, r *imagev1.IssuePlatformPublisherCredentialRequest) (*imagev1.IssuePlatformPublisherCredentialResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.IssuePlatformPublisherCredentialRequest)
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.IssuePlatformPublisherCredentialResponse, error) {
		return c.platform.IssuePlatformPublisherCredential(call, req)
	})
}

func (c *ImageManagementClient) ResetPlatformPublisher(ctx context.Context, actor string, r *imagev1.ResetPlatformPublisherCredentialRequest) (*imagev1.ResetPlatformPublisherCredentialResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.ResetPlatformPublisherCredentialRequest)
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.ResetPlatformPublisherCredentialResponse, error) {
		return c.platform.ResetPlatformPublisherCredential(call, req)
	})
}

func (c *ImageManagementClient) DisablePlatformPublisher(ctx context.Context, actor string, r *imagev1.DisablePlatformPublisherCredentialRequest) (*imagev1.DisablePlatformPublisherCredentialResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.DisablePlatformPublisherCredentialRequest)
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.DisablePlatformPublisherCredentialResponse, error) {
		return c.platform.DisablePlatformPublisherCredential(call, req)
	})
}

func (c *ImageManagementClient) ListPlatformImages(ctx context.Context, actor string, r *imagev1.ListPlatformImagesRequest) (*imagev1.ListPlatformImagesResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.ListPlatformImagesRequest)
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.ListPlatformImagesResponse, error) {
		return c.platform.ListPlatformImages(call, req)
	})
}

func (c *ImageManagementClient) GetPlatformImage(ctx context.Context, actor string, r *imagev1.GetPlatformImageRequest) (*imagev1.GetPlatformImageResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.GetPlatformImageRequest)
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.GetPlatformImageResponse, error) {
		return c.platform.GetPlatformImage(call, req)
	})
}

func (c *ImageManagementClient) RegisterPlatformImage(ctx context.Context, actor string, r *imagev1.RegisterPlatformImageRequest) (*imagev1.RegisterPlatformImageResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.RegisterPlatformImageRequest)
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.RegisterPlatformImageResponse, error) {
		return c.platform.RegisterPlatformImage(call, req)
	})
}

func (c *ImageManagementClient) UpdatePlatformImage(ctx context.Context, actor string, r *imagev1.UpdatePlatformImageRequest) (*imagev1.UpdatePlatformImageResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.UpdatePlatformImageRequest)
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.UpdatePlatformImageResponse, error) {
		return c.platform.UpdatePlatformImage(call, req)
	})
}

func (c *ImageManagementClient) UnregisterPlatformImage(ctx context.Context, actor string, r *imagev1.UnregisterPlatformImageRequest) (*imagev1.UnregisterPlatformImageResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.UnregisterPlatformImageRequest)
	return imageManagementCall(c, ctx, "", actor, func(call context.Context) (*imagev1.UnregisterPlatformImageResponse, error) {
		return c.platform.UnregisterPlatformImage(call, req)
	})
}

func (c *ImageManagementClient) GetTenantImageSpace(ctx context.Context, tenant, actor string, r *imagev1.GetTenantImageSpaceRequest) (*imagev1.GetTenantImageSpaceResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.GetTenantImageSpaceRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.GetTenantImageSpaceResponse, error) {
		return c.managed.GetTenantImageSpace(call, req)
	})
}

func (c *ImageManagementClient) EnsureTenantImageSpace(ctx context.Context, tenant, actor string, r *imagev1.EnsureTenantImageSpaceRequest) (*imagev1.EnsureTenantImageSpaceResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.EnsureTenantImageSpaceRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.EnsureTenantImageSpaceResponse, error) {
		return c.managed.EnsureTenantImageSpace(call, req)
	})
}

func (c *ImageManagementClient) GetTenantPublisherCredential(ctx context.Context, tenant, actor string, r *imagev1.GetTenantPublisherCredentialRequest) (*imagev1.GetTenantPublisherCredentialResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.GetTenantPublisherCredentialRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.GetTenantPublisherCredentialResponse, error) {
		return c.managed.GetTenantPublisherCredential(call, req)
	})
}

func (c *ImageManagementClient) IssueTenantPublisher(ctx context.Context, tenant, actor string, r *imagev1.IssueTenantPublisherCredentialRequest) (*imagev1.IssueTenantPublisherCredentialResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.IssueTenantPublisherCredentialRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.IssueTenantPublisherCredentialResponse, error) {
		return c.managed.IssueTenantPublisherCredential(call, req)
	})
}

func (c *ImageManagementClient) ResetTenantPublisher(ctx context.Context, tenant, actor string, r *imagev1.ResetTenantPublisherCredentialRequest) (*imagev1.ResetTenantPublisherCredentialResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.ResetTenantPublisherCredentialRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.ResetTenantPublisherCredentialResponse, error) {
		return c.managed.ResetTenantPublisherCredential(call, req)
	})
}

func (c *ImageManagementClient) DisableTenantPublisher(ctx context.Context, tenant, actor string, r *imagev1.DisableTenantPublisherCredentialRequest) (*imagev1.DisableTenantPublisherCredentialResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.DisableTenantPublisherCredentialRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.DisableTenantPublisherCredentialResponse, error) {
		return c.managed.DisableTenantPublisherCredential(call, req)
	})
}

func (c *ImageManagementClient) ListTenantImages(ctx context.Context, tenant, actor string, r *imagev1.ListTenantImagesRequest) (*imagev1.ListTenantImagesResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.ListTenantImagesRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.ListTenantImagesResponse, error) {
		return c.managed.ListTenantImages(call, req)
	})
}

func (c *ImageManagementClient) GetTenantImage(ctx context.Context, tenant, actor string, r *imagev1.GetTenantImageRequest) (*imagev1.GetTenantImageResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.GetTenantImageRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.GetTenantImageResponse, error) {
		return c.managed.GetTenantImage(call, req)
	})
}

func (c *ImageManagementClient) RegisterTenantImage(ctx context.Context, tenant, actor string, r *imagev1.RegisterTenantImageRequest) (*imagev1.RegisterTenantImageResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.RegisterTenantImageRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.RegisterTenantImageResponse, error) {
		return c.managed.RegisterTenantImage(call, req)
	})
}

func (c *ImageManagementClient) UpdateTenantImage(ctx context.Context, tenant, actor string, r *imagev1.UpdateTenantImageRequest) (*imagev1.UpdateTenantImageResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.UpdateTenantImageRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.UpdateTenantImageResponse, error) {
		return c.managed.UpdateTenantImage(call, req)
	})
}

func (c *ImageManagementClient) UnregisterTenantImage(ctx context.Context, tenant, actor string, r *imagev1.UnregisterTenantImageRequest) (*imagev1.UnregisterTenantImageResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "Image management request required")
	}
	req := proto.Clone(r).(*imagev1.UnregisterTenantImageRequest)
	req.TargetTenantId = tenant
	return imageManagementCall(c, ctx, tenant, actor, func(call context.Context) (*imagev1.UnregisterTenantImageResponse, error) {
		return c.managed.UnregisterTenantImage(call, req)
	})
}
