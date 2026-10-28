package data

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

// modelDevImplementationPeer forwards one canned catalogue page and records the
// requested kind, so the test proves the client passes the filter through and
// rejects a page that ignores it.
type modelDevImplementationPeer struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
	page  *modeldevv1.ListImplementationsResponse
	kinds []*trainingv1.ExecutionKind
}

func (p *modelDevImplementationPeer) ListImplementations(_ context.Context, in *modeldevv1.ListImplementationsRequest) (*modeldevv1.ListImplementationsResponse, error) {
	p.kinds = append(p.kinds, in.Kind)
	return p.page, nil
}

func modelDevImplementationView(id string, kind trainingv1.ExecutionKind) *modeldevv1.ImplementationView {
	return &modeldevv1.ImplementationView{
		ImplementationId: id, Name: "implementation " + id, Kind: kind, Version: "1",
		ParameterContractVersion: "ani.mlp.parameters.v2.1",
		ParameterRules: []*modeldevv1.ParameterRule{{
			Name: "epochs", Type: modeldevv1.ParameterType_PARAMETER_TYPE_INTEGER,
			DefaultValue: &trainingv1.Parameter{Name: "epochs", Value: &trainingv1.Parameter_IntegerValue{IntegerValue: 3}},
		}},
	}
}

func TestModelDevCatalogueClientForwardsAndEnforcesKind(t *testing.T) {
	general := trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING
	finetuning := trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING
	for _, test := range []struct {
		name    string
		kind    *trainingv1.ExecutionKind
		items   []*modeldevv1.ImplementationView
		want    codes.Code
		wantRPC *trainingv1.ExecutionKind
	}{
		{name: "absent kind keeps the whole page", items: []*modeldevv1.ImplementationView{
			modelDevImplementationView("builtin.mlp", general), modelDevImplementationView("builtin.sft_lora", finetuning),
		}, want: codes.OK},
		{name: "filtered page passes the kind through", kind: &finetuning,
			items: []*modeldevv1.ImplementationView{modelDevImplementationView("builtin.sft_lora", finetuning)},
			want:  codes.OK, wantRPC: &finetuning},
		// An owner build that ignores the new field must not silently widen a
		// narrowed request.
		{name: "mixed page under a filter is unavailable", kind: &finetuning,
			items: []*modeldevv1.ImplementationView{
				modelDevImplementationView("builtin.mlp", general), modelDevImplementationView("builtin.sft_lora", finetuning),
			}, want: codes.Unavailable, wantRPC: &finetuning},
		{name: "no match is an empty page", kind: &finetuning, want: codes.OK, wantRPC: &finetuning},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			config.Address = listener.Addr().String()
			peer := &modelDevImplementationPeer{page: &modeldevv1.ListImplementationsResponse{Implementations: test.items, Total: uint64(len(test.items))}}
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
			out, err := client.ListImplementations(ctx, scope, 1, 20, test.kind)
			require.Equal(t, test.want, status.Code(err))
			require.Len(t, peer.kinds, 1)
			require.Equal(t, test.wantRPC, peer.kinds[0])
			if test.want != codes.OK {
				require.Nil(t, out)
			}
		})
	}
}
