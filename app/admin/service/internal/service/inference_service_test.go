package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/stretchr/testify/require"
	commandv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/command/v1"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	viewv1 "go-wind-admin/api/gen/go/inference/view/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

type directInferenceOwnerProbe struct {
	createCalls int
	request     *inferencev1.CreateInferenceServiceRequest
	tenant      string
	actor       string
}

type inferenceModelProbe struct {
	response *modelv1.GetModelVersionResponse
	err      error
	tenant   string
	actor    string
	request  *modelv1.GetModelVersionRequest
	calls    int
}

func (p *inferenceModelProbe) GetModelVersion(_ context.Context, tenant, actor string, request *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error) {
	p.calls++
	p.tenant, p.actor, p.request = tenant, actor, request
	return p.response, p.err
}

func (p *directInferenceOwnerProbe) Create(context.Context, *inferencev1.CreateInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	panic("legacy create must not be used for a request without GPU")
}
func (p *directInferenceOwnerProbe) Delete(context.Context, *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	panic("unused")
}
func (p *directInferenceOwnerProbe) CreateTrusted(_ context.Context, tenant, actor string, request *inferencev1.CreateInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	p.createCalls++
	p.tenant, p.actor, p.request = tenant, actor, request
	return &inferencev1.OperationResponse{Resource: &inferencev1.InferenceService{Id: "svc-1"}, Operation: &inferencev1.Operation{Id: "op-1"}}, nil
}
func (p *directInferenceOwnerProbe) DeleteTrusted(context.Context, string, string, *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	panic("unused")
}
func (p *directInferenceOwnerProbe) GetTrusted(context.Context, string, string, *inferencev1.GetInferenceServiceRequest) (*inferencev1.InferenceService, error) {
	panic("unused")
}
func (p *directInferenceOwnerProbe) ListTrusted(context.Context, string, string, *inferencev1.ListInferenceServicesRequest) (*inferencev1.ListInferenceServicesResponse, error) {
	panic("unused")
}
func (p *directInferenceOwnerProbe) GetOperationTrusted(context.Context, string, string, *inferencev1.GetOperationRequest) (*inferencev1.Operation, error) {
	panic("unused")
}
func (p *directInferenceOwnerProbe) UpdateTrusted(context.Context, string, string, *inferencev1.UpdateInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	return &inferencev1.OperationResponse{Resource: &inferencev1.InferenceService{Id: "svc-1"}, Operation: &inferencev1.Operation{Id: "op-update"}}, nil
}
func (p *directInferenceOwnerProbe) StartTrusted(context.Context, string, string, *inferencev1.ServiceCommandRequest) (*inferencev1.OperationResponse, error) {
	return &inferencev1.OperationResponse{Resource: &inferencev1.InferenceService{Id: "svc-1"}, Operation: &inferencev1.Operation{Id: "op-start"}}, nil
}
func (p *directInferenceOwnerProbe) StopTrusted(context.Context, string, string, *inferencev1.ServiceCommandRequest) (*inferencev1.OperationResponse, error) {
	return &inferencev1.OperationResponse{Resource: &inferencev1.InferenceService{Id: "svc-1"}, Operation: &inferencev1.Operation{Id: "op-stop"}}, nil
}
func (p *directInferenceOwnerProbe) RestartTrusted(context.Context, string, string, *inferencev1.ServiceCommandRequest) (*inferencev1.OperationResponse, error) {
	return &inferencev1.OperationResponse{Resource: &inferencev1.InferenceService{Id: "svc-1"}, Operation: &inferencev1.Operation{Id: "op-restart"}}, nil
}
func (p *directInferenceOwnerProbe) ListOperationsTrusted(context.Context, string, string, *inferencev1.ListOperationsRequest) (*inferencev1.ListOperationsResponse, error) {
	return &inferencev1.ListOperationsResponse{Items: []*inferencev1.Operation{{Id: "op-1", ServiceId: "svc-1"}}, Total: 1}, nil
}

type directInferenceTenantProbe struct{}

func (directInferenceTenantProbe) ResourceTenantID(context.Context, uint32) (string, error) {
	return "11111111-1111-4111-8111-111111111111", nil
}

type inferenceComputeProbe struct {
	selection *compute.ComputeSelection
	calls     int
	err       error
}

func (p *inferenceComputeProbe) ResolveComputePreset(_ context.Context, tenant, actor string, request *compute.ResolveComputePresetRequest) (*compute.ComputeSelection, error) {
	p.calls++
	if request.TenantId != tenant || request.Usage != compute.Usage_INFERENCE {
		panic("untrusted compute identity")
	}
	return p.selection, p.err
}

func inferenceCPUSelection(t *testing.T) *compute.ComputeSelection {
	t.Helper()
	s := &compute.ComputeSelection{SchemaVersion: 1, PresetId: "40000000-0000-4000-8000-000000000001", Code: "cpu-test", Name: "CPU", ComputeType: "cpu", CpuMillis: 1501, MemoryMib: 2049}
	var err error
	s.SelectionDigest, err = compute.SelectionDigest(s)
	require.NoError(t, err)
	return s
}

func inferencePublicIntent() *view.CreateInferenceRequest {
	return &view.CreateInferenceRequest{IdempotencyKey: uuid.NewString(), PresetId: "40000000-0000-4000-8000-000000000001", Name: "qwen", ModelVersionId: "30000000-0000-4000-8000-000000000001", ServedModelName: "qwen", EngineType: "vllm", EngineImage: "registry/vllm:fixed", Command: "exec vllm serve /models --served-model-name qwen", Replicas: 1}
}

func TestInferenceServiceRejectsRawResourceCreate(t *testing.T) {
	owner := &directInferenceOwnerProbe{}
	model := &inferenceModelProbe{}
	s := NewInferenceServiceWithOwnerAndModel(nil, owner, directInferenceTenantProbe{}, model)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42})
	request := &view.CreateInferenceRequest{
		IdempotencyKey: "direct-create-1", Name: "qwen-direct", ModelVersionId: "version-1", ServedModelName: "qwen-direct", Replicas: 1,
		EngineType: "vllm", EngineImage: "registry/vllm:latest", Command: "python -m vllm.entrypoints.openai.api_server --served-model-name qwen-direct", Cpu: "1", Memory: "2Gi",
	}
	_, err := s.CreateInference(ctx, request)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Zero(t, owner.createCalls)
	require.Zero(t, model.calls)
}

func TestInferenceServiceRejectsModelThatIsNotReady(t *testing.T) {
	model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Version: &modelv1.ModelVersion{Id: "version-1", Status: "pending"}}}
	binding := NewInferenceComputeBinding(nil, nil, model, &inferenceComputeProbe{selection: inferenceCPUSelection(t)})
	_, err := binding.PrepareManagedBusiness(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42}, "11111111-1111-4111-8111-111111111111", inferencePublicIntent())
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, 1, model.calls)
}

func TestInferenceServiceChecksModelBeforeManagedCreate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		version  *modelv1.ModelVersion
		wantCode codes.Code
		calls    int
	}{
		{name: "not ready", version: &modelv1.ModelVersion{Id: "version-1", Status: "pending"}, wantCode: codes.FailedPrecondition},
		{name: "wrong version", version: &modelv1.ModelVersion{Id: "version-2", Status: "ready"}, wantCode: codes.FailedPrecondition},
		{name: "ready matching version", version: &modelv1.ModelVersion{Id: "version-1", Status: "ready"}, wantCode: codes.OK, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Version: tc.version}}
			binding := NewInferenceComputeBinding(nil, nil, model, &inferenceComputeProbe{selection: inferenceCPUSelection(t)})
			intent := inferencePublicIntent()
			intent.ModelVersionId = "version-1"
			prepared, err := binding.PrepareManagedBusiness(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42}, "11111111-1111-4111-8111-111111111111", intent)
			require.Equal(t, tc.wantCode, status.Code(err))
			require.Equal(t, 1, model.calls)
			if tc.calls == 1 {
				require.NotNil(t, prepared)
			}
		})
	}
}

func TestInferenceServiceExposesTrustedLifecycleCommands(t *testing.T) {
	owner := &directInferenceOwnerProbe{}
	model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Version: &modelv1.ModelVersion{Id: "version-1", Status: "ready"}}}
	s := NewInferenceServiceWithOwnerAndModel(nil, owner, directInferenceTenantProbe{}, model)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42})
	update, err := s.UpdateInference(ctx, &view.UpdateInferenceRequest{
		IdempotencyKey: "20000000-0000-4000-8000-000000000004", ResourceId: "20000000-0000-4000-8000-000000000002", ExpectedGeneration: 2, ModelVersionId: "30000000-0000-4000-8000-000000000001",
		UpdateMask:      &fieldmaskpb.FieldMask{Paths: []string{"model_version_id", "served_model_name", "engine"}},
		ServedModelName: "qwen", EngineType: "vllm", EngineImage: "registry/vllm:latest",
		Command: "vllm serve /models --served-model-name qwen",
	})
	require.NoError(t, err)
	require.Equal(t, "op-update", update.OperationId)
	for _, tc := range []struct {
		name string
		call func(context.Context, *view.InferenceServiceCommandRequest) (*view.InferenceAcceptance, error)
		want string
	}{
		{name: "start", call: s.StartInference, want: "op-start"},
		{name: "stop", call: s.StopInference, want: "op-stop"},
		{name: "restart", call: s.RestartInference, want: "op-restart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.call(ctx, &view.InferenceServiceCommandRequest{IdempotencyKey: tc.name + "-1", ResourceId: "svc-1", ExpectedGeneration: 2})
			require.NoError(t, err)
			require.Equal(t, tc.want, out.OperationId)
		})
	}
	operations, err := s.ListInferenceOperations(ctx, &view.ListInferenceOperationsRequest{ResourceId: "svc-1"})
	require.NoError(t, err)
	require.Len(t, operations.Items, 1)
}

func TestInferenceServiceRejectsRawGPUPlacement(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     viewv1.RuntimeMode
		workers  int32
		replicas int32
	}{
		{name: "deployment", mode: viewv1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT, replicas: 3},
		{name: "lws", mode: viewv1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, workers: 2, replicas: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &InferenceService{}
			_, err := s.buildCreateRequest(&view.CreateInferenceRequest{IdempotencyKey: "20000000-0000-4000-8000-000000000001", Name: "qwen", ModelVersionId: "version-1", ServedModelName: "qwen", EngineType: "vllm", EngineImage: "image", Command: "serve", Cpu: "1", Memory: "2Gi", Replicas: tc.replicas, RuntimeMode: tc.mode, WorkerReplicas: tc.workers, Gpu: &view.GpuSelection{ClusterId: "cluster", PoolId: "pool", ProfileId: "profile", ProfileVersion: 1}})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestInferenceServiceRejectsInvalidFlatCreateFields(t *testing.T) {
	base := &view.CreateInferenceRequest{IdempotencyKey: "create", Name: "qwen", ModelVersionId: "version-1", ServedModelName: "qwen", EngineType: "vllm", EngineImage: "image", Command: "serve", Cpu: "1", Memory: "2Gi", Replicas: 1}
	for name, mutate := range map[string]func(*view.CreateInferenceRequest){
		"missing command":       func(d *view.CreateInferenceRequest) { d.Command = "" },
		"zero replicas":         func(d *view.CreateInferenceRequest) { d.Replicas = 0 },
		"bad cpu":               func(d *view.CreateInferenceRequest) { d.Cpu = "0" },
		"deployment workers":    func(d *view.CreateInferenceRequest) { d.WorkerReplicas = 1 },
		"partial gpu selection": func(d *view.CreateInferenceRequest) { d.Gpu = &view.GpuSelection{ClusterId: "cluster", PoolId: "pool"} },
		"zero lws workers": func(d *view.CreateInferenceRequest) {
			d.RuntimeMode = viewv1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := proto.Clone(base).(*view.CreateInferenceRequest)
			mutate(d)
			_, err := (&InferenceService{}).buildCreateRequest(d)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

// This endpoint double verifies the adapter's wire contract only. It does not
// stand in for Inference's business receipt/PG or the software integration gate.
type inferenceWirePeer struct {
	create *inferencev1.CreateInferenceServiceRequest
	delete *inferencev1.DeleteInferenceServiceRequest
	ack    *commandv1.DurableOwnerAck
}

func (p *inferenceWirePeer) Create(_ context.Context, r *inferencev1.CreateInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	p.create = r
	return &inferencev1.OperationResponse{DurableOwnerAck: p.ack}, nil
}
func (p *inferenceWirePeer) Delete(_ context.Context, r *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	p.delete = r
	return &inferencev1.OperationResponse{DurableOwnerAck: p.ack}, nil
}
func (p *inferenceWirePeer) CreateTrusted(ctx context.Context, _ string, _ string, r *inferencev1.CreateInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	return p.Create(ctx, r)
}
func (p *inferenceWirePeer) DeleteTrusted(ctx context.Context, _ string, _ string, r *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	return p.Delete(ctx, r)
}

func inferenceBusiness(t *testing.T) (*inferencev1.CreateInferenceServiceRequest, *QuotaDispatchCommand) {
	t.Helper()
	plan, _, _ := gpuVector(t)
	plan.Request.ContainerName = "kserve-container"
	g := plan.Request
	r := &inferencev1.CreateInferenceServiceRequest{Name: "wire", ModelVersionId: "30000000-0000-4000-8000-000000000001", Replicas: int32(g.Replicas), Resource: &inferencev1.ResourceSpec{Requests: map[string]string{"cpu": "1"}, Limits: map[string]string{"memory": "8Gi"}, Gpu: &inferencev1.GpuRequest{ClusterId: g.ClusterId, PoolId: g.PoolId, ProfileId: g.ProfileId, ProfileVersion: g.ProfileVersion, Replicas: g.Replicas, DevicesPerReplica: g.DevicesPerReplica, ContainerName: g.ContainerName}}, Engine: &inferencev1.EngineSpec{Type: "vllm", Image: "test/image@sha256:fixture", Command: "server --fixture"}}
	command := &QuotaDispatchCommand{OperationID: "20000000-0000-4000-8000-000000000001", ResourceID: "20000000-0000-4000-8000-000000000002", ResourceTenantID: "20000000-0000-4000-8000-000000000003", Actor: QuotaActor{Type: "user", ID: "7"}, Action: InferenceCreateAction, RequestHash: "hash"}
	return r, command
}

func TestInferenceManagedDispatchRejectsRetiredGpuCanonical(t *testing.T) {
	_, command := inferenceBusiness(t)
	peer := &inferenceWirePeer{}
	_, err := NewInferenceGpuBinding(peer, nil).Dispatch(context.Background(), command)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Nil(t, peer.create)
	require.Nil(t, peer.delete)
}

func TestInferenceManagedDispatchUsesStoredCommandContract(t *testing.T) {
	const tenant = "11111111-1111-4111-8111-111111111111"
	const operation = "20000000-0000-4000-8000-000000000001"
	const resource = "20000000-0000-4000-8000-000000000002"
	peer := &inferenceWirePeer{ack: &commandv1.DurableOwnerAck{OperationId: operation, ResourceId: resource, Accepted: true}}
	binding := NewInferenceComputeBinding(peer, nil, &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Version: &modelv1.ModelVersion{Id: "30000000-0000-4000-8000-000000000001", Status: "ready"}}}, &inferenceComputeProbe{selection: inferenceCPUSelection(t)})
	prepared, err := binding.PrepareManagedBusiness(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42}, tenant, inferencePublicIntent())
	require.NoError(t, err)
	raw, err := prepared.freeze(tenant, "user", "7", operation, resource, nil)
	require.NoError(t, err)
	_, err = binding.Dispatch(context.Background(), &QuotaDispatchCommand{OperationID: operation, ResourceID: resource, ResourceTenantID: tenant, Actor: QuotaActor{Type: "user", ID: "7"}, Action: InferenceCreateAction, CanonicalRequest: raw})
	require.NoError(t, err)
	require.Equal(t, operation, peer.create.GetRequestId())
	require.Equal(t, tenant, peer.create.GetContext().GetTenantId())
	require.Equal(t, resource, peer.create.GetContext().GetResourceId())
}

func TestInferencePublicBusinessRejectsAttachmentAndGPUBypass(t *testing.T) {
	r, _ := inferenceBusiness(t)
	binding := NewInferenceGpuBinding(nil, nil)
	_, err := binding.ValidateGpuBusiness(context.Background(), r)
	require.NoError(t, err)
	r.Resource.Limits["nvidia.com/gpu"] = "1"
	_, err = binding.ValidateGpuBusiness(context.Background(), r)
	require.Error(t, err)
	delete(r.Resource.Limits, "nvidia.com/gpu")
	r.ProtoReflect().SetUnknown([]byte{0xa2, 0x01, 0x00})
	_, err = binding.CanonicalGpuBusiness(r)
	require.Error(t, err)
	require.Error(t, binding.AuthorizeGpu(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 1}, InferenceCreateAction, ""))
}

func TestInferenceMissingAuthoritativeModuleCannotEnableFromConfig(t *testing.T) {
	err := data.NewInferenceAuthorizationRepo(nil).Authorize(context.Background(), 1, 7, data.InferenceCreatePath)
	if !data.InferenceModuleRegistered() {
		require.ErrorIs(t, err, data.ErrInferenceAuthorizationDenied)
	} else {
		require.ErrorIs(t, err, data.ErrInferenceAuthorizationUnavailable)
	}
}
