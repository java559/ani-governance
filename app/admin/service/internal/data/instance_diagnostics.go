package data

import (
	"context"

	"github.com/google/uuid"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func (c *InstanceClient) Runtime(ctx context.Context, tenant, actor, id string) (*res.InstanceRuntime, error) {
	return instanceCall(c, ctx, tenant, actor, func(call context.Context) (*res.InstanceRuntime, error) {
		return c.client.GetInstanceRuntime(call, &res.GetInstanceRequest{TenantId: tenant, InstanceId: id})
	})
}

func (c *InstanceClient) Events(ctx context.Context, tenant, actor, id string) (*res.InstanceEvents, error) {
	return instanceCall(c, ctx, tenant, actor, func(call context.Context) (*res.InstanceEvents, error) {
		return c.client.ListInstanceEvents(call, &res.GetInstanceRequest{TenantId: tenant, InstanceId: id})
	})
}

// Logs deliberately inherits the caller's stream lifetime and cancellation.
// instanceCall's unary 30 second total deadline would cut idle follow streams.
func (c *InstanceClient) Logs(ctx context.Context, tenant, actor, id string, tail uint32, follow bool) (grpc.ServerStreamingClient[res.InstanceLogFrame], error) {
	if c == nil || c.client == nil {
		return nil, status.Error(codes.Unavailable, "INSTANCE_ADAPTER_UNAVAILABLE")
	}
	if !validResourceIdentity(tenant, actor) {
		return nil, status.Error(codes.Unauthenticated, "INVALID_INSTANCE_CALLER")
	}
	call := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return c.client.StreamInstanceLogs(call, &res.StreamInstanceLogsRequest{TenantId: tenant, InstanceId: id, TailLines: tail, Follow: follow}, grpc.MaxCallRecvMsgSize(20*1024))
}
