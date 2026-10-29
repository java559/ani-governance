package data

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// workloadPeer substitutes only the external RPC. Delegation metadata, response
// validation and projection are the production code.
type workloadPeer struct {
	modeldevv1.UnimplementedModelDevQueryServiceServer
	scope       ModelDevResolveScope
	pods        *modeldevv1.ListExecutionPodsResponse
	events      *modeldevv1.ListExecutionEventsResponse
	execution   string
	lastLayer   modeldevv1.PodLayer
	lastScope   string
	lastRecycle bool
	remote      error
}

func (p *workloadPeer) check(ctx context.Context, method string) error {
	md, _ := metadata.FromIncomingContext(ctx)
	for key, want := range map[string]string{"x-ani-tenant-id": p.scope.ResourceTenantID, "x-ani-actor": p.scope.Actor, "x-ani-data-scope": "tenant-all", "x-ani-authorized-method": method} {
		got := md.Get(key)
		if len(got) != 1 || got[0] != want {
			return status.Error(codes.Internal, "delegation mismatch")
		}
	}
	return p.remote
}

func (p *workloadPeer) ListExecutionPods(ctx context.Context, in *modeldevv1.ListExecutionPodsRequest) (*modeldevv1.ListExecutionPodsResponse, error) {
	if err := p.check(ctx, modeldevv1.ModelDevQueryService_ListExecutionPods_FullMethodName); err != nil {
		return nil, err
	}
	if in.ExecutionId != p.execution {
		return nil, status.Error(codes.Internal, "selector mismatch")
	}
	p.lastLayer, p.lastRecycle = in.Layer, in.IncludeRecycled
	return p.pods, nil
}

func (p *workloadPeer) ListExecutionEvents(ctx context.Context, in *modeldevv1.ListExecutionEventsRequest) (*modeldevv1.ListExecutionEventsResponse, error) {
	if err := p.check(ctx, modeldevv1.ModelDevQueryService_ListExecutionEvents_FullMethodName); err != nil {
		return nil, err
	}
	if in.ExecutionId != p.execution {
		return nil, status.Error(codes.Internal, "selector mismatch")
	}
	p.lastScope = in.Scope
	return p.events, nil
}

func workloadClient(t *testing.T, peer *workloadPeer) (*ModelDevClient, ModelDevResolveScope) {
	t.Helper()
	config, security := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	config.Address = listener.Addr().String()
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	modeldevv1.RegisterModelDevQueryServiceServer(server, peer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client, closeClient, err := NewModelDevClient(config)
	require.NoError(t, err)
	t.Cleanup(closeClient)
	return client, peer.scope
}

func podView(phase string, availability modeldevv1.PodAvailability, layer modeldevv1.PodLayer) *modeldevv1.PodView {
	return &modeldevv1.PodView{
		PodName: "md-pod", PodUid: uuid.NewString(), Phase: phase, Layer: layer, Availability: availability,
		CreatedAt: timestamppb.Now(), LogId: uuid.NewString(), ApiPresent: availability == modeldevv1.PodAvailability_POD_AVAILABILITY_LIVE,
		Containers: []*modeldevv1.ContainerView{{Name: "node", Type: "container", State: "running"}},
	}
}

func TestListExecutionPodsPassesLayerAndValidatesEachPod(t *testing.T) {
	id := uuid.NewString()
	peer := &workloadPeer{
		scope:     ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"},
		execution: id,
		pods: &modeldevv1.ListExecutionPodsResponse{ObservedAt: timestamppb.Now(), Availability: modeldevv1.PodAvailability_POD_AVAILABILITY_LIVE,
			Pods: []*modeldevv1.PodView{podView("Running", modeldevv1.PodAvailability_POD_AVAILABILITY_LIVE, modeldevv1.PodLayer_POD_LAYER_TRAINER)}},
	}
	client, scope := workloadClient(t, peer)
	out, err := client.ListExecutionPods(context.Background(), scope, id, WorkloadLayerTrainer, true)
	require.NoError(t, err)
	require.Len(t, out.Pods, 1)
	require.Equal(t, modeldevv1.PodLayer_POD_LAYER_TRAINER, peer.lastLayer)
	require.True(t, peer.lastRecycle)

	for _, test := range []struct {
		name   string
		mutate func(*modeldevv1.PodView)
	}{
		{"synthetic phase", func(p *modeldevv1.PodView) { p.Phase = "RECYCLED" }},
		{"unknown availability", func(p *modeldevv1.PodView) { p.Availability = 987 }},
		{"duplicate pod uid", nil},
		{"foreign layer", func(p *modeldevv1.PodView) { p.Layer = modeldevv1.PodLayer_POD_LAYER_PIPELINE }},
		{"unknown container type", func(p *modeldevv1.PodView) { p.Containers[0].Type = "sidecar" }},
		{"non-uuid log id", func(p *modeldevv1.PodView) { p.LogId = "v2:abc" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := &modeldevv1.ListExecutionPodsResponse{ObservedAt: timestamppb.Now(), Pods: []*modeldevv1.PodView{podView("Running", modeldevv1.PodAvailability_POD_AVAILABILITY_LIVE, modeldevv1.PodLayer_POD_LAYER_TRAINER)}}
			if test.name == "duplicate pod uid" {
				response.Pods = append(response.Pods, response.Pods[0])
			} else if test.mutate != nil {
				test.mutate(response.Pods[0])
			}
			peer.pods = response
			_, err := client.ListExecutionPods(context.Background(), scope, id, WorkloadLayerTrainer, false)
			require.Equal(t, codes.Unavailable, status.Code(err))
		})
	}
}

func TestListExecutionEventsValidatesProjection(t *testing.T) {
	id := uuid.NewString()
	valid := &modeldevv1.EventView{Type: "Warning", Reason: "FailedScheduling", Message: "0/3 nodes", Source: "kube-scheduler",
		InvolvedKind: "Pod", InvolvedName: "md-pod", InvolvedUid: uuid.NewString(),
		FirstTimestamp: timestamppb.Now(), LastTimestamp: timestamppb.Now(), Count: 3}
	peer := &workloadPeer{
		scope:     ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:73"},
		execution: id,
		events:    &modeldevv1.ListExecutionEventsResponse{ObservedAt: timestamppb.Now(), RetentionHint: "1h", Events: []*modeldevv1.EventView{valid}},
	}
	client, scope := workloadClient(t, peer)
	out, err := client.ListExecutionEvents(context.Background(), scope, id, EventScopeWarning)
	require.NoError(t, err)
	require.Len(t, out.Events, 1)
	require.Equal(t, "warning", peer.lastScope)

	for _, test := range []struct {
		name   string
		mutate func(*modeldevv1.EventView)
	}{
		{"unknown type", func(e *modeldevv1.EventView) { e.Type = "Info" }},
		{"missing reason", func(e *modeldevv1.EventView) { e.Reason = "" }},
		{"missing message", func(e *modeldevv1.EventView) { e.Message = "" }},
		{"negative count", func(e *modeldevv1.EventView) { e.Count = -1 }},
		{"reversed timestamps", func(e *modeldevv1.EventView) {
			e.FirstTimestamp = timestamppb.New(e.LastTimestamp.AsTime().Add(time.Minute))
		}},
		{"non-uuid involved uid", func(e *modeldevv1.EventView) { e.InvolvedUid = "pod-name" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := proto.Clone(valid).(*modeldevv1.EventView)
			test.mutate(event)
			peer.events = &modeldevv1.ListExecutionEventsResponse{ObservedAt: timestamppb.Now(), Events: []*modeldevv1.EventView{event}}
			_, err := client.ListExecutionEvents(context.Background(), scope, id, EventScopeKey)
			require.Equal(t, codes.Unavailable, status.Code(err))
		})
	}
}

// The selection is validated before any downstream call so a malformed selector
// never reaches the owner.
func TestLogSelectionValidation(t *testing.T) {
	valid := LogSelection{PodUID: uuid.NewString(), Container: "node", Previous: true}
	require.True(t, modelDevValidLogSelection(valid))
	require.False(t, modelDevValidLogSelection(LogSelection{PodUID: "not-a-uuid"}))
	require.False(t, modelDevValidLogSelection(LogSelection{PodUID: valid.PodUID, Container: "Node"}))
	require.False(t, modelDevValidLogSelection(LogSelection{PodUID: valid.PodUID, Container: strings.Repeat("a", 64)}))
}

// Only a step container may differ from the worker default; a worker response
// that claims another container is refused.
func TestLogSourceBinding(t *testing.T) {
	logID := uuid.NewString()
	podUID := uuid.NewString()
	worker := &modeldevv1.GetExecutionLogsResponse{
		Source:     &trainingv1.LogRef{LogId: logID, ResourceUid: podUID, ContainerName: "node"},
		SourceKind: modeldevv1.LogSourceKind_LOG_SOURCE_KIND_TRAINER, ContainerName: "node",
	}
	require.True(t, modelDevValidLogSource(worker, LogSelection{PodUID: podUID}))
	require.False(t, modelDevValidLogSource(worker, LogSelection{}))
	require.False(t, modelDevValidLogSource(worker, LogSelection{PodUID: podUID, Container: "main"}))

	step := &modeldevv1.GetExecutionLogsResponse{
		Source:     &trainingv1.LogRef{LogId: logID, ResourceUid: podUID, ContainerName: "main"},
		SourceKind: modeldevv1.LogSourceKind_LOG_SOURCE_KIND_KFP_STEP, ContainerName: "main",
	}
	require.True(t, modelDevValidLogSource(step, LogSelection{PodUID: podUID, Container: "main"}))
	require.False(t, modelDevValidLogSource(step, LogSelection{}))

	unknown := proto.Clone(worker).(*modeldevv1.GetExecutionLogsResponse)
	unknown.SourceKind = modeldevv1.LogSourceKind_LOG_SOURCE_KIND_UNSPECIFIED
	require.False(t, modelDevValidLogSource(unknown, LogSelection{}))
}
