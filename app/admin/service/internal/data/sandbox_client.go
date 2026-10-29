package data

import (
	"context"
	"time"

	"github.com/google/uuid"
	pb "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// SandboxClient is intentionally carried by InstanceClient so all Resource
// calls share the single mTLS connection created by NewResourceClientsFromEnv.
type SandboxClient struct{ client pb.SandboxServiceClient }

func sandboxCall[T any](c *InstanceClient, ctx context.Context, tenant, actor string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.sandbox == nil {
		return zero, status.Error(codes.Unavailable, "SANDBOX_ADAPTER_UNAVAILABLE")
	}
	if !validResourceIdentity(tenant, actor) {
		return zero, status.Error(codes.Unauthenticated, "INVALID_SANDBOX_CALLER")
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return fn(call)
}

func (c *InstanceClient) ResolveSandbox(ctx context.Context, tenant, actor string, r *pb.ResolveCreateSpecRequest) (*pb.ResolveCreateSpecResponse, error) {
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.ResolveCreateSpecResponse, error) {
		return c.sandbox.ResolveCreateSpec(call, r)
	})
}

func (c *InstanceClient) AcceptSandboxCreate(ctx context.Context, tenant, actor string, r *pb.AcceptCreateRequest) (*pb.AcceptCreateResponse, error) {
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.AcceptCreateResponse, error) {
		return c.sandbox.AcceptCreate(call, r)
	})
}

func (c *InstanceClient) GetSandbox(ctx context.Context, tenant, actor string, r *pb.GetSandboxRequest) (*pb.GetSandboxResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.GetSandboxResponse, error) { return c.sandbox.GetSandbox(call, r) })
}

func (c *InstanceClient) ListSandboxes(ctx context.Context, tenant, actor string, r *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.ListSandboxesResponse, error) { return c.sandbox.ListSandboxes(call, r) })
}

func (c *InstanceClient) GetSandboxOperation(ctx context.Context, tenant, actor string, r *pb.GetOperationRequest) (*pb.GetOperationResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.GetOperationResponse, error) { return c.sandbox.GetOperation(call, r) })
}

func (c *InstanceClient) PauseSandbox(ctx context.Context, tenant, actor string, r *pb.PauseRequest) (*pb.PauseResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.PauseResponse, error) { return c.sandbox.Pause(call, r) })
}

func (c *InstanceClient) ResumeSandbox(ctx context.Context, tenant, actor string, r *pb.ResumeRequest) (*pb.ResumeResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.ResumeResponse, error) { return c.sandbox.Resume(call, r) })
}

func (c *InstanceClient) DeleteSandbox(ctx context.Context, tenant, actor string, r *pb.DeleteRequest) (*pb.DeleteResponse, error) {
	if r == nil || r.TenantId != tenant {
		return nil, status.Error(codes.InvalidArgument, "INVALID_SANDBOX_REQUEST")
	}
	return sandboxCall(c, ctx, tenant, actor, func(call context.Context) (*pb.DeleteResponse, error) { return c.sandbox.Delete(call, r) })
}
