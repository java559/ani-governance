package data

import (
	"context"
	"fmt"
	"os"
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

type ImageClientConfig struct {
	Address, CAFile, CertFile, KeyFile string
	Timeout                            time.Duration
}
type ImageClient struct {
	client  imagev1.TenantImageServiceClient
	timeout time.Duration
}

func NewImageClient(c ImageClientConfig) (*ImageClient, func(), error) {
	invalid := func() (*ImageClient, func(), error) {
		return nil, nil, fmt.Errorf("invalid Image mTLS client configuration")
	}
	if c.Address == "" || c.Timeout <= 0 || c.Timeout > 45*time.Second {
		return invalid()
	}
	security, err := resourceClientTLS(c.CAFile, c.CertFile, c.KeyFile)
	if err != nil {
		dialDependencyFailed(peerResourceService, "load_certificate", err)
		return nil, nil, fmt.Errorf("Image mTLS configuration: %w", err)
	}
	conn, err := grpc.NewClient(c.Address, outboundDial(peerResourceService, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(security)))...)
	if err != nil {
		return nil, nil, fmt.Errorf("create Image client: %w", err)
	}
	return &ImageClient{client: imagev1.NewTenantImageServiceClient(conn), timeout: c.Timeout}, func() { _ = conn.Close() }, nil
}
func ImageConfigFromEnv() (ImageClientConfig, error) {
	timeout := 30 * time.Second
	if raw := os.Getenv("ANI_IMAGE_TIMEOUT"); raw != "" {
		var err error
		timeout, err = time.ParseDuration(raw)
		if err != nil || timeout <= 0 || timeout > 45*time.Second {
			return ImageClientConfig{}, fmt.Errorf("invalid Image client timeout")
		}
	}
	return ImageClientConfig{Address: os.Getenv("ANI_IMAGE_ADDR"), CAFile: os.Getenv("ANI_IMAGE_CA"), CertFile: os.Getenv("ANI_IMAGE_CERT"), KeyFile: os.Getenv("ANI_IMAGE_KEY"), Timeout: timeout}, nil
}
func trustedImageCall[T any](c *ImageClient, ctx context.Context, tenant, actor string, call func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.client == nil || c.timeout <= 0 {
		return zero, status.Error(codes.Unavailable, "Image client unavailable")
	}
	if !validResourceIdentity(tenant, actor) {
		return zero, status.Error(codes.Unauthenticated, "invalid trusted Image identity")
	}
	if err := ctx.Err(); err != nil {
		return zero, status.FromContextError(err).Err()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	// Replaces all pre-existing outbound metadata, including public identity,
	// bearer tokens and operator hints. Only verified principal facts cross hops.
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	// Preserve the original status and domain details, including deadlines.
	return call(ctx)
}
func imageRequestRequired() error {
	return status.Error(codes.InvalidArgument, "Image request required")
}
func (c *ImageClient) EnsureImageSpace(ctx context.Context, tenant, actor string, r *imagev1.EnsureImageSpaceRequest) (*imagev1.EnsureImageSpaceResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.EnsureImageSpaceRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.EnsureImageSpaceResponse, error) {
		return c.client.EnsureImageSpace(ctx, req)
	})
}
func (c *ImageClient) GetImageSpace(ctx context.Context, tenant, actor string, r *imagev1.GetImageSpaceRequest) (*imagev1.GetImageSpaceResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.GetImageSpaceRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.GetImageSpaceResponse, error) {
		return c.client.GetImageSpace(ctx, req)
	})
}
func (c *ImageClient) GetPublisherCredential(ctx context.Context, tenant, actor string, r *imagev1.GetPublisherCredentialRequest) (*imagev1.GetPublisherCredentialResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.GetPublisherCredentialRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.GetPublisherCredentialResponse, error) {
		return c.client.GetPublisherCredential(ctx, req)
	})
}
func (c *ImageClient) IssuePublisherCredential(ctx context.Context, tenant, actor string, r *imagev1.IssuePublisherCredentialRequest) (*imagev1.IssuePublisherCredentialResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.IssuePublisherCredentialRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.IssuePublisherCredentialResponse, error) {
		return c.client.IssuePublisherCredential(ctx, req)
	})
}
func (c *ImageClient) ResetPublisherCredential(ctx context.Context, tenant, actor string, r *imagev1.ResetPublisherCredentialRequest) (*imagev1.ResetPublisherCredentialResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.ResetPublisherCredentialRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.ResetPublisherCredentialResponse, error) {
		return c.client.ResetPublisherCredential(ctx, req)
	})
}
func (c *ImageClient) DisablePublisherCredential(ctx context.Context, tenant, actor string, r *imagev1.DisablePublisherCredentialRequest) (*imagev1.DisablePublisherCredentialResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.DisablePublisherCredentialRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.DisablePublisherCredentialResponse, error) {
		return c.client.DisablePublisherCredential(ctx, req)
	})
}
func (c *ImageClient) RegisterImage(ctx context.Context, tenant, actor string, r *imagev1.RegisterImageRequest) (*imagev1.RegisterImageResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.RegisterImageRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.RegisterImageResponse, error) {
		return c.client.RegisterImage(ctx, req)
	})
}
func (c *ImageClient) GetImage(ctx context.Context, tenant, actor string, r *imagev1.GetImageRequest) (*imagev1.GetImageResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.GetImageRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.GetImageResponse, error) { return c.client.GetImage(ctx, req) })
}
func (c *ImageClient) ListImages(ctx context.Context, tenant, actor string, r *imagev1.ListImagesRequest) (*imagev1.ListImagesResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.ListImagesRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.ListImagesResponse, error) { return c.client.ListImages(ctx, req) })
}
func (c *ImageClient) UpdateImage(ctx context.Context, tenant, actor string, r *imagev1.UpdateImageRequest) (*imagev1.UpdateImageResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.UpdateImageRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.UpdateImageResponse, error) { return c.client.UpdateImage(ctx, req) })
}
func (c *ImageClient) UnregisterImage(ctx context.Context, tenant, actor string, r *imagev1.UnregisterImageRequest) (*imagev1.UnregisterImageResponse, error) {
	if r == nil {
		return nil, imageRequestRequired()
	}
	req := proto.Clone(r).(*imagev1.UnregisterImageRequest)
	req.TenantId = tenant
	return trustedImageCall(c, ctx, tenant, actor, func(ctx context.Context) (*imagev1.UnregisterImageResponse, error) {
		return c.client.UnregisterImage(ctx, req)
	})
}
