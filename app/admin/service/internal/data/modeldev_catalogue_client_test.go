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
	"google.golang.org/protobuf/types/known/timestamppb"
)

// modelDevImplementationPeer forwards one canned catalogue page and records the
// requested kind, so the test proves the client passes the filter through and
// rejects a page that ignores it.
type modelDevImplementationPeer struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
	page        *modeldevv1.ListImplementationsResponse
	kinds       []*trainingv1.ExecutionKind
	requireMode bool
}

func (p *modelDevImplementationPeer) ListImplementations(_ context.Context, in *modeldevv1.ListImplementationsRequest) (*modeldevv1.ListImplementationsResponse, error) {
	p.kinds = append(p.kinds, in.Kind)
	if p.requireMode && (in.DeviceMode == nil || (*in.DeviceMode != "cpu" && *in.DeviceMode != "gpu")) {
		return nil, status.Error(codes.InvalidArgument, "explicit device_mode required by current ModelDev contract")
	}
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
			out, err := client.ListImplementations(ctx, scope, 1, 20, test.kind, "cpu")
			require.Equal(t, test.want, status.Code(err))
			require.Len(t, peer.kinds, 1)
			require.Equal(t, test.wantRPC, peer.kinds[0])
			if test.want != codes.OK {
				require.Nil(t, out)
			}
		})
	}
}

// This is the current typed remote contract over real mTLS, rather than a
// nil-tolerant former catalogue response.
func TestModelDevCatalogueClientSendsRequiredDeviceMode(t *testing.T) {
	config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	config.Address = listener.Addr().String()
	peer := &modelDevImplementationPeer{requireMode: true, page: &modeldevv1.ListImplementationsResponse{}}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	modeldevv1.RegisterModelDevQueryServiceServer(server, peer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, closeClient, err := NewModelDevClient(config)
	require.NoError(t, err)
	t.Cleanup(closeClient)
	scope := ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"}
	_, err = client.ListImplementations(context.Background(), scope, 1, 20, nil, "cpu")
	require.NoError(t, err, "CURRENT_CATALOGUE_MODE: the BFF must pass an explicit CPU/GPU material selector")
}

type modelDevVerifiedInputPeer struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
	input *modeldevv1.InputVersionView
}

func (p *modelDevVerifiedInputPeer) GetInputVersion(context.Context, *modeldevv1.GetInputVersionRequest) (*modeldevv1.GetInputVersionResponse, error) {
	return &modeldevv1.GetInputVersionResponse{InputVersion: p.input}, nil
}
func (p *modelDevVerifiedInputPeer) ListInputVersions(context.Context, *modeldevv1.ListInputVersionsRequest) (*modeldevv1.ListInputVersionsResponse, error) {
	return &modeldevv1.ListInputVersionsResponse{InputVersions: []*modeldevv1.InputVersionView{p.input}, Total: 1}, nil
}

// Only the remote owner's already verified facts are a peer. Both real client
// read methods must preserve a large current 16-feature CSV without a Gov L.
func TestModelDevInputCatalogueReadsOwnerVerifiedLargeCSV(t *testing.T) {
	config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	config.Address = listener.Addr().String()
	input := &modeldevv1.InputVersionView{InputVersionId: uuid.NewString(), State: modeldevv1.InputState_INPUT_STATE_READY, Format: "CSV", SizeBytes: 34604157, Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", RowCount: 33, FeatureCount: 16, CreatedAt: timestamppb.Now()}
	peer := &modelDevVerifiedInputPeer{input: input}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	modeldevv1.RegisterModelDevQueryServiceServer(server, peer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, closeClient, err := NewModelDevClient(config)
	require.NoError(t, err)
	t.Cleanup(closeClient)
	scope := ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"}
	t.Run("GET", func(t *testing.T) {
		out, err := client.GetInputVersion(context.Background(), scope, input.InputVersionId)
		require.NoError(t, err, "CURRENT_OWNER_INPUT: a 34,604,157B CSV within owner L must remain readable")
		require.Equal(t, int64(34604157), out.InputVersion.SizeBytes)
		require.Equal(t, uint32(16), out.InputVersion.FeatureCount)
	})
	t.Run("LIST", func(t *testing.T) {
		state := modeldevv1.InputState_INPUT_STATE_READY
		out, err := client.ListInputVersions(context.Background(), scope, 1, 20, &state, nil)
		require.NoError(t, err, "CURRENT_OWNER_INPUT: list must use the same owner-verified size semantics as GET")
		require.Len(t, out.InputVersions, 1)
		require.Equal(t, int64(34604157), out.InputVersions[0].SizeBytes)
	})
	t.Run("CSV still requires sixteen features", func(t *testing.T) {
		input.FeatureCount = 17
		defer func() { input.FeatureCount = 16 }()
		_, err := client.GetInputVersion(context.Background(), scope, input.InputVersionId)
		require.Equal(t, codes.Unavailable, status.Code(err))
		_, err = client.ListInputVersions(context.Background(), scope, 1, 20, nil, nil)
		require.Equal(t, codes.Unavailable, status.Code(err))
	})
	t.Run("nonpositive source size remains invalid", func(t *testing.T) {
		input.SizeBytes = 0
		defer func() { input.SizeBytes = 34604157 }()
		_, err := client.GetInputVersion(context.Background(), scope, input.InputVersionId)
		require.Equal(t, codes.Unavailable, status.Code(err))
	})

}
