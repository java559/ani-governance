package service

import (
	"context"
	"encoding/json"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/stretchr/testify/require"
	attachment "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/integration/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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

type directInferenceTenantProbe struct{}

func (directInferenceTenantProbe) ResourceTenantID(context.Context, uint32) (string, error) {
	return "11111111-1111-4111-8111-111111111111", nil
}

func TestInferenceServiceCreatesWithoutGPUThroughTrustedOwner(t *testing.T) {
	owner := &directInferenceOwnerProbe{}
	model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Model: &modelv1.Model{TenantId: "11111111-1111-4111-8111-111111111111"}, Version: &modelv1.ModelVersion{Id: "version-1", Status: "ready"}}}
	s := NewInferenceServiceWithOwnerAndModel(nil, owner, directInferenceTenantProbe{}, model)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42})
	request := &view.CreateInferenceRequest{Data: &view.CreateInferenceData{
		IdempotencyKey: "direct-create-1", Name: "qwen-direct", ModelVersionId: "version-1", ServedModelName: "qwen-direct", Replicas: 1,
		EngineType: "vllm", EngineImage: "registry/vllm:latest", Command: "python -m vllm.entrypoints.openai.api_server --served-model-name qwen-direct", Cpu: "1", Memory: "2Gi",
	}}
	reply, err := s.CreateInference(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if reply.GetResourceId() != "svc-1" || reply.GetOperationId() != "op-1" || owner.createCalls != 1 {
		t.Fatalf("unexpected acceptance: %#v calls=%d", reply, owner.createCalls)
	}
	if owner.tenant != "11111111-1111-4111-8111-111111111111" || owner.actor != "governance:user:7" {
		t.Fatalf("unexpected trusted identity: tenant=%q actor=%q", owner.tenant, owner.actor)
	}
	if owner.request.GetEngine().GetCommand() != request.Data.Command || owner.request.GetEngine().GetImage() != request.Data.EngineImage || owner.request.GetEngine().GetType() != request.Data.EngineType {
		t.Fatalf("engine was changed: %#v", owner.request.GetEngine())
	}
	endpoint := owner.request.GetRuntime().GetEndpoint()
	require.Equal(t, int32(8000), endpoint.GetContainerPort())
	require.Equal(t, int32(80), endpoint.GetServicePort())
	require.Equal(t, "8000", endpoint.GetTargetPort())
	require.Equal(t, "TCP", endpoint.GetProtocol())
	if owner.request.GetResource().GetRequests()["cpu"] != "1" || owner.request.GetResource().GetRequests()["memory"] != "2Gi" || owner.request.GetResource().GetLimits() != nil {
		t.Fatalf("resources were changed: %#v", owner.request.GetResource())
	}
	if model.tenant != owner.tenant || model.actor != owner.actor || model.request.GetModelVersionId() != request.Data.ModelVersionId {
		t.Fatalf("model readiness was not checked with trusted identity: %#v", model)
	}
}

func TestInferenceServiceRejectsModelThatIsNotReady(t *testing.T) {
	owner := &directInferenceOwnerProbe{}
	model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Model: &modelv1.Model{TenantId: "11111111-1111-4111-8111-111111111111"}, Version: &modelv1.ModelVersion{Id: "version-1", Status: "pending"}}}
	s := NewInferenceServiceWithOwnerAndModel(nil, owner, directInferenceTenantProbe{}, model)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42})
	_, err := s.CreateInference(ctx, &view.CreateInferenceRequest{Data: &view.CreateInferenceData{IdempotencyKey: "direct-create-1", Name: "qwen-direct", ModelVersionId: "version-1", ServedModelName: "qwen-direct", EngineType: "vllm", EngineImage: "registry/vllm:latest", Command: "serve", Cpu: "1", Memory: "2Gi", Replicas: 1}})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Zero(t, owner.createCalls)
}

func TestInferenceServiceChecksModelBeforeDirectCreate(t *testing.T) {
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
			owner := &directInferenceOwnerProbe{}
			model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Version: tc.version}}
			s := NewInferenceServiceWithOwnerAndModel(nil, owner, directInferenceTenantProbe{}, model)
			ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42})
			reply, err := s.CreateInference(ctx, &view.CreateInferenceRequest{Data: &view.CreateInferenceData{
				IdempotencyKey: "20000000-0000-4000-8000-000000000001", Name: "qwen", ModelVersionId: "version-1", ServedModelName: "qwen", EngineType: "vllm", EngineImage: "image", Command: "serve", Cpu: "1", Memory: "2Gi", Replicas: 1,
			}})
			require.Equal(t, tc.wantCode, status.Code(err))
			require.Equal(t, tc.calls, owner.createCalls)
			if tc.calls == 1 {
				require.Equal(t, "svc-1", reply.GetResourceId())
				require.Equal(t, "version-1", owner.request.GetModelVersionId())
			}
		})
	}
}

func TestInferenceServiceDerivesGPUPlacementForDeploymentAndLWS(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      inferencev1.RuntimeMode
		workers   int32
		replicas  int32
		container string
		gpuPods   uint32
	}{
		{name: "deployment", mode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT, replicas: 3, container: "kserve-container", gpuPods: 3},
		{name: "lws", mode: inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET, workers: 2, replicas: 3, container: "main", gpuPods: 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &InferenceService{}
			request, err := s.buildCreateRequest(&view.CreateInferenceData{IdempotencyKey: "20000000-0000-4000-8000-000000000001", Name: "qwen", ModelVersionId: "version-1", ServedModelName: "qwen", EngineType: "vllm", EngineImage: "image", Command: "serve", Cpu: "1", Memory: "2Gi", Replicas: tc.replicas, RuntimeMode: tc.mode, WorkerReplicas: tc.workers, Gpu: &view.GpuSelection{ClusterId: "cluster", PoolId: "pool", ProfileId: "profile", ProfileVersion: 1}})
			require.NoError(t, err)
			require.Equal(t, tc.container, request.GetResource().GetGpu().GetContainerName())
			require.Equal(t, tc.gpuPods, request.GetResource().GetGpu().GetReplicas())
			require.Equal(t, uint32(1), request.GetResource().GetGpu().GetDevicesPerReplica())
			require.Equal(t, int32(8000), request.GetRuntime().GetEndpoint().GetContainerPort())
			require.Equal(t, int32(80), request.GetRuntime().GetEndpoint().GetServicePort())
			require.Equal(t, "8000", request.GetRuntime().GetEndpoint().GetTargetPort())
			require.Equal(t, "TCP", request.GetRuntime().GetEndpoint().GetProtocol())
		})
	}
}

func TestInferenceServiceRejectsInvalidFlatCreateFields(t *testing.T) {
	base := &view.CreateInferenceData{IdempotencyKey: "create", Name: "qwen", ModelVersionId: "version-1", ServedModelName: "qwen", EngineType: "vllm", EngineImage: "image", Command: "serve", Cpu: "1", Memory: "2Gi", Replicas: 1}
	for name, mutate := range map[string]func(*view.CreateInferenceData){
		"missing command":       func(d *view.CreateInferenceData) { d.Command = "" },
		"zero replicas":         func(d *view.CreateInferenceData) { d.Replicas = 0 },
		"bad cpu":               func(d *view.CreateInferenceData) { d.Cpu = "0" },
		"deployment workers":    func(d *view.CreateInferenceData) { d.WorkerReplicas = 1 },
		"partial gpu selection": func(d *view.CreateInferenceData) { d.Gpu = &view.GpuSelection{ClusterId: "cluster", PoolId: "pool"} },
		"zero lws workers": func(d *view.CreateInferenceData) {
			d.RuntimeMode = inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := proto.Clone(base).(*view.CreateInferenceData)
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
	ack    *attachment.DurableOwnerAck
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
	digest, err := GpuPlanDigest(plan)
	require.NoError(t, err)
	plan.ResolutionDigest = digest
	g := plan.Request
	r := &inferencev1.CreateInferenceServiceRequest{Name: "wire", ModelVersionId: "30000000-0000-4000-8000-000000000001", Replicas: int32(g.Replicas), Resource: &inferencev1.ResourceSpec{Requests: map[string]string{"cpu": "1"}, Limits: map[string]string{"memory": "8Gi"}, Gpu: &inferencev1.GpuRequest{ClusterId: g.ClusterId, PoolId: g.PoolId, ProfileId: g.ProfileId, ProfileVersion: g.ProfileVersion, Replicas: g.Replicas, DevicesPerReplica: g.DevicesPerReplica, ContainerName: g.ContainerName}}, Engine: &inferencev1.EngineSpec{Type: "vllm", Image: "test/image@sha256:fixture", Command: "server --fixture"}}
	business, err := inferencev1.CanonicalBusinessPayload(r)
	require.NoError(t, err)
	businessDigest, err := inferencev1.BusinessPayloadDigest(r)
	require.NoError(t, err)
	canonical := GpuCanonical{SchemaVersion: 2, GpuRequest: plan.Request, GpuPlan: plan, BusinessPayload: business, BusinessPayloadDigest: businessDigest, MeteringVersion: GpuMeteringVersion, QuotaItems: []GpuQuotaItem{{QuotaCode: GpuSharedQuotaCode, Units: 12288}, {QuotaCode: "storage.bytes", Units: 1024}}}
	raw, err := json.Marshal(canonical)
	require.NoError(t, err)
	command := &QuotaDispatchCommand{OperationID: "20000000-0000-4000-8000-000000000001", ResourceID: "20000000-0000-4000-8000-000000000002", ResourceTenantID: "20000000-0000-4000-8000-000000000003", Actor: QuotaActor{Type: "user", ID: "7"}, Action: InferenceCreateAction, RequestHash: "hash", CanonicalRequest: raw, Charges: []QuotaChargeRef{{ChargeID: "20000000-0000-4000-8000-000000000004", QuotaCode: GpuSharedQuotaCode, ChargedUnits: 12288}, {ChargeID: "20000000-0000-4000-8000-000000000005", QuotaCode: "storage.bytes", ChargedUnits: 1024}}}
	return r, command
}

func TestInferenceAdapterPreservesOriginalBusinessPlanAndAllCharges(t *testing.T) {
	r, command := inferenceBusiness(t)
	peer := &inferenceWirePeer{ack: &attachment.DurableOwnerAck{OperationId: command.OperationID, ResourceId: command.ResourceID, Accepted: true}}
	binding := NewInferenceGpuBinding(peer, nil)
	raw, err := binding.Dispatch(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, ValidateDurableOwnerAck(command, raw))
	require.Equal(t, command.OperationID, peer.create.RequestId)
	require.Equal(t, command.ResourceID, peer.create.GpuOwnerAttachment.Ref.ResourceId)
	require.Len(t, peer.create.GpuOwnerAttachment.GpuCharges, 1)
	require.Len(t, peer.create.OriginalCharges, 2)
	require.True(t, proto.Equal(r.Engine, peer.create.Engine))
	require.Equal(t, r.Resource.Requests, peer.create.Resource.Requests)
	command.CreateOperationID = command.OperationID
	command.OperationID = "20000000-0000-4000-8000-000000000006"
	command.Action = InferenceDeleteAction
	peer.ack = &attachment.DurableOwnerAck{OperationId: command.OperationID, ResourceId: command.ResourceID, Accepted: true}
	_, err = binding.Dispatch(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, command.CreateOperationID, peer.delete.GpuOwnerAttachment.Ref.CreateOperationId)
	require.Equal(t, command.OperationID, peer.delete.GpuOwnerAttachment.DeleteOperationId)
	require.Zero(t, peer.delete.ExpectedGeneration, "owner receives deletion intent without caller guessing local generation")
	require.Len(t, peer.delete.OriginalCharges, 2)
}

func TestInferenceAdapterRejectsWrongDurableAckAndIncompleteVector(t *testing.T) {
	_, command := inferenceBusiness(t)
	for _, ack := range []*attachment.DurableOwnerAck{nil, {OperationId: command.OperationID, ResourceId: command.ResourceID, Accepted: false}, {OperationId: command.OperationID, ResourceId: "other", Accepted: true}, {OperationId: "other", ResourceId: command.ResourceID, Accepted: true}} {
		_, err := NewInferenceGpuBinding(&inferenceWirePeer{ack: ack}, nil).Dispatch(context.Background(), command)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
	}
	peer := &inferenceWirePeer{}
	command.Charges = command.Charges[:1]
	_, err := NewInferenceGpuBinding(peer, nil).Dispatch(context.Background(), command)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Nil(t, peer.create)
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
	r.GpuOwnerAttachment = &attachment.GpuOwnerCreateAttachment{}
	_, err = binding.CanonicalGpuBusiness(r)
	require.Error(t, err)
	require.Error(t, binding.AuthorizeGpu(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 1}, InferenceCreateAction, ""))
}

func TestInferenceRequestHashMatchesSharedPublicContract(t *testing.T) {
	r, command := inferenceBusiness(t)
	business, err := inferencev1.CanonicalBusinessPayload(r)
	require.NoError(t, err)
	var businessObject map[string]any
	require.NoError(t, json.Unmarshal(business, &businessObject))
	gpuObject, err := gpuCanonicalMessage(inferenceGpuRequest(r.Resource.Gpu).ProtoReflect())
	require.NoError(t, err)
	govHash, err := gpuHash(map[string]any{"schema": "gov-gpu-create-v1", "tenant_id": command.ResourceTenantID, "actor_type": command.Actor.Type, "actor_id": command.Actor.ID, "owner": "ani-inference", "action": InferenceCreateAction, "gpu_request": gpuObject, "business_type": string(r.ProtoReflect().Descriptor().FullName()), "business": businessObject})
	require.NoError(t, err)
	ownerHash, err := inferencev1.ManagedCreateRequestHash(r, command.ResourceTenantID, command.Actor.Type, command.Actor.ID)
	require.NoError(t, err)
	require.Equal(t, govHash, ownerHash)
	govHash, err = gpuHash(map[string]any{"schema": "gov-gpu-delete-v1", "tenant_id": command.ResourceTenantID, "actor_type": command.Actor.Type, "actor_id": command.Actor.ID, "owner": "ani-inference", "action": InferenceDeleteAction, "resource_id": command.ResourceID, "create_operation_id": command.OperationID})
	require.NoError(t, err)
	ownerHash, err = inferencev1.ManagedDeleteRequestHash(command.ResourceTenantID, command.ResourceID, command.OperationID, command.Actor.Type, command.Actor.ID)
	require.NoError(t, err)
	require.Equal(t, govHash, ownerHash)
}

func TestInferenceMissingAuthoritativeModuleCannotEnableFromConfig(t *testing.T) {
	err := data.NewInferenceAuthorizationRepo(nil).Authorize(context.Background(), 1, 7, data.InferenceCreatePath)
	if !data.InferenceModuleRegistered() {
		require.ErrorIs(t, err, data.ErrInferenceAuthorizationDenied)
	} else {
		require.ErrorIs(t, err, data.ErrInferenceAuthorizationUnavailable)
	}
}
