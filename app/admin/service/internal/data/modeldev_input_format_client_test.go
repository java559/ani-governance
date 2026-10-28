package data

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// modelDevInputPeer records the selectors it received so the test proves the
// client forwards the format filter instead of dropping it.
type modelDevInputPeer struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
	page     *modeldevv1.ListInputVersionsResponse
	requests []*modeldevv1.ListInputVersionsRequest
}

func (p *modelDevInputPeer) ListInputVersions(_ context.Context, in *modeldevv1.ListInputVersionsRequest) (*modeldevv1.ListInputVersionsResponse, error) {
	p.requests = append(p.requests, in)
	return p.page, nil
}

// A dropped format filter would silently widen the page, which the caller
// cannot detect from total alone.
func TestModelDevCatalogueClientForwardsInputFormat(t *testing.T) {
	text := func(value string) *string { return &value }
	for _, test := range []struct {
		name       string
		format     *string
		wantFormat *string
	}{
		{name: "absent format is not invented", format: nil},
		{name: "CSV is forwarded", format: text("CSV"), wantFormat: text("CSV")},
		{name: "JSON is forwarded", format: text("JSON"), wantFormat: text("JSON")},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			config.Address = listener.Addr().String()
			peer := &modelDevInputPeer{page: &modeldevv1.ListInputVersionsResponse{
				InputVersions: []*modeldevv1.InputVersionView{{
					InputVersionId: uuid.NewString(), State: modeldevv1.InputState_INPUT_STATE_READY, Format: "CSV",
					SizeBytes: 128, Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
					RowCount: 10, FeatureCount: 16, CreatedAt: timestamppb.New(time.Now()),
				}},
				Total: 1,
			}}
			server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
			modeldevv1.RegisterModelDevQueryServiceServer(server, peer)
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(server.Stop)
			client, closeClient, err := NewModelDevClient(config)
			require.NoError(t, err)
			t.Cleanup(closeClient)
			scope := ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "governance:user:1"))

			out, err := client.ListInputVersions(ctx, scope, 2, 10, nil, test.format)
			require.Equal(t, codes.OK, status.Code(err))
			require.NotNil(t, out)
			require.Len(t, peer.requests, 1)
			require.Equal(t, uint32(2), peer.requests[0].GetPage().GetPage())
			require.Equal(t, uint32(10), peer.requests[0].GetPage().GetPageSize())
			require.Equal(t, test.wantFormat, peer.requests[0].Format)
		})
	}
}
