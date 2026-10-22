package data

import (
	"context"
	"testing"
	"time"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type modelOperationsClientProbe struct {
	modelv1.ModelServiceClient
	t *testing.T
}

func (p *modelOperationsClientProbe) ListModelOperations(ctx context.Context, req *modelv1.ListModelOperationsRequest, _ ...grpc.CallOption) (*modelv1.ListModelOperationsResponse, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	require.Equal(p.t, []string{"11111111-1111-4111-8111-111111111111"}, md.Get("x-ani-tenant-id"))
	require.Equal(p.t, []string{"governance:user:7"}, md.Get("x-ani-actor"))
	require.Empty(p.t, md.Get("authorization"))
	require.Equal(p.t, md.Get("x-ani-tenant-id")[0], req.TenantId)
	require.Equal(p.t, int32(3), req.GetPage())
	require.Equal(p.t, int32(2), req.GetPageSize())
	_, ok := ctx.Deadline()
	require.True(p.t, ok)
	return &modelv1.ListModelOperationsResponse{Total: 12}, nil
}
func TestModelOperationsClientRebuildsIdentity(t *testing.T) {
	c := &ModelClient{client: &modelOperationsClientProbe{t: t}, timeout: time.Second}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "secret", "x-ani-tenant-id", "forged", "x-ani-actor", "forged"))
	req := &modelv1.ListModelOperationsRequest{TenantId: "forged", ModelId: "model-1", Page: proto.Int32(3), PageSize: proto.Int32(2)}
	reply, err := c.ListModelOperations(ctx, "11111111-1111-4111-8111-111111111111", "governance:user:7", req)
	require.NoError(t, err)
	require.Equal(t, int64(12), reply.Total)
	require.Equal(t, "forged", req.TenantId)
}
