package data

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// This peer replaces only the external ModelDev boundary. It does not emulate
// registration, its persistence, or a provider receipt.
type modelDevRegistrationPeer struct {
	modeldevv1.UnimplementedModelDevOperationsServiceServer
	scope   ModelDevResolveScope
	id      string
	reply   *modeldevv1.GetExecutionResponse
	remote  error
	calls   atomic.Int32
	wait    bool
	entered chan struct{}
}

func (p *modelDevRegistrationPeer) ResumeModelRegistration(ctx context.Context, in *modeldevv1.ResumeModelRegistrationRequest) (*modeldevv1.GetExecutionResponse, error) {
	p.calls.Add(1)
	md, _ := metadata.FromIncomingContext(ctx)
	for key, want := range map[string]string{
		"x-ani-tenant-id":         p.scope.ResourceTenantID,
		"x-ani-actor":             p.scope.Actor,
		"x-ani-data-scope":        "tenant-all",
		"x-ani-authorized-method": modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName,
	} {
		if values := md.Get(key); len(values) != 1 || values[0] != want {
			return nil, status.Error(codes.Internal, "wrong registration delegation")
		}
	}
	if in.ExecutionId != p.id || len(in.ProtoReflect().GetUnknown()) != 0 || len(md.Get("authorization")) != 0 || len(md.Get("x-secret-injected")) != 0 || len(md.Get("x-ani-request-id")) != 1 || !modelDevCanonicalUUID(md.Get("x-ani-request-id")[0]) {
		return nil, status.Error(codes.Internal, "registration scope changed or inbound metadata leaked")
	}
	if p.entered != nil {
		close(p.entered)
	}
	if p.wait {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if p.remote != nil {
		return nil, p.remote
	}
	return p.reply, nil
}

func registrationClientExecution(t *testing.T) *modeldevv1.ExecutionView {
	t.Helper()
	f := modelDevResponseFixture(t)
	return &modeldevv1.ExecutionView{
		Identity: &trainingv1.ExecutionIdentity{ExecutionId: uuid.NewString(), OperationId: uuid.NewString(), ExecutionSpecHash: strings.Repeat("a", 64)},
		Kind:     trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING, ImplementationId: "builtin.sft_lora",
		ComputePresetId: f.selection.PresetId, ComputeOrigin: f.response.Snapshot.ComputeOrigin,
		Timing: f.response.Snapshot.Timing, AcceptedAt: f.response.Snapshot.Timing.AcceptedAt, DeadlineAt: f.response.Snapshot.Timing.ExecutionDeadlineAt,
		ObservedAt:        timestamppb.Now(),
		States:            &modeldevv1.ExecutionStates{ComputeState: modeldevv1.ComputeState_COMPUTE_STATE_SUCCEEDED, DeliveryState: modeldevv1.DeliveryState_DELIVERY_STATE_PUBLISHED, CloseState: modeldevv1.CloseState_CLOSE_STATE_CLOSED, ResourceState: modeldevv1.ResourceState_RESOURCE_STATE_NOT_APPLICABLE},
		ModelRegistration: &modeldevv1.ModelRegistrationView{State: "READY", RegistrationId: uuid.NewString(), ModelId: uuid.NewString(), ModelVersionId: uuid.NewString()},
	}
}

func registrationBoundaryClient(t *testing.T, peer *modelDevRegistrationPeer) *ModelDevClient {
	t.Helper()
	config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	modeldevv1.RegisterModelDevOperationsServiceServer(server, peer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	config.Address = listener.Addr().String()
	client, closeClient, err := NewModelDevClient(config)
	require.NoError(t, err)
	t.Cleanup(closeClient)
	return client
}

func TestModelDevResumeRegistrationRebuildsOnlyCurrentDelegation(t *testing.T) {
	execution := registrationClientExecution(t)
	peer := &modelDevRegistrationPeer{scope: ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"}, id: execution.Identity.ExecutionId, reply: &modeldevv1.GetExecutionResponse{Execution: execution}}
	client := registrationBoundaryClient(t, peer)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "governance:user:1", "x-ani-authorized-method", "forged", "authorization", "Bearer not-a-real-token", "x-secret-injected", "not-a-real-secret"))
	out, err := client.ResumeModelRegistration(ctx, peer.scope, peer.id)
	require.NoError(t, err)
	require.True(t, proto.Equal(peer.reply, out))
	require.Equal(t, int32(1), peer.calls.Load())
}

func TestModelDevResumeRegistrationRejectsUnboundOrInventedObservations(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*modeldevv1.GetExecutionResponse)
		valid  bool
	}{
		{"ready receipt", func(*modeldevv1.GetExecutionResponse) {}, true},
		{"pending has no receipt", func(r *modeldevv1.GetExecutionResponse) {
			r.Execution.ModelRegistration = &modeldevv1.ModelRegistrationView{State: "PENDING"}
		}, true},
		{"dependency is explicit", func(r *modeldevv1.GetExecutionResponse) {
			r.Execution.ModelRegistration = &modeldevv1.ModelRegistrationView{State: "DEPENDENCY_UNAVAILABLE", FailureCode: "MODEL_DEPENDENCY_UNAVAILABLE"}
		}, true},
		{"unknown state", func(r *modeldevv1.GetExecutionResponse) { r.Execution.ModelRegistration.State = "SUCCESS" }, false},
		{"ready missing registration", func(r *modeldevv1.GetExecutionResponse) { r.Execution.ModelRegistration.RegistrationId = "" }, false},
		{"ready missing model", func(r *modeldevv1.GetExecutionResponse) { r.Execution.ModelRegistration.ModelId = "" }, false},
		{"ready noncanonical version", func(r *modeldevv1.GetExecutionResponse) {
			r.Execution.ModelRegistration.ModelVersionId = "00000000-0000-0000-0000-000000000000"
		}, false},
		{"ready retains failure", func(r *modeldevv1.GetExecutionResponse) {
			r.Execution.ModelRegistration.FailureCode = "MODEL_DEPENDENCY_UNAVAILABLE"
		}, false},
		{"pending with invented IDs", func(r *modeldevv1.GetExecutionResponse) { r.Execution.ModelRegistration.State = "PENDING" }, false},
		{"dependency wrong code", func(r *modeldevv1.GetExecutionResponse) {
			r.Execution.ModelRegistration = &modeldevv1.ModelRegistrationView{State: "DEPENDENCY_UNAVAILABLE", FailureCode: "arbitrary-private-error"}
		}, false},
		{"general training cannot register", func(r *modeldevv1.GetExecutionResponse) {
			r.Execution.Kind = trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING
			r.Execution.ImplementationId = "builtin.mlp"
		}, false},
		{"missing registration after resume", func(r *modeldevv1.GetExecutionResponse) { r.Execution.ModelRegistration = nil }, false},
		{"not closed", func(r *modeldevv1.GetExecutionResponse) {
			r.Execution.States.CloseState = modeldevv1.CloseState_CLOSE_STATE_CLOSING
		}, false},
		{"wrong original execution", func(r *modeldevv1.GetExecutionResponse) { r.Execution.Identity.ExecutionId = uuid.NewString() }, false},
		{"unknown nested wire", func(r *modeldevv1.GetExecutionResponse) {
			r.Execution.ModelRegistration.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			execution := registrationClientExecution(t)
			id := execution.Identity.ExecutionId
			reply := &modeldevv1.GetExecutionResponse{Execution: execution}
			test.change(reply)
			peer := &modelDevRegistrationPeer{scope: ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"}, id: id, reply: reply}
			client := registrationBoundaryClient(t, peer)
			out, err := client.ResumeModelRegistration(context.Background(), peer.scope, id)
			if test.valid {
				require.NoError(t, err)
				require.True(t, proto.Equal(reply, out))
			} else {
				require.Nil(t, out)
				require.Equal(t, codes.Unavailable, status.Code(err))
			}
			require.Equal(t, int32(1), peer.calls.Load())
		})
	}
	in := registrationClientExecution(t)
	in.ModelRegistration.State = "invented"
	require.False(t, modelDevValidExecution(in), "ordinary GET/list must reject an invented registration observation too")
	in = registrationClientExecution(t)
	in.Kind = trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING
	in.ImplementationId = "builtin.mlp"
	in.ModelRegistration = nil
	require.True(t, modelDevValidExecution(in), "general training still has no registration requirement")
}

func TestModelDevResumeRegistrationPreservesParentBudgetAndNeverRetries(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.Internal, codes.DeadlineExceeded, codes.NotFound, codes.FailedPrecondition, codes.PermissionDenied, codes.InvalidArgument} {
		t.Run(code.String(), func(t *testing.T) {
			execution := registrationClientExecution(t)
			peer := &modelDevRegistrationPeer{scope: ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"}, id: execution.Identity.ExecutionId, remote: status.Error(code, "private backend token=hidden")}
			client := registrationBoundaryClient(t, peer)
			out, err := client.ResumeModelRegistration(context.Background(), peer.scope, peer.id)
			require.Nil(t, out)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private")
			require.NotContains(t, err.Error(), "token=")
			require.Equal(t, int32(1), peer.calls.Load())
		})
	}
	for _, cancelParent := range []bool{false, true} {
		execution := registrationClientExecution(t)
		peer := &modelDevRegistrationPeer{scope: ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"}, id: execution.Identity.ExecutionId, wait: true, entered: make(chan struct{})}
		client := registrationBoundaryClient(t, peer)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		start := time.Now()
		done := make(chan error, 1)
		go func() { _, err := client.ResumeModelRegistration(ctx, peer.scope, peer.id); done <- err }()
		select {
		case <-peer.entered:
		case <-time.After(time.Second):
			t.Fatal("peer not reached")
		}
		want := codes.DeadlineExceeded
		if cancelParent {
			cancel()
			want = codes.Canceled
		}
		err := <-done
		cancel()
		require.Equal(t, want, status.Code(err))
		require.Less(t, time.Since(start), 500*time.Millisecond)
		require.Equal(t, int32(1), peer.calls.Load())
	}
}
