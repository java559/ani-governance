package data

import (
	"context"
	"net"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// A forwarded filter must reach the owner unchanged. Dropping it would silently
// widen the page, which the caller cannot detect from total alone.
type modelDevExecutionFilterPeer struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
	scope   ModelDevResolveScope
	request *modeldevv1.ListExecutionsRequest
}

func (p *modelDevExecutionFilterPeer) ListExecutions(_ context.Context, in *modeldevv1.ListExecutionsRequest) (*modeldevv1.ListExecutionsResponse, error) {
	p.request = in
	return &modeldevv1.ListExecutionsResponse{}, nil
}

func TestModelDevClientForwardsExecutionStateFilters(t *testing.T) {
	compute := modeldevv1.ComputeState_COMPUTE_STATE_SUCCEEDED
	delivery := modeldevv1.DeliveryState_DELIVERY_STATE_PUBLISHED
	closeState := modeldevv1.CloseState_CLOSE_STATE_CLOSED
	name := "lora"

	for _, test := range []struct {
		name   string
		filter ExecutionFilter
		want   *modeldevv1.ListExecutionsRequest
	}{
		{
			name:   "no filter forwards no selector",
			filter: ExecutionFilter{},
		},
		{
			name:   "all three selectors are forwarded",
			filter: ExecutionFilter{ComputeState: &compute, DeliveryState: &delivery, CloseState: &closeState},
		},
		{
			name:   "a single selector does not imply the others",
			filter: ExecutionFilter{ComputeState: &compute},
		},
		{
			name:   "the name filter is forwarded",
			filter: ExecutionFilter{Name: &name},
		},
		{
			name:   "name combines with a state selector",
			filter: ExecutionFilter{Name: &name, ComputeState: &compute},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			config.Address = listener.Addr().String()
			peer := &modelDevExecutionFilterPeer{scope: ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"}}
			server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
			modeldevv1.RegisterModelDevQueryServiceServer(server, peer)
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(server.Stop)
			client, closeClient, err := NewModelDevClient(config)
			require.NoError(t, err)
			t.Cleanup(closeClient)
			ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "governance:user:1"))

			_, err = client.ListExecutions(ctx, peer.scope, 2, 10, test.filter)
			require.NoError(t, err)
			require.NotNil(t, peer.request)
			require.NotNil(t, peer.request.Page)
			require.Equal(t, uint32(2), peer.request.GetPage().GetPage())
			require.Equal(t, uint32(10), peer.request.GetPage().GetPageSize())
			require.Equal(t, test.filter.ComputeState, peer.request.ComputeState)
			require.Equal(t, test.filter.DeliveryState, peer.request.DeliveryState)
			require.Equal(t, test.filter.CloseState, peer.request.CloseState)
			require.Equal(t, test.filter.Name, peer.request.Name)
		})
	}
}
