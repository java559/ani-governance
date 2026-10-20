package data

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	pb "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/monitoring/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type ObservabilityClient struct{ client pb.MonitoringServiceClient }

func NewObservabilityClientFromEnv() (*ObservabilityClient, func(), error) {
	addr := os.Getenv("ANI_OBSERVABILITY_GRPC_ADDR")
	ca, cert, key := os.Getenv("ANI_OBSERVABILITY_CA_FILE"), os.Getenv("ANI_OBSERVABILITY_CLIENT_CERT_FILE"), os.Getenv("ANI_OBSERVABILITY_CLIENT_KEY_FILE")
	if addr == "" && ca == "" && cert == "" && key == "" {
		return nil, func() {}, nil
	}
	if addr == "" {
		return nil, nil, fmt.Errorf("observability gRPC address is required")
	}
	tlsConfig, err := observabilityClientTLS(ca, cert, key)
	if err != nil {
		return nil, nil, fmt.Errorf("observability mTLS configuration invalid: %w", err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return nil, nil, err
	}
	return &ObservabilityClient{pb.NewMonitoringServiceClient(conn)}, func() { _ = conn.Close() }, nil
}

func observabilityCall[T any](c *ObservabilityClient, ctx context.Context, tenant, actor string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.client == nil {
		return zero, status.Error(codes.Unavailable, "observability unavailable")
	}
	if !validResourceIdentity(tenant, actor) {
		return zero, status.Error(codes.Unauthenticated, "invalid observability caller")
	}
	call, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return fn(call)
}
func (c *ObservabilityClient) QueryRange(ctx context.Context, tenant, actor string, r *pb.QueryRangeRequest) (*pb.QueryRangeResponse, error) {
	return observabilityCall(c, ctx, tenant, actor, func(ctx context.Context) (*pb.QueryRangeResponse, error) { return c.client.QueryRange(ctx, r) })
}
func (c *ObservabilityClient) QueryLatest(ctx context.Context, tenant, actor string, r *pb.QueryLatestRequest) (*pb.QueryLatestResponse, error) {
	return observabilityCall(c, ctx, tenant, actor, func(ctx context.Context) (*pb.QueryLatestResponse, error) { return c.client.QueryLatest(ctx, r) })
}
