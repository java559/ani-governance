package data

import (
	"bytes"
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// These are deliberately controlled ACK/error peers over real TLS. They prove
// the sender boundary; the separate worker/provider lane proves owner PG facts.
func TestModelDevDispatchPreservesOriginalAndRebuildsMetadata(t *testing.T) {
	original, response := modelDevDispatchFixture(t)
	wantIntent, wantSnapshot, err := original.CanonicalPayloads()
	if err != nil {
		t.Fatal("invalid fixed dispatch fixture")
	}
	type observation struct {
		request *modeldevv1.AcceptExecutionRequest
		md      metadata.MD
	}
	observed := make(chan observation, 2)
	config, calls := startModelDevDispatchPeer(t, original, response, func(ctx context.Context, request *modeldevv1.AcceptExecutionRequest) (*modeldevv1.AcceptExecutionResponse, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		select {
		case observed <- observation{proto.Clone(request).(*modeldevv1.AcceptExecutionRequest), md.Copy()}:
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return proto.Clone(response).(*modeldevv1.AcceptExecutionResponse), nil
	})
	client := newModelDevBoundaryClient(t, config)
	poison := metadata.Pairs("x-ani-tenant-id", uuid.NewString(), "x-ani-tenant-id", uuid.NewString(),
		"x-ani-actor", "governance:user:8", "x-ani-request-id", uuid.NewString(),
		"authorization", "private-delivery-sentinel", "cookie", "private-delivery-sentinel",
		"ani-workload-token", "private-delivery-sentinel", "ani-delegation", "private-delivery-sentinel",
		"proxy-authorization", "private-delivery-sentinel", "x-forwarded-client-cert", "private-delivery-sentinel")
	before := poison.Copy()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(metadata.NewIncomingContext(ctx, poison), poison)
	lastID := ""
	for i := 0; i < 2; i++ {
		got, err := client.AcceptExecution(ctx, original)
		want := ModelDevOwnerReceipt{OperationID: original.OperationID, ExecutionID: original.ExecutionID, ExecutionSpecHash: original.SpecHash,
			ComputeState: "SUCCEEDED", DeliveryState: "PUBLISHED", ResourceState: "NOT_APPLICABLE", CloseState: "CLOSED", Revision: 37, Replayed: true}
		if err != nil || got != want {
			if err != nil && err.Error() == "modeldev delivery client not implemented" {
				t.Fatal("MODELDEV_DELIVERY_CLIENT_BEHAVIOR: sender not implemented after real TLS preflight")
			}
			t.Fatal("sender did not retain the complete legal owner ACK")
		}
		var seen observation
		select {
		case seen = <-observed:
		case <-ctx.Done():
			t.Fatal("sender returned without a bounded observed RPC")
		}
		intent, intentErr := contractpb.DecodeIntent(seen.request.Intent)
		snapshot, snapshotErr := contractpb.DecodeSnapshot(seen.request.Snapshot)
		if intentErr != nil || snapshotErr != nil || seen.request.Identity == nil || seen.request.AcceptedAt == nil {
			t.Fatal("wire command is incomplete")
		}
		wireOriginal := cpup01.AdmissionEnvelope{TenantID: seen.request.ResourceTenantId, Actor: seen.request.AdmittedActorId,
			OperationID: seen.request.Identity.OperationId, ExecutionID: seen.request.Identity.ExecutionId, SpecHash: seen.request.Identity.ExecutionSpecHash,
			Intent: intent, IntentHash: seen.request.IntentHash, Snapshot: snapshot, AcceptedAt: seen.request.AcceptedAt.AsTime()}
		wireIntent, wireSnapshot, wireErr := wireOriginal.CanonicalPayloads()
		if wireErr != nil || !bytes.Equal(wireIntent, wantIntent) || !bytes.Equal(wireSnapshot, wantSnapshot) ||
			wireOriginal.TenantID != original.TenantID || wireOriginal.Actor != original.Actor || wireOriginal.OperationID != original.OperationID ||
			wireOriginal.ExecutionID != original.ExecutionID || !wireOriginal.AcceptedAt.Equal(original.AcceptedAt) {
			t.Error("sender changed the immutable command, accepted time, deadline or binding")
		}
		if !reflect.DeepEqual(seen.md.Get("x-ani-tenant-id"), []string{original.TenantID}) || !reflect.DeepEqual(seen.md.Get("x-ani-actor"), []string{original.Actor}) {
			t.Error("trusted envelope did not replace ambient scope")
		}
		ids := seen.md.Get("x-ani-request-id")
		if len(ids) != 1 {
			t.Fatal("RPC lacks one request identity")
		}
		id, parseErr := uuid.Parse(ids[0])
		if parseErr != nil || id == uuid.Nil || id.String() != ids[0] || ids[0] == lastID || ids[0] == poison.Get("x-ani-request-id")[0] {
			t.Error("RPC request identity is not fresh and canonical")
		}
		lastID = ids[0]
		for _, key := range []string{"authorization", "cookie", "ani-workload-token", "ani-delegation", "proxy-authorization", "x-forwarded-client-cert"} {
			if len(seen.md.Get(key)) != 0 {
				t.Error("ambient credential reached the delivery peer")
			}
		}
	}
	afterIntent, afterSnapshot, afterErr := original.CanonicalPayloads()
	incoming, _ := metadata.FromIncomingContext(ctx)
	outgoing, _ := metadata.FromOutgoingContext(ctx)
	if afterErr != nil || !bytes.Equal(afterIntent, wantIntent) || !bytes.Equal(afterSnapshot, wantSnapshot) || !reflect.DeepEqual(before, poison) || !reflect.DeepEqual(before, incoming) || !reflect.DeepEqual(before, outgoing) || calls.Load() != 2 {
		t.Error("sender mutated caller material or retried an ACK")
	}
}

func TestModelDevDispatchRejectsInvalidAcknowledgment(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*modeldevv1.AcceptExecutionResponse)
	}{
		{"missing identity", func(r *modeldevv1.AcceptExecutionResponse) { r.Identity = nil }},
		{"missing states", func(r *modeldevv1.AcceptExecutionResponse) { r.States = nil }},
		{"outer unknown", func(r *modeldevv1.AcceptExecutionResponse) { r.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01}) }},
		{"identity unknown", func(r *modeldevv1.AcceptExecutionResponse) {
			r.Identity.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		}},
		{"states unknown", func(r *modeldevv1.AcceptExecutionResponse) {
			r.States.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		}},
		{"different operation", func(r *modeldevv1.AcceptExecutionResponse) { r.Identity.OperationId = uuid.NewString() }},
		{"different execution", func(r *modeldevv1.AcceptExecutionResponse) { r.Identity.ExecutionId = uuid.NewString() }},
		{"different hash", func(r *modeldevv1.AcceptExecutionResponse) { r.Identity.ExecutionSpecHash = strings.Repeat("0", 64) }},
		{"zero revision", func(r *modeldevv1.AcceptExecutionResponse) { r.Revision = 0 }},
		{"unknown compute", func(r *modeldevv1.AcceptExecutionResponse) { r.States.ComputeState = modeldevv1.ComputeState(999) }},
		{"unspecified delivery", func(r *modeldevv1.AcceptExecutionResponse) {
			r.States.DeliveryState = modeldevv1.DeliveryState_DELIVERY_STATE_UNSPECIFIED
		}},
		{"unknown resource", func(r *modeldevv1.AcceptExecutionResponse) { r.States.ResourceState = modeldevv1.ResourceState(999) }},
		{"unspecified close", func(r *modeldevv1.AcceptExecutionResponse) {
			r.States.CloseState = modeldevv1.CloseState_CLOSE_STATE_UNSPECIFIED
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			original, response := modelDevDispatchFixture(t)
			bad := proto.Clone(response).(*modeldevv1.AcceptExecutionResponse)
			test.change(bad)
			config, calls := startModelDevDispatchPeer(t, original, response, func(context.Context, *modeldevv1.AcceptExecutionRequest) (*modeldevv1.AcceptExecutionResponse, error) {
				return bad, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got, err := newModelDevBoundaryClient(t, config).AcceptExecution(ctx, original)
			assertModelDevDispatchFailure(t, got, err, "INVALID_ACK", false)
			if calls.Load() != 1 {
				t.Error("invalid ACK was not consumed exactly once over TLS")
			}
		})
	}
}

func TestModelDevDispatchClassifiesOnlyCorrelatedPermanentFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		code      codes.Code
		reason    modeldevv1.ErrorReason
		shape     string
		want      string
		permanent bool
	}{
		{"invalid command", codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "clean", "INVALID_COMMAND", true},
		{"command conflict", codes.AlreadyExists, modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT, "clean", "COMMAND_CONFLICT", true},
		{"bare invalid", codes.InvalidArgument, 0, "bare", "OWNER_UNAVAILABLE", false},
		{"mismatched pair", codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT, "clean", "OWNER_UNAVAILABLE", false},
		{"foreign correlation", codes.AlreadyExists, modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT, "correlation", "OWNER_UNAVAILABLE", false},
		{"unknown detail", codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "unknown", "OWNER_UNAVAILABLE", false},
		{"unknown status", codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "status unknown", "OWNER_UNAVAILABLE", false},
		{"unknown Any envelope", codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "Any unknown", "OWNER_UNAVAILABLE", false},
		{"multiple details", codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "multiple", "OWNER_UNAVAILABLE", false},
		{"untrusted violations", codes.InvalidArgument, modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, "violations", "OWNER_UNAVAILABLE", false},
		{"authentication unavailable", codes.Unauthenticated, modeldevv1.ErrorReason_ERROR_REASON_UNAUTHENTICATED, "clean", "OWNER_UNAVAILABLE", false},
		{"persistence unavailable", codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, "clean", "OWNER_UNAVAILABLE", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			original, response := modelDevDispatchFixture(t)
			config, calls := startModelDevDispatchPeer(t, original, response, func(ctx context.Context, _ *modeldevv1.AcceptExecutionRequest) (*modeldevv1.AcceptExecutionResponse, error) {
				failure := status.New(test.code, "private-delivery-sentinel")
				if test.shape == "bare" {
					return nil, failure.Err()
				}
				md, _ := metadata.FromIncomingContext(ctx)
				correlation := ""
				if values := md.Get("x-ani-request-id"); len(values) == 1 {
					correlation = values[0]
				}
				detail := &modeldevv1.ErrorDetail{Reason: test.reason, SafeMessage: "private-delivery-sentinel", CorrelationId: correlation}
				switch test.shape {
				case "correlation":
					detail.CorrelationId = uuid.NewString()
				case "unknown":
					detail.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
				case "violations":
					detail.Violations = []*modeldevv1.FieldViolation{{Field: "private-delivery-sentinel", Reason: "private-delivery-sentinel"}}
				}
				var err error
				if test.shape == "multiple" {
					failure, err = failure.WithDetails(detail, detail)
				} else {
					failure, err = failure.WithDetails(detail)
				}
				if err != nil {
					return nil, status.Error(codes.Internal, "peer detail encoding failed")
				}
				wire := failure.Proto()
				if test.shape == "status unknown" {
					wire.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
				}
				if test.shape == "Any unknown" {
					wire.Details[0].ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
				}
				return nil, status.FromProto(wire).Err()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got, err := newModelDevBoundaryClient(t, config).AcceptExecution(ctx, original)
			assertModelDevDispatchFailure(t, got, err, test.want, test.permanent)
			if calls.Load() != 1 {
				t.Error("error classification did not consume exactly one TLS failure")
			}
		})
	}
}

func TestModelDevDispatchPreservesCallerCancellation(t *testing.T) {
	for _, mode := range []string{"already canceled", "already expired", "cancel in flight", "deadline in flight"} {
		t.Run(mode, func(t *testing.T) {
			original, response := modelDevDispatchFixture(t)
			entered, exited := make(chan struct{}, 1), make(chan error, 1)
			config, calls := startModelDevDispatchPeer(t, original, response, func(ctx context.Context, _ *modeldevv1.AcceptExecutionRequest) (*modeldevv1.AcceptExecutionResponse, error) {
				select {
				case entered <- struct{}{}:
				case <-ctx.Done():
					return nil, status.FromContextError(ctx.Err()).Err()
				}
				<-ctx.Done()
				select {
				case exited <- ctx.Err():
				default:
				}
				return nil, status.FromContextError(ctx.Err()).Err()
			})
			config.Timeout = 2 * time.Second
			client := newModelDevBoundaryClient(t, config)
			ctx, cancel := context.WithCancel(context.Background())
			want := error(context.Canceled)
			if strings.Contains(mode, "expired") {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			}
			if mode == "deadline in flight" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			if mode == "already canceled" {
				cancel()
			}
			if strings.HasPrefix(mode, "already") {
				got, err := client.AcceptExecution(ctx, original)
				if got != (ModelDevOwnerReceipt{}) || !errors.Is(err, want) || calls.Load() != 0 {
					t.Error("pre-canceled call lost its context cause or sent a command")
				}
				return
			}
			type outcome struct {
				receipt ModelDevOwnerReceipt
				err     error
			}
			done := make(chan outcome, 1)
			go func() { got, err := client.AcceptExecution(ctx, original); done <- outcome{got, err} }()
			finished := false
			defer func() {
				cancel()
				if !finished {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("sender goroutine exceeded cleanup bound")
					}
				}
			}()
			select {
			case <-entered:
			case <-done:
				finished = true
				t.Fatal("MODELDEV_DELIVERY_CLIENT_BEHAVIOR: sender returned before proven TLS peer entry")
			case <-time.After(3 * time.Second):
				t.Fatal("sender did not reach proven peer within bound")
			}
			if mode == "cancel in flight" {
				cancel()
			}
			select {
			case got := <-done:
				finished = true
				if got.receipt != (ModelDevOwnerReceipt{}) || !errors.Is(got.err, want) {
					t.Error("in-flight cancellation lost its original context cause")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("sender cancellation exceeded bound")
			}
			select {
			case err := <-exited:
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Error("peer lacks propagated context cancellation")
				}
			case <-time.After(3 * time.Second):
				t.Error("peer remained active after caller cancellation")
			}
			if calls.Load() != 1 {
				t.Error("canceled command retried")
			}
		})
	}
}

func TestModelDevDispatchBoundsUnavailableOwner(t *testing.T) {
	for _, mode := range []string{"own timeout", "closed connection"} {
		t.Run(mode, func(t *testing.T) {
			original, response := modelDevDispatchFixture(t)
			exited := make(chan struct{}, 1)
			config, calls := startModelDevDispatchPeer(t, original, response, func(ctx context.Context, _ *modeldevv1.AcceptExecutionRequest) (*modeldevv1.AcceptExecutionResponse, error) {
				<-ctx.Done()
				select {
				case exited <- struct{}{}:
				default:
				}
				return nil, status.FromContextError(ctx.Err()).Err()
			})
			config.Timeout = 500 * time.Millisecond
			client, closeClient, err := NewModelDevClient(config)
			if err != nil || client == nil || closeClient == nil {
				t.Fatal("valid client construction failed")
			}
			t.Cleanup(closeClient)
			if mode == "closed connection" {
				closeClient()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got, err := client.AcceptExecution(ctx, original)
			assertModelDevDispatchFailure(t, got, err, "OWNER_UNAVAILABLE", false)
			if ctx.Err() != nil {
				t.Error("client ignored its own finite timeout")
			}
			if mode == "closed connection" {
				if calls.Load() != 0 {
					t.Error("closed connection sent a command")
				}
				return
			}
			if calls.Load() != 1 {
				t.Error("finite timeout did not follow one actual peer call")
				return
			}
			select {
			case <-exited:
			case <-time.After(3 * time.Second):
				t.Error("timeout left the peer active")
			}
		})
	}
}

func TestModelDevDispatchRejectsCorruptOriginalWithoutSending(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*cpup01.AdmissionEnvelope)
	}{
		{"intent hash mismatch", func(a *cpup01.AdmissionEnvelope) { a.IntentHash = strings.Repeat("0", 64) }},
		{"spec hash mismatch", func(a *cpup01.AdmissionEnvelope) { a.SpecHash = strings.Repeat("0", 64) }},
		{"invalid actor namespace", func(a *cpup01.AdmissionEnvelope) { a.Actor = "untrusted:user:42" }},
		{"noncanonical actor", func(a *cpup01.AdmissionEnvelope) { a.Actor = "governance:user:042" }},
		{"zero operation", func(a *cpup01.AdmissionEnvelope) { a.OperationID = uuid.Nil.String() }},
		{"accepted time precision", func(a *cpup01.AdmissionEnvelope) { a.AcceptedAt = a.AcceptedAt.Add(time.Nanosecond) }},
		{"deadline precision", func(a *cpup01.AdmissionEnvelope) { a.Snapshot.DeadlineAt = a.Snapshot.DeadlineAt.Add(time.Nanosecond) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			original, response := modelDevDispatchFixture(t)
			config, calls := startModelDevDispatchPeer(t, original, response, func(context.Context, *modeldevv1.AcceptExecutionRequest) (*modeldevv1.AcceptExecutionResponse, error) {
				return response, nil
			})
			test.change(&original)

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got, err := newModelDevBoundaryClient(t, config).AcceptExecution(ctx, original)
			assertModelDevDispatchFailure(t, got, err, "INVALID_COMMAND", true)
			if calls.Load() != 0 {
				t.Error("corrupt frozen command reached the owner")
			}
		})
	}
}

func modelDevDispatchFixture(t *testing.T) (cpup01.AdmissionEnvelope, *modeldevv1.AcceptExecutionResponse) {
	t.Helper()
	base := modelDevResponseFixture(t)
	snapshot, err := contractpb.DecodeSnapshot(base.response.Snapshot)
	if err != nil {
		t.Fatal("MODELDEV_DELIVERY_CLIENT_PREFLIGHT: fixed snapshot invalid")
	}
	_, intentHash, err := cpup01.CanonicalIntent(base.intent)
	if err != nil {
		t.Fatal("MODELDEV_DELIVERY_CLIENT_PREFLIGHT: fixed intent invalid")
	}
	original := cpup01.AdmissionEnvelope{TenantID: base.scope.ResourceTenantID, Actor: base.scope.Actor,
		OperationID: base.identity.OperationID, ExecutionID: base.identity.ExecutionID, Intent: base.intent, IntentHash: intentHash,
		Snapshot: snapshot, SpecHash: base.response.ExecutionSpecHash, AcceptedAt: base.acceptedAt}
	if _, _, err := original.CanonicalPayloads(); err != nil {
		t.Fatal("MODELDEV_DELIVERY_CLIENT_PREFLIGHT: original invalid")
	}
	// Legal public axes need not equal the current implementation's first state.
	response := &modeldevv1.AcceptExecutionResponse{Identity: &trainingv1.ExecutionIdentity{OperationId: original.OperationID, ExecutionId: original.ExecutionID, ExecutionSpecHash: original.SpecHash},
		States: &modeldevv1.ExecutionStates{ComputeState: modeldevv1.ComputeState_COMPUTE_STATE_SUCCEEDED, DeliveryState: modeldevv1.DeliveryState_DELIVERY_STATE_PUBLISHED,
			ResourceState: modeldevv1.ResourceState_RESOURCE_STATE_NOT_APPLICABLE, CloseState: modeldevv1.CloseState_CLOSE_STATE_CLOSED}, Revision: 37, Replayed: true}
	return original, response
}

func assertModelDevDispatchFailure(t *testing.T, got ModelDevOwnerReceipt, err error, code string, permanent bool) {
	t.Helper()
	var failure *ModelDevDeliveryFailure
	if got != (ModelDevOwnerReceipt{}) || !errors.As(err, &failure) || failure == nil || failure.Code != code || failure.Permanent != permanent {
		t.Errorf("MODELDEV_DELIVERY_CLIENT_BEHAVIOR: want zero receipt and %s permanent=%t; typed failure=%t", code, permanent, failure != nil)
		return
	}
	if len(err.Error()) > 128 || strings.Contains(err.Error(), "private-delivery-sentinel") {
		t.Error("delivery error leaked remote content or has unbounded text")
	}
}

type modelDevDispatchPeer struct {
	modeldevv1.UnimplementedModelDevCommandServiceServer
	controlID string
	baseline  *modeldevv1.AcceptExecutionResponse
	handle    func(context.Context, *modeldevv1.AcceptExecutionRequest) (*modeldevv1.AcceptExecutionResponse, error)
	calls     atomic.Int32
}

func (peer *modelDevDispatchPeer) AcceptExecution(ctx context.Context, request *modeldevv1.AcceptExecutionRequest) (*modeldevv1.AcceptExecutionResponse, error) {
	if request.GetIdentity().GetOperationId() == peer.controlID {
		return proto.Clone(peer.baseline).(*modeldevv1.AcceptExecutionResponse), nil
	}
	peer.calls.Add(1)
	return peer.handle(ctx, request)
}

func startModelDevDispatchPeer(t *testing.T, original cpup01.AdmissionEnvelope, response *modeldevv1.AcceptExecutionResponse, handle func(context.Context, *modeldevv1.AcceptExecutionRequest) (*modeldevv1.AcceptExecutionResponse, error)) (ModelDevClientConfig, *atomic.Int32) {
	t.Helper()
	config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("MODELDEV_DELIVERY_CLIENT_PREFLIGHT: TLS socket unavailable")
	}
	config.Address = listener.Addr().String()
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	peer := &modelDevDispatchPeer{controlID: uuid.NewString(), baseline: response, handle: handle}
	modeldevv1.RegisterModelDevCommandServiceServer(server, peer)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				t.Error("delivery TLS peer did not stop cleanly")
			}
		case <-time.After(2 * time.Second):
			t.Error("delivery TLS peer stop exceeded bound")
		}
	})
	connection := modelDevDirectTLSConnection(t, config, "ani-modeldev-service")
	intent, intentErr := contractpb.EncodeIntent(original.Intent)
	snapshot, snapshotErr := contractpb.EncodeSnapshot(original.Snapshot)
	if intentErr != nil || snapshotErr != nil {
		t.Fatal("MODELDEV_DELIVERY_CLIENT_PREFLIGHT: fixed command cannot encode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	control, err := modeldevv1.NewModelDevCommandServiceClient(connection).AcceptExecution(ctx, &modeldevv1.AcceptExecutionRequest{
		Identity:         &trainingv1.ExecutionIdentity{OperationId: peer.controlID, ExecutionId: original.ExecutionID, ExecutionSpecHash: original.SpecHash},
		ResourceTenantId: original.TenantID, AdmittedActorId: original.Actor, IntentHash: original.IntentHash,
		Intent: intent, Snapshot: snapshot, AcceptedAt: timestamppb.New(original.AcceptedAt),
	}, grpc.WaitForReady(true))
	if err != nil || !proto.Equal(control, response) {
		t.Fatal("MODELDEV_DELIVERY_CLIENT_PREFLIGHT: generated TLS control failed; behavior NOT_RUN")
	}
	t.Log("MODELDEV_DELIVERY_CLIENT_PREFLIGHT PASS: actual TLS socket and generated control ACK; controlled peer, not owner persistence")
	return config, &peer.calls
}
