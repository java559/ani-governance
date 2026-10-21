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
	"go-wind-admin/app/admin/service/internal/commandcontract"
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
	var e error
	s.SelectionDigest, e = compute.SelectionDigest(s)
	require.NoError(t, e)
	return s
}
func inferencePublicIntent() *view.CreateInferenceData {
	return &view.CreateInferenceData{IdempotencyKey: uuid.NewString(), PresetId: "40000000-0000-4000-8000-000000000001", Name: "qwen", ModelVersionId: "30000000-0000-4000-8000-000000000001", ServedModelName: "qwen", EngineType: "vllm", EngineImage: "registry/vllm:fixed", Command: "exec vllm serve /models --served-model-name qwen", Replicas: 1}
}
func TestInferenceServiceCreatesWithoutGPUThroughTrustedOwner(t *testing.T) {
	owner := &directInferenceOwnerProbe{}
	model := &inferenceModelProbe{}
	s := NewInferenceServiceWithOwnerAndModel(nil, owner, directInferenceTenantProbe{}, model)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42})
	_, e := s.CreateInference(ctx, &view.CreateInferenceRequest{Data: inferencePublicIntent()})
	require.Error(t, e, "managed CPU requires durable acceptance")
	require.Zero(t, owner.createCalls)
	require.Zero(t, model.calls)
}
func TestInferenceServiceRejectsModelThatIsNotReady(t *testing.T) {
	public := inferencePublicIntent()
	model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Version: &modelv1.ModelVersion{Id: public.ModelVersionId, Status: "pending"}}}
	b := NewInferenceComputeBinding(nil, nil, model, &inferenceComputeProbe{selection: inferenceCPUSelection(t)})
	_, e := b.PrepareManagedBusiness(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42}, "11111111-1111-4111-8111-111111111111", public)
	require.Equal(t, codes.FailedPrecondition, status.Code(e))
	require.Equal(t, 1, model.calls)
}
func TestInferenceServiceChecksModelBeforeDirectCreate(t *testing.T) {
	public := inferencePublicIntent()
	model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Version: &modelv1.ModelVersion{Id: public.ModelVersionId, Status: "ready"}}}
	computePeer := &inferenceComputeProbe{selection: inferenceCPUSelection(t)}
	b := NewInferenceComputeBinding(nil, nil, model, computePeer)
	tenant := "11111111-1111-4111-8111-111111111111"
	prepared, e := b.PrepareManagedBusiness(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42}, tenant, public)
	require.NoError(t, e)
	require.Nil(t, prepared.gpu)
	raw, e := prepared.freeze(tenant, "user", "7", "20000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000002", nil)
	require.NoError(t, e)
	facts, e := commandcontract.ValidateStoredCommand(commandcontract.StoredCommand{Owner: commandcontract.InferenceOwner, Action: InferenceCreateAction, TenantID: tenant, OperationID: "20000000-0000-4000-8000-000000000001", ResourceID: "20000000-0000-4000-8000-000000000002", ActorType: "user", ActorID: "7", Canonical: raw, RequireChargeIDs: true})
	require.NoError(t, e)
	require.True(t, facts.CPU)
	business, e := inferencev1.DecodeBusinessPayload(facts.BusinessPayload)
	require.NoError(t, e)
	require.Equal(t, "1501m", business.Resource.Requests["cpu"])
	require.Equal(t, "2049Mi", business.Resource.Limits["memory"])
	require.Equal(t, public.Command, business.Engine.Command)
	require.Equal(t, 1, model.calls)
	require.Equal(t, 1, computePeer.calls)
}

func TestInferenceServiceExposesTrustedLifecycleCommands(t *testing.T) {
	owner := &directInferenceOwnerProbe{}
	model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Version: &modelv1.ModelVersion{Id: "version-1", Status: "ready"}}}
	s := NewInferenceServiceWithOwnerAndModel(nil, owner, directInferenceTenantProbe{}, model)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42})
	update, err := s.UpdateInference(ctx, &view.UpdateInferenceRequest{Data: &view.UpdateInferenceData{
		IdempotencyKey: "update-1", ResourceId: "20000000-0000-4000-8000-000000000002", ExpectedGeneration: 2, ModelVersionId: "30000000-0000-4000-8000-000000000001", UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"model_version_id", "served_model_name", "engine"}},
		ServedModelName: "qwen", EngineType: "vllm", EngineImage: "registry/vllm:latest",
		Command: "vllm serve /models --served-model-name qwen",
	}})
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
			out, err := tc.call(ctx, &view.InferenceServiceCommandRequest{Data: &view.InferenceServiceCommandData{IdempotencyKey: tc.name + "-1", ResourceId: "svc-1", ExpectedGeneration: 2}})
			require.NoError(t, err)
			require.Equal(t, tc.want, out.OperationId)
		})
	}
	operations, err := s.ListInferenceOperations(ctx, &view.ListInferenceOperationsRequest{ResourceId: "svc-1"})
	require.NoError(t, err)
	require.Len(t, operations.Items, 1)
}

func TestInferenceServiceDerivesGPUPlacementForDeploymentAndLWS(t *testing.T) {
	topology, e := inferencev1.NormalizeTopology(&inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT}, 3, 2)
	require.NoError(t, e)
	require.Equal(t, uint32(3), topology.ExecutionReplicas)
	require.Equal(t, uint32(2), topology.DevicesPerReplica)
	require.Equal(t, int64(6), topology.TotalDevices)
	_, e = inferencev1.NormalizeTopology(&inferencev1.RuntimeSpec{Mode: inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, WorkerReplicas: 2}, 3, 1)
	require.Error(t, e, "GPU LWS is rejected before admission")
}
func TestInferenceServiceRejectsInvalidFlatCreateFields(t *testing.T) {
	base := inferencePublicIntent()
	for name, mutate := range map[string]func(*view.CreateInferenceData){"missing command": func(d *view.CreateInferenceData) { d.Command = "" }, "missing preset": func(d *view.CreateInferenceData) { d.PresetId = "" }, "raw CPU": func(d *view.CreateInferenceData) { d.Cpu = "1" }, "raw memory": func(d *view.CreateInferenceData) { d.Memory = "1Gi" }, "raw GPU": func(d *view.CreateInferenceData) { d.Gpu = &view.GpuSelection{} }, "zero replicas": func(d *view.CreateInferenceData) { d.Replicas = 0 }, "deployment workers": func(d *view.CreateInferenceData) { d.WorkerReplicas = 2 }} {
		t.Run(name, func(t *testing.T) {
			d := proto.Clone(base).(*view.CreateInferenceData)
			mutate(d)
			_, e := new(InferenceService).buildCreateRequest(d)
			require.Equal(t, codes.InvalidArgument, status.Code(e))
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

func inferenceBusiness(t *testing.T) (*inferencev1.CreateInferenceServiceRequest, *QuotaDispatchCommand) {
	t.Helper()
	plan, _, _ := gpuVector(t)
	plan.Request.ContainerName = "kserve-container"
	g := plan.Request
	r := &inferencev1.CreateInferenceServiceRequest{Name: "wire", ModelVersionId: "30000000-0000-4000-8000-000000000001", Replicas: int32(g.Replicas), Resource: &inferencev1.ResourceSpec{Requests: map[string]string{"cpu": "1"}, Limits: map[string]string{"memory": "8Gi"}, Gpu: &inferencev1.GpuRequest{ClusterId: g.ClusterId, PoolId: g.PoolId, ProfileId: g.ProfileId, ProfileVersion: g.ProfileVersion, Replicas: g.Replicas, DevicesPerReplica: g.DevicesPerReplica, ContainerName: g.ContainerName}}, Engine: &inferencev1.EngineSpec{Type: "vllm", Image: "test/image@sha256:fixture", Command: "server --fixture"}}
	command := &QuotaDispatchCommand{OperationID: "20000000-0000-4000-8000-000000000001", ResourceID: "20000000-0000-4000-8000-000000000002", ResourceTenantID: "20000000-0000-4000-8000-000000000003", Actor: QuotaActor{Type: "user", ID: "7"}, Action: InferenceCreateAction, RequestHash: "hash"}
	return r, command
}

func TestInferenceGPUExecutionRemainsDisabled(t *testing.T) {
	_, command := inferenceBusiness(t)
	peer := &inferenceWirePeer{}
	_, err := NewInferenceGpuBinding(peer, nil).Dispatch(context.Background(), command)
	require.Error(t, err)
	require.Nil(t, peer.create)
	require.Nil(t, peer.delete)
}

func TestInferencePublicBusinessRejectsAttachmentAndGPUBypass(t *testing.T) {
	r := inferencePublicIntent()
	binding := NewInferenceGpuBinding(nil, nil)
	_, err := binding.ValidateGpuBusiness(context.Background(), r)
	require.NoError(t, err)
	r.Gpu = &view.GpuSelection{}
	_, err = binding.ValidateGpuBusiness(context.Background(), r)
	require.Error(t, err)
	r.Gpu = nil
	r.ProtoReflect().SetUnknown([]byte{0xa2, 0x01, 0x00})
	_, err = binding.IntentHash("11111111-1111-4111-8111-111111111111", "user", "7", r)
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
