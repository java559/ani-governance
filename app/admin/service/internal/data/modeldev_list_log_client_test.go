package data

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Only the external RPC boundary is substituted; production mTLS, delegation
// and response validation execute normally. No ModelDev business claim here.
type modelDevListLogPeer struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
	scope       ModelDevResolveScope
	list        *modeldevv1.ListExecutionsResponse
	logs        *modeldevv1.GetExecutionLogsResponse
	executionID string
	remote      error
}

func (p *modelDevListLogPeer) check(ctx context.Context, method string) error {
	md, _ := metadata.FromIncomingContext(ctx)
	for key, want := range map[string]string{"x-ani-tenant-id": p.scope.ResourceTenantID, "x-ani-actor": p.scope.Actor, "x-ani-data-scope": "tenant-all", "x-ani-authorized-method": method} {
		got := md.Get(key)
		if len(got) != 1 || got[0] != want {
			return status.Error(codes.Internal, "delegation mismatch")
		}
	}
	if len(md.Get("x-secret-injected")) != 0 || len(md.Get("x-ani-request-id")) != 1 || !modelDevCanonicalUUID(md.Get("x-ani-request-id")[0]) {
		return status.Error(codes.Internal, "metadata leak")
	}
	return p.remote
}
func (p *modelDevListLogPeer) ListExecutions(ctx context.Context, in *modeldevv1.ListExecutionsRequest) (*modeldevv1.ListExecutionsResponse, error) {
	if err := p.check(ctx, modeldevv1.ModelDevQueryService_ListExecutions_FullMethodName); err != nil {
		return nil, err
	}
	if in.Page == nil || in.Page.Page == nil || *in.Page.Page != 1 || in.Page.PageSize == nil || *in.Page.PageSize != 1 || in.ComputeState != nil || in.CloseState != nil || in.DeliveryState != nil {
		return nil, status.Error(codes.Internal, "selector mismatch")
	}
	return p.list, nil
}
func (p *modelDevListLogPeer) GetExecutionLogs(ctx context.Context, in *modeldevv1.GetExecutionLogsRequest) (*modeldevv1.GetExecutionLogsResponse, error) {
	if err := p.check(ctx, modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName); err != nil {
		return nil, err
	}
	if in.ExecutionId != p.executionID || in.LogId != nil || in.TailLines != 1 || in.MaxBytes != 32 {
		return nil, status.Error(codes.Internal, "selector mismatch")
	}
	return p.logs, nil
}
func TestModelDevListLogClientDelegatesCurrentScopeAndBoundsResponses(t *testing.T) {
	id := uuid.NewString()
	execution := &modeldevv1.ExecutionView{Identity: &trainingv1.ExecutionIdentity{ExecutionId: id, OperationId: uuid.NewString(), ExecutionSpecHash: strings.Repeat("a", 64)}, Kind: trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING, States: &modeldevv1.ExecutionStates{ComputeState: modeldevv1.ComputeState_COMPUTE_STATE_TRAINING, DeliveryState: modeldevv1.DeliveryState_DELIVERY_STATE_PENDING, CloseState: modeldevv1.CloseState_CLOSE_STATE_OPEN, ResourceState: modeldevv1.ResourceState_RESOURCE_STATE_NOT_APPLICABLE}, AcceptedAt: timestamppb.Now(), DeadlineAt: timestamppb.Now(), ObservedAt: timestamppb.Now()}
	list := &modeldevv1.ListExecutionsResponse{Executions: []*modeldevv1.ExecutionView{execution}}
	logs := &modeldevv1.GetExecutionLogsResponse{Source: &trainingv1.LogRef{LogId: uuid.NewString(), ResourceUid: uuid.NewString(), ContainerName: "node"}, Lines: []*modeldevv1.LogLine{{Timestamp: timestamppb.Now(), Text: "real stdout boundary sample"}}, ObservedAt: timestamppb.Now()}
	for _, test := range []struct {
		name              string
		change            func(*modelDevListLogPeer)
		listCode, logCode codes.Code
	}{
		{"valid current scope", nil, codes.OK, codes.OK},
		{"oversized list", func(p *modelDevListLogPeer) { p.list.Executions = append(p.list.Executions, p.list.Executions[0]) }, codes.Unavailable, codes.OK},
		{"unknown state", func(p *modelDevListLogPeer) { p.list.Executions[0].States.ComputeState = 987 }, codes.Unavailable, codes.OK},
		{"invalid execution", func(p *modelDevListLogPeer) { p.list.Executions[0].Identity.ExecutionId = "invalid" }, codes.Unavailable, codes.OK},
		{"log bytes", func(p *modelDevListLogPeer) { p.logs.Lines[0].Text = strings.Repeat("x", 33) }, codes.OK, codes.Unavailable},
		{"log lines", func(p *modelDevListLogPeer) { p.logs.Lines = append(p.logs.Lines, p.logs.Lines[0]) }, codes.OK, codes.Unavailable},
		{"missing actual timestamp", func(p *modelDevListLogPeer) { p.logs.Lines[0].Timestamp = nil }, codes.OK, codes.Unavailable},
		{"wrong container", func(p *modelDevListLogPeer) { p.logs.Source.ContainerName = "other" }, codes.OK, codes.Unavailable},
		{"hidden log field", func(p *modelDevListLogPeer) { p.logs.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01}) }, codes.OK, codes.Unavailable},
		{"unavailable remains error", func(p *modelDevListLogPeer) { p.remote = status.Error(codes.Unavailable, "private pod credential") }, codes.Unavailable, codes.Unavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			config.Address = listener.Addr().String()
			peer := &modelDevListLogPeer{scope: ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"}, list: proto.Clone(list).(*modeldevv1.ListExecutionsResponse), logs: proto.Clone(logs).(*modeldevv1.GetExecutionLogsResponse), executionID: id}
			if test.change != nil {
				test.change(peer)
			}
			server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
			modeldevv1.RegisterModelDevQueryServiceServer(server, peer)
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(server.Stop)
			client, closeClient, err := NewModelDevClient(config)
			require.NoError(t, err)
			t.Cleanup(closeClient)
			ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "governance:user:1", "x-ani-authorized-method", "forged", "x-secret-injected", "private"))
			listed, err := client.ListExecutions(ctx, peer.scope, 1, 1)
			require.Equal(t, test.listCode, status.Code(err))
			if err != nil {
				require.Nil(t, listed)
				require.NotContains(t, err.Error(), "private")
			} else {
				require.True(t, proto.Equal(peer.list, listed))
			}
			// An invalid page window is rejected before any downstream call.
			if _, err := client.ListExecutions(ctx, peer.scope, 0, 20); status.Code(err) != codes.Unavailable {
				t.Fatal("zero page was forwarded")
			}
			if _, err := client.ListExecutions(ctx, peer.scope, 1, 101); status.Code(err) != codes.Unavailable {
				t.Fatal("out-of-range page size was forwarded")
			}
			got, err := client.GetExecutionLogs(ctx, peer.scope, id, 1, 32)
			require.Equal(t, test.logCode, status.Code(err))
			if err != nil {
				require.Nil(t, got)
				require.NotContains(t, err.Error(), "private")
			} else {
				require.True(t, proto.Equal(peer.logs, got))
			}
		})
	}
}
