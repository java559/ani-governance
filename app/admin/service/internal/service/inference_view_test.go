package service

import (
	"context"
	"testing"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type inferenceReadOwnerProbe struct {
	service *inferencev1.InferenceService
	list    *inferencev1.ListInferenceServicesResponse
}

func (*inferenceReadOwnerProbe) Create(context.Context, *inferencev1.CreateInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	panic("unused")
}

func (*inferenceReadOwnerProbe) Delete(context.Context, *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	panic("unused")
}

func (p *inferenceReadOwnerProbe) GetTrusted(context.Context, string, string, *inferencev1.GetInferenceServiceRequest) (*inferencev1.InferenceService, error) {
	return p.service, nil
}

func (p *inferenceReadOwnerProbe) ListTrusted(context.Context, string, string, *inferencev1.ListInferenceServicesRequest) (*inferencev1.ListInferenceServicesResponse, error) {
	return p.list, nil
}

func TestInferenceServiceGetAndListProjectFlatViews(t *testing.T) {
	source := &inferencev1.InferenceService{Id: "svc-1", Name: "qwen", Generation: 9, Engine: &inferencev1.EngineSpec{Command: "exec serve"}}
	owner := &inferenceReadOwnerProbe{service: source, list: &inferencev1.ListInferenceServicesResponse{Items: []*inferencev1.InferenceService{source}, Total: 1}}
	s := NewInferenceServiceWithOwner(nil, owner, directInferenceTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42})

	got, err := s.GetInference(ctx, &view.GetInferenceRequest{ResourceId: "svc-1"})
	require.NoError(t, err)
	require.Equal(t, "exec serve", got.GetCommand())
	require.Nil(t, got.ProtoReflect().Descriptor().Fields().ByName("engine"))

	page, pageSize := int32(1), int32(10)
	list, err := s.ListInference(ctx, &view.ListInferenceRequest{Page: &page, PageSize: &pageSize})
	require.NoError(t, err)
	require.Equal(t, int64(1), list.GetTotal())
	require.Len(t, list.GetItems(), 1)
	require.Equal(t, "svc-1", list.GetItems()[0].GetId())
}

func TestMapInferenceViewProjectsFlatCommandAndGPU(t *testing.T) {
	source := &inferencev1.InferenceService{
		Id:                "svc-1",
		Name:              "qwen",
		DesiredState:      "RUNNING",
		Generation:        7,
		AppliedGeneration: 7,
		ModelVersionId:    "version-1",
		ServedModelName:   "qwen-served",
		RuntimePhase:      "Ready",
		PublicationPhase:  "Published",
		InvocationHealth:  "healthy",
		ObservedAt:        timestamppb.New(time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC)),
		Replicas:          2,
		Resource: &inferencev1.ResourceSpec{
			Requests: map[string]string{"cpu": "2", "memory": "8Gi"},
			Gpu:      &inferencev1.GpuRequest{ClusterId: "cluster-1", PoolId: "pool-1", ProfileId: "profile-1", ProfileVersion: 3},
		},
		Runtime:       &inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, WorkerReplicas: 4},
		Engine:        &inferencev1.EngineSpec{Image: "registry.example/vllm:1", Type: "vllm", Command: "python -m server --model qwen"},
		ModelArtifact: &inferencev1.ModelArtifact{Provider: "must-not-leak"},
	}

	got := mapInferenceView(source)
	require.Equal(t, "svc-1", got.GetId())
	require.Equal(t, "qwen", got.GetName())
	require.Equal(t, "RUNNING", got.GetDesiredState())
	require.Equal(t, int64(7), got.GetGeneration())
	require.Equal(t, "version-1", got.GetModelVersionId())
	require.Equal(t, "qwen-served", got.GetServedModelName())
	require.Equal(t, "Ready", got.GetPhase())
	require.Equal(t, "Published", got.GetPublicationPhase())
	require.Equal(t, int64(7), got.GetAppliedGeneration())
	require.Equal(t, "healthy", got.GetHealth())
	require.Equal(t, "registry.example/vllm:1", got.GetEngineImage())
	require.Equal(t, "vllm", got.GetEngineType())
	require.Equal(t, "python -m server --model qwen", got.GetCommand())
	require.Equal(t, "2", got.GetCpu())
	require.Equal(t, "8Gi", got.GetMemory())
	require.Equal(t, int32(2), got.GetReplicas())
	require.Equal(t, int32(inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET), int32(got.GetRuntimeMode()))
	require.Equal(t, int32(4), got.GetWorkerReplicas())
	require.Equal(t, "cluster-1", got.GetGpu().GetClusterId())
	require.Equal(t, "pool-1", got.GetGpu().GetPoolId())
	require.Equal(t, "profile-1", got.GetGpu().GetProfileId())
	require.Equal(t, uint64(3), got.GetGpu().GetProfileVersion())

	fields := got.ProtoReflect().Descriptor().Fields()
	for _, name := range []string{"resource", "runtime", "engine", "model_artifact", "attachments", "args"} {
		require.Nil(t, fields.ByName(protoreflect.Name(name)), "public view must not expose %s", name)
	}
}
