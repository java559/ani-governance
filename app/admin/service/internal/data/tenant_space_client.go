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

// TenantSpaceResult is a trusted Resource response, checked again by the worker.
type TenantSpaceResult struct{ TenantID, OperationID, Phase string }

func tenantSpaceCall[T any](c *InstanceClient, ctx context.Context, tenant, operation string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.sandbox == nil {
		return zero, status.Error(codes.Unavailable, "TENANT_SPACE_ADAPTER_UNAVAILABLE")
	}
	if !tenantSpaceUUID(tenant) || !tenantSpaceUUID(operation) {
		return zero, status.Error(codes.InvalidArgument, "INVALID_TENANT_SPACE_REQUEST")
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", "governance:tenant-space", "x-ani-request-id", uuid.NewString()))
	return fn(call)
}

func (c *InstanceClient) OpenTenantSpace(ctx context.Context, tenant, operation string) (TenantSpaceResult, error) {
	result, err := tenantSpaceCall(c, ctx, tenant, operation, func(call context.Context) (*pb.OpenTenantSpaceResponse, error) {
		return c.sandbox.OpenTenantSpace(call, &pb.OpenTenantSpaceRequest{TenantId: tenant, OperationId: operation})
	})
	if err != nil {
		return TenantSpaceResult{}, err
	}
	space := result.GetSpace()
	if space == nil {
		return TenantSpaceResult{}, status.Error(codes.DataLoss, "TENANT_SPACE_RESPONSE_MISSING")
	}
	return TenantSpaceResult{TenantID: space.GetTenantId(), OperationID: space.GetOperationId(), Phase: space.GetPhase()}, nil
}
func (c *InstanceClient) CloseTenantSpace(ctx context.Context, tenant, operation string) (TenantSpaceResult, error) {
	result, err := tenantSpaceCall(c, ctx, tenant, operation, func(call context.Context) (*pb.CloseTenantSpaceResponse, error) {
		return c.sandbox.CloseTenantSpace(call, &pb.CloseTenantSpaceRequest{TenantId: tenant, OperationId: operation})
	})
	if err != nil {
		return TenantSpaceResult{}, err
	}
	space := result.GetSpace()
	if space == nil {
		return TenantSpaceResult{}, status.Error(codes.DataLoss, "TENANT_SPACE_RESPONSE_MISSING")
	}
	return TenantSpaceResult{TenantID: space.GetTenantId(), OperationID: space.GetOperationId(), Phase: space.GetPhase()}, nil
}
