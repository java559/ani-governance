package data

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	gpu "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/gpu/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// GPU has a separate bounded deadline and explicit platform context. Legacy
// monitoring calls retain their tenant validation and six-second deadline.
func gpuObservabilityCall[T any](c *ObservabilityClient, ctx context.Context, platform bool, tenant, actor string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.gpu == nil {
		return zero, status.Error(codes.Unavailable, "GPU monitoring unavailable")
	}
	if !strings.HasPrefix(actor, "governance:user:") || len(actor) <= len("governance:user:") || (platform && tenant != "") || (!platform && !validResourceIdentity(tenant, actor)) {
		return zero, status.Error(codes.Unauthenticated, "invalid GPU caller")
	}
	mode := "tenant"
	if platform {
		mode = "platform"
	}
	md := metadata.Pairs("x-ani-context", mode, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString())
	if !platform {
		md.Set("x-ani-tenant-id", tenant)
	}
	call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, md), 32*time.Second)
	defer cancel()
	return fn(call)
}
func (c *ObservabilityClient) QueryGpuLatest(ctx context.Context, platform bool, tenant, actor string, r *gpu.QueryGpuLatestRequest) (*gpu.QueryGpuResponse, error) {
	return gpuObservabilityCall(c, ctx, platform, tenant, actor, func(call context.Context) (*gpu.QueryGpuResponse, error) {
		out, err := c.gpu.QueryGpuLatest(call, r, grpc.MaxCallRecvMsgSize(8<<20))
		if err != nil {
			return nil, err
		}
		if out == nil {
			return nil, status.Error(codes.Unavailable, "invalid GPU response")
		}
		return out.Result, nil
	})
}
func (c *ObservabilityClient) QueryGpuRange(ctx context.Context, platform bool, tenant, actor string, r *gpu.QueryGpuRangeRequest) (*gpu.QueryGpuResponse, error) {
	return gpuObservabilityCall(c, ctx, platform, tenant, actor, func(call context.Context) (*gpu.QueryGpuResponse, error) {
		out, err := c.gpu.QueryGpuRange(call, r, grpc.MaxCallRecvMsgSize(8<<20))
		if err != nil {
			return nil, err
		}
		if out == nil {
			return nil, status.Error(codes.Unavailable, "invalid GPU response")
		}
		return out.Result, nil
	})
}
