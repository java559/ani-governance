package data

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	terminal "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/terminal/v1"
	pb "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type InstanceClient struct{ client pb.InstanceServiceClient }

func NewInstanceClientFromEnv() (*InstanceClient, func(), error) {
	addr := os.Getenv("ANI_INSTANCE_ADDR")
	if addr == "" {
		return nil, func() {}, nil
	}
	tls, e := resourceClientTLS(os.Getenv("ANI_INSTANCE_CA"), os.Getenv("ANI_INSTANCE_CERT"), os.Getenv("ANI_INSTANCE_KEY"))
	if e != nil {
		return nil, nil, fmt.Errorf("instance mTLS configuration invalid: %w", e)
	}
	conn, e := grpc.NewClient(addr, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(tls)))
	if e != nil {
		return nil, nil, e
	}
	return &InstanceClient{pb.NewInstanceServiceClient(conn)}, func() { _ = conn.Close() }, nil
}
func instanceCall[T any](c *InstanceClient, ctx context.Context, tenant, actor string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.client == nil {
		return zero, status.Error(codes.Unavailable, "INSTANCE_ADAPTER_UNAVAILABLE")
	}
	if !validResourceIdentity(tenant, actor) {
		return zero, status.Error(codes.Unauthenticated, "INVALID_INSTANCE_CALLER")
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return fn(call)
}
func (c *InstanceClient) Resolve(ctx context.Context, t, a string, r *pb.CreateInstanceData, defaults *pb.SchedulingOptions) (*pb.CreateSpec, error) {
	return instanceCall(c, ctx, t, a, func(ctx context.Context) (*pb.CreateSpec, error) {
		return c.client.ResolveCreateSpec(ctx, &pb.ResolveCreateSpecRequest{TenantId: t, Data: r, SchedulingDefaults: defaults})
	})
}

type instanceCommandActorKey struct{}

// WithInstanceCommandActor carries the actor from the trusted persisted command.
// The client reconstructs metadata instead of forwarding public headers.
func WithInstanceCommandActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, instanceCommandActorKey{}, actor)
}
func (c *InstanceClient) Create(ctx context.Context, r *pb.AcceptCreateRequest) (*pb.OperationResponse, error) {
	if r == nil || r.Context == nil {
		return nil, status.Error(codes.InvalidArgument, "INVALID_INSTANCE_COMMAND")
	}
	actor, _ := ctx.Value(instanceCommandActorKey{}).(string)
	return instanceCall(c, ctx, r.Context.TenantId, actor, func(ctx context.Context) (*pb.OperationResponse, error) { return c.client.AcceptCreate(ctx, r) })
}
func (c *InstanceClient) Delete(ctx context.Context, r *pb.AcceptDeleteRequest) (*pb.OperationResponse, error) {
	if r == nil || r.Context == nil {
		return nil, status.Error(codes.InvalidArgument, "INVALID_INSTANCE_COMMAND")
	}
	actor, _ := ctx.Value(instanceCommandActorKey{}).(string)
	return instanceCall(c, ctx, r.Context.TenantId, actor, func(ctx context.Context) (*pb.OperationResponse, error) { return c.client.AcceptDelete(ctx, r) })
}
func (c *InstanceClient) Change(ctx context.Context, t, a string, r *pb.ChangeInstanceRequest, start bool) (*pb.OperationResponse, error) {
	return instanceCall(c, ctx, t, a, func(ctx context.Context) (*pb.OperationResponse, error) {
		if start {
			return c.client.Start(ctx, r)
		}
		return c.client.Stop(ctx, r)
	})
}
func (c *InstanceClient) Get(ctx context.Context, t, a, id string) (*pb.Instance, error) {
	return instanceCall(c, ctx, t, a, func(ctx context.Context) (*pb.Instance, error) {
		return c.client.GetInstance(ctx, &pb.GetInstanceRequest{TenantId: t, InstanceId: id})
	})
}
func (c *InstanceClient) List(ctx context.Context, t, a string, r *pb.ListInstancesRequest) (*pb.ListInstancesResponse, error) {
	if r == nil || r.TenantId != t {
		return nil, status.Error(codes.InvalidArgument, "INVALID_INSTANCE_REQUEST")
	}
	return instanceCall(c, ctx, t, a, func(ctx context.Context) (*pb.ListInstancesResponse, error) {
		return c.client.ListInstances(ctx, r)
	})
}
func (c *InstanceClient) Operation(ctx context.Context, t, a, id string) (*pb.Operation, error) {
	return instanceCall(c, ctx, t, a, func(ctx context.Context) (*pb.Operation, error) {
		return c.client.GetOperation(ctx, &pb.GetOperationRequest{TenantId: t, OperationId: id})
	})
}
func (c *InstanceClient) Presets(ctx context.Context, t, a string, r *pb.ListPresetsRequest) (*pb.ListPresetsResponse, error) {
	if r == nil || r.TenantId != t {
		return nil, status.Error(codes.InvalidArgument, "INVALID_INSTANCE_REQUEST")
	}
	return instanceCall(c, ctx, t, a, func(ctx context.Context) (*pb.ListPresetsResponse, error) {
		return c.client.ListPresets(ctx, r)
	})
}
func (c *InstanceClient) Target(ctx context.Context, t, a, id string, version int64) (*terminal.TerminalTarget, error) {
	return instanceCall(c, ctx, t, a, func(ctx context.Context) (*terminal.TerminalTarget, error) {
		return c.client.ResolveTerminalTarget(ctx, &pb.ResolveTerminalTargetRequest{TenantId: t, InstanceId: id, ExpectedVersion: version})
	})
}
