package data

import (
	"context"
	"time"

	"github.com/google/uuid"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// VMImageClient is the trusted Resource adapter for the tenant image catalog.
// It shares the managed Resource listener with the VM and instance clients, so a
// missing deployment disables the image lane exactly the way it disables VMs.
type VMImageClient struct{ client vm.VMImageServiceClient }

// vmImageCall repeats the VM lane's trusted-caller rules: an absent adapter is
// Unavailable, an unusable identity is Unauthenticated, every unary call carries
// its own deadline, and tenant plus actor travel only as signed metadata.
func vmImageCall[T any](c *VMImageClient, ctx context.Context, tenant, actor string, invoke func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.client == nil {
		return zero, status.Error(codes.Unavailable, "VM_ADAPTER_UNAVAILABLE")
	}
	if !validResourceIdentity(tenant, actor) {
		return zero, status.Error(codes.Unauthenticated, "VM_SCOPE_DENIED")
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return invoke(call)
}

func (c *VMImageClient) List(ctx context.Context, tenant, actor string, page, size uint32, kind, state, keyword string) (*vm.ListImagesResponse, error) {
	return vmImageCall(c, ctx, tenant, actor, func(call context.Context) (*vm.ListImagesResponse, error) {
		return c.client.ListImages(call, &vm.ListImagesRequest{TenantId: tenant, Page: page, PageSize: size, Kind: kind, State: state, Keyword: keyword})
	})
}

func (c *VMImageClient) Get(ctx context.Context, tenant, actor, id string) (*vm.VMImage, error) {
	return vmImageCall(c, ctx, tenant, actor, func(call context.Context) (*vm.VMImage, error) {
		return c.client.GetImage(call, &vm.GetImageRequest{TenantId: tenant, ImageId: id})
	})
}

// Create binds the caller tenant into the request before sending it, so a body
// that names another tenant can never reach the owner service.
func (c *VMImageClient) Create(ctx context.Context, tenant, actor string, in *vm.CreateImageRequest) (*vm.VMImage, error) {
	if in == nil || in.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "VM_INVALID_REQUEST")
	}
	return vmImageCall(c, ctx, tenant, actor, func(call context.Context) (*vm.VMImage, error) {
		return c.client.CreateImage(call, in)
	})
}

func (c *VMImageClient) Session(ctx context.Context, tenant, actor string, in *vm.CreateUploadSessionRequest) (*vm.UploadSession, error) {
	if in == nil || in.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "VM_INVALID_REQUEST")
	}
	return vmImageCall(c, ctx, tenant, actor, func(call context.Context) (*vm.UploadSession, error) {
		return c.client.CreateUploadSession(call, in)
	})
}

func (c *VMImageClient) Update(ctx context.Context, tenant, actor string, in *vm.UpdateImageRequest) (*vm.VMImage, error) {
	if in == nil || in.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "VM_INVALID_REQUEST")
	}
	return vmImageCall(c, ctx, tenant, actor, func(call context.Context) (*vm.VMImage, error) {
		return c.client.UpdateImage(call, in)
	})
}

func (c *VMImageClient) Delete(ctx context.Context, tenant, actor string, in *vm.DeleteImageRequest) (*vm.VMImage, error) {
	if in == nil || in.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "VM_INVALID_REQUEST")
	}
	return vmImageCall(c, ctx, tenant, actor, func(call context.Context) (*vm.VMImage, error) {
		return c.client.DeleteImage(call, in)
	})
}
