package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	attachment "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/integration/v1"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Command actions are the owner's authoritative RPC methods; public callers
// cannot select an action or destination.
const InferenceCreateAction = inferencev1.InferenceServiceManager_CreateInferenceService_FullMethodName
const InferenceDeleteAction = inferencev1.InferenceServiceManager_DeleteInferenceService_FullMethodName

type InferenceOwnerClient interface {
	Create(context.Context, *inferencev1.CreateInferenceServiceRequest) (*inferencev1.OperationResponse, error)
	Delete(context.Context, *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error)
}

// InferenceModelClient is deliberately the narrow readiness surface used by
// create. The Model service remains the authority for tenant scoped model
// state; Governance only verifies that the selected version is ready.
type InferenceModelClient interface {
	GetModelVersion(context.Context, string, string, *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error)
}

type InferenceGpuAcceptance interface {
	AcceptGpuCreate(context.Context, string, *acc.GpuRequest, proto.Message) (*data.QuotaOccupyResult, error)
	AcceptGpuDelete(context.Context, string, string) (*attachment.GpuDeleteAcceptance, error)
	HasGpuCreate(context.Context, string) (bool, error)
}

type inferenceTrustedOwnerClient interface {
	CreateTrusted(context.Context, string, string, *inferencev1.CreateInferenceServiceRequest) (*inferencev1.OperationResponse, error)
	DeleteTrusted(context.Context, string, string, *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error)
}

type InferenceActionAuthorizer interface {
	Authorize(context.Context, uint32, uint32, string) error
}

type InferenceGpuBinding struct {
	client        InferenceOwnerClient
	authorization InferenceActionAuthorizer
	model         InferenceModelClient
}

func NewInferenceGpuBinding(client InferenceOwnerClient, authorization InferenceActionAuthorizer) *InferenceGpuBinding {
	return &InferenceGpuBinding{client: client, authorization: authorization}
}
func NewInferenceGpuBindingWithModel(client InferenceOwnerClient, authorization InferenceActionAuthorizer, model InferenceModelClient) *InferenceGpuBinding {
	return &InferenceGpuBinding{client: client, authorization: authorization, model: model}
}
func (*InferenceGpuBinding) OwnerService() string { return "ani-inference" }
func (*InferenceGpuBinding) CreateAction() string { return InferenceCreateAction }
func (*InferenceGpuBinding) DeleteAction() string { return InferenceDeleteAction }
func (*InferenceGpuBinding) Actions() []string {
	return []string{InferenceCreateAction, InferenceDeleteAction}
}
func (*InferenceGpuBinding) GpuQuotaCodes() []string {
	return []string{GpuPhysicalQuotaCode, GpuSharedQuotaCode}
}

func (b *InferenceGpuBinding) AuthorizeGpu(ctx context.Context, p *auth.Principal, action, resource string) error {
	if p == nil || p.Type != auth.SubjectUser || p.TenantID == 0 || p.ID == 0 || b == nil || b.authorization == nil {
		return kratoserrors.Forbidden("FORBIDDEN", "inference action forbidden")
	}
	path := data.InferenceCreatePath
	if action == InferenceDeleteAction {
		path = data.InferenceDeletePath
	} else if action != InferenceCreateAction {
		return kratoserrors.Forbidden("FORBIDDEN", "inference action forbidden")
	}
	err := b.authorization.Authorize(ctx, p.TenantID, p.ID, path)
	if errors.Is(err, data.ErrInferenceAuthorizationDenied) {
		return kratoserrors.Forbidden("FORBIDDEN", "inference action forbidden")
	}
	if errors.Is(err, data.ErrInferenceAuthorizationUnavailable) {
		return kratoserrors.ServiceUnavailable("INFERENCE_AUTHORIZATION_UNAVAILABLE", "inference authorization unavailable")
	}
	return err
}

// CanonicalGpuBusiness selects the shared public API canonicalization. Trusted
// attachments and immediate command IDs never enter the original business hash.
func (*InferenceGpuBinding) CanonicalGpuBusiness(message proto.Message) ([]byte, error) {
	request, ok := message.(*inferencev1.CreateInferenceServiceRequest)
	if !ok || request == nil || request.GetGpuOwnerAttachment() != nil || len(request.GetOriginalCharges()) != 0 || request.GetRequestId() != "" {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid inference user business")
	}
	return inferencev1.CanonicalBusinessPayload(request)
}

// Model readiness is a new-admission dependency. GpuAcceptance invokes this
// resolver only after current authorization and the durable replay gate.
func (b *InferenceGpuBinding) ResolveGpuBusiness(ctx context.Context, p *auth.Principal, tenant string, message proto.Message) (*acc.GpuRequest, proto.Message, error) {
	request, ok := message.(*inferencev1.CreateInferenceServiceRequest)
	if !ok || request == nil || request.GetResource().GetGpu() == nil || p == nil {
		return nil, nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid inference user business")
	}
	actor, err := p.Actor()
	if err != nil {
		return nil, nil, status.Error(codes.Unauthenticated, "login required")
	}
	if err := checkInferenceModelReady(ctx, b.model, tenant, actor, request.ModelVersionId); err != nil {
		return nil, nil, err
	}
	return inferenceGpuRequest(request.Resource.Gpu), request, nil
}

func (*InferenceGpuBinding) ValidateGpuBusiness(_ context.Context, message proto.Message) ([]data.QuotaOccupyItem, error) {
	r, ok := message.(*inferencev1.CreateInferenceServiceRequest)
	if !ok || r == nil || r.GetResource().GetGpu() == nil || r.GetGpuOwnerAttachment() != nil || len(r.GetOriginalCharges()) != 0 || r.GetRequestId() != "" {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid inference user business")
	}
	if err := inferencev1.ValidateBusinessPayload(r); err != nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid inference business or GPU topology")
	}
	// This slice adds the established GPU meter. Dispatch below preserves all
	// original mixed charges already present in a frozen business command.
	return nil, nil
}

func (b *InferenceGpuBinding) Dispatch(ctx context.Context, command *QuotaDispatchCommand) ([]byte, error) {
	if b == nil || b.client == nil || command == nil {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	createAttachment, deleteAttachment, err := BuildGpuOwnerAttachments("ani-inference", command)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "INVALID_ORIGINAL_GPU_COMMAND")
	}
	frozen, err := DecodeGpuCanonical(command.CanonicalRequest)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "INVALID_ORIGINAL_GPU_COMMAND")
	}
	charges := make([]*inferencev1.OriginalQuotaCharge, 0, len(command.Charges))
	for _, charge := range command.Charges {
		charges = append(charges, &inferencev1.OriginalQuotaCharge{ChargeId: charge.ChargeID, QuotaCode: charge.QuotaCode, OriginalUnits: charge.ChargedUnits})
	}
	var response *inferencev1.OperationResponse
	switch command.Action {
	case InferenceCreateAction:
		if createAttachment == nil || deleteAttachment != nil || command.CreateOperationID != "" {
			return nil, status.Error(codes.FailedPrecondition, "INVALID_INFERENCE_CREATE_COMMAND")
		}
		request, decodeErr := inferencev1.DecodeBusinessPayload(frozen.BusinessPayload)
		if decodeErr != nil {
			return nil, status.Error(codes.FailedPrecondition, "INVALID_INFERENCE_BUSINESS")
		}
		digest, digestErr := inferencev1.BusinessPayloadDigest(request)
		if digestErr != nil || digest != frozen.BusinessPayloadDigest {
			return nil, status.Error(codes.FailedPrecondition, "INVALID_INFERENCE_BUSINESS_DIGEST")
		}
		if _, validationErr := b.ValidateGpuBusiness(ctx, request); validationErr != nil {
			return nil, status.Error(codes.FailedPrecondition, "INVALID_INFERENCE_BUSINESS")
		}
		if !proto.Equal(inferenceGpuRequest(request.Resource.Gpu), frozen.GpuRequest) {
			return nil, status.Error(codes.FailedPrecondition, "INVALID_INFERENCE_GPU_REQUEST")
		}
		request.RequestId = command.OperationID
		request.GpuOwnerAttachment = createAttachment
		request.OriginalCharges = charges
		if trusted, ok := b.client.(inferenceTrustedOwnerClient); ok {
			response, err = trusted.CreateTrusted(ctx, command.ResourceTenantID, quotaActorString(command.Actor), request)
		} else {
			response, err = b.client.Create(ctx, request)
		}
	case InferenceDeleteAction:
		if deleteAttachment == nil || createAttachment != nil || command.CreateOperationID == "" {
			return nil, status.Error(codes.FailedPrecondition, "INVALID_INFERENCE_DELETE_COMMAND")
		}
		deleteRequest := &inferencev1.DeleteInferenceServiceRequest{RequestId: command.OperationID, ResourceId: command.ResourceID, GpuOwnerAttachment: deleteAttachment, OriginalCharges: charges}
		if trusted, ok := b.client.(inferenceTrustedOwnerClient); ok {
			response, err = trusted.DeleteTrusted(ctx, command.ResourceTenantID, quotaActorString(command.Actor), deleteRequest)
		} else {
			response, err = b.client.Delete(ctx, deleteRequest)
		}
	default:
		return nil, status.Error(codes.FailedPrecondition, "INVALID_INFERENCE_ACTION")
	}
	if err != nil {
		return nil, err
	}
	if response == nil || response.DurableOwnerAck == nil || len(response.DurableOwnerAck.ProtoReflect().GetUnknown()) != 0 || !response.DurableOwnerAck.Accepted || response.DurableOwnerAck.OperationId != command.OperationID || response.DurableOwnerAck.ResourceId != command.ResourceID {
		return nil, status.Error(codes.FailedPrecondition, "INVALID_DURABLE_OWNER_ACK")
	}
	ack := struct {
		OperationID string `json:"operation_id"`
		ResourceID  string `json:"resource_id"`
		Accepted    bool   `json:"accepted"`
	}{command.OperationID, command.ResourceID, true}
	raw, err := json.Marshal(ack)
	if err != nil {
		return nil, err
	}
	if err = ValidateDurableOwnerAck(command, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func inferenceGpuRequest(g *inferencev1.GpuRequest) *acc.GpuRequest {
	if g == nil {
		return nil
	}
	return &acc.GpuRequest{ClusterId: g.ClusterId, PoolId: g.PoolId, ProfileId: g.ProfileId, ProfileVersion: g.ProfileVersion, Replicas: g.Replicas, DevicesPerReplica: g.DevicesPerReplica, ContainerName: g.ContainerName}
}

func quotaActorString(actor QuotaActor) string {
	switch actor.Type {
	case "user":
		return "governance:user:" + actor.ID
	case "api_key":
		return "governance:access-key:" + actor.ID
	default:
		return ""
	}
}

type InferenceService struct {
	admin.UnimplementedInferenceServiceServer
	acceptance InferenceGpuAcceptance
	owner      InferenceOwnerClient
	tenants    ResourceTenantResolver
	model      InferenceModelClient
}

func NewInferenceService(a InferenceGpuAcceptance) *InferenceService {
	return &InferenceService{acceptance: a}
}

func NewInferenceServiceWithOwner(a InferenceGpuAcceptance, owner InferenceOwnerClient, tenants ResourceTenantResolver) *InferenceService {
	return &InferenceService{acceptance: a, owner: owner, tenants: tenants}
}

func NewInferenceServiceWithOwnerAndModel(a InferenceGpuAcceptance, owner InferenceOwnerClient, tenants ResourceTenantResolver, model InferenceModelClient) *InferenceService {
	return &InferenceService{acceptance: a, owner: owner, tenants: tenants, model: model}
}

func (s *InferenceService) CreateInference(ctx context.Context, in *view.CreateInferenceRequest) (*view.InferenceAcceptance, error) {
	if in == nil || in.Data == nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "inference data is required")
	}
	d := in.Data
	request, err := s.buildCreateRequest(d)
	if err != nil {
		return nil, err
	}
	if request.Resource.GetGpu() != nil {
		if s == nil || s.acceptance == nil {
			return nil, quotapb.ErrorQuotaAdapterUnavailable("%s", "inference unavailable")
		}
		result, err := s.acceptance.AcceptGpuCreate(ctx, d.IdempotencyKey, inferenceGpuRequest(request.Resource.Gpu), request)
		if err != nil {
			return nil, err
		}
		return &view.InferenceAcceptance{OperationId: result.OperationID, ResourceId: result.ResourceID, Replayed: result.Replayed, Result: "ACCEPTED", DispatchOperationId: result.OperationID}, nil
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkInferenceModelReady(ctx, s.model, tenant, actor, d.ModelVersionId); err != nil {
		return nil, err
	}
	return s.createDirect(ctx, d, request, tenant, actor)
}

func checkInferenceModelReady(ctx context.Context, model InferenceModelClient, tenant, actor, versionID string) error {
	if model == nil {
		return status.Error(codes.Unavailable, "MODEL_ADAPTER_UNAVAILABLE")
	}
	modelReply, err := model.GetModelVersion(ctx, tenant, actor, &modelv1.GetModelVersionRequest{ModelVersionId: versionID})
	if err != nil {
		code := status.Code(err)
		if code == codes.OK || code == codes.Unknown {
			code = codes.Unavailable
		}
		return status.Error(code, "model readiness check failed")
	}
	if modelReply == nil || modelReply.Version == nil || modelReply.Version.Id != versionID || !strings.EqualFold(strings.TrimSpace(modelReply.Version.Status), "ready") {
		return status.Error(codes.FailedPrecondition, "MODEL_NOT_READY")
	}
	return nil
}

func (s *InferenceService) buildCreateRequest(d *view.CreateInferenceData) (*inferencev1.CreateInferenceServiceRequest, error) {
	if d == nil {
		return nil, status.Error(codes.InvalidArgument, "inference data is required")
	}
	for field, value := range map[string]string{"idempotency_key": d.IdempotencyKey, "name": d.Name, "model_version_id": d.ModelVersionId, "served_model_name": d.ServedModelName, "engine_image": d.EngineImage, "engine_type": d.EngineType, "command": d.Command, "cpu": d.Cpu, "memory": d.Memory} {
		if strings.TrimSpace(value) == "" {
			return nil, status.Errorf(codes.InvalidArgument, "%s is required", field)
		}
	}
	if d.Replicas <= 0 {
		return nil, status.Error(codes.InvalidArgument, "replicas must be positive")
	}
	for field, value := range map[string]string{"cpu": d.Cpu, "memory": d.Memory} {
		quantity, err := resource.ParseQuantity(value)
		if err != nil || quantity.Sign() <= 0 {
			return nil, status.Errorf(codes.InvalidArgument, "%s must be a positive quantity", field)
		}
	}
	mode := d.RuntimeMode
	if mode == inferencev1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		mode = inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT
	}
	switch mode {
	case inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT:
		if d.WorkerReplicas != 0 {
			return nil, status.Error(codes.InvalidArgument, "worker_replicas is only valid for leader worker set")
		}
	case inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET:
		if d.WorkerReplicas <= 0 {
			return nil, status.Error(codes.InvalidArgument, "worker_replicas must be positive")
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "invalid runtime_mode")
	}
	var gpu *inferencev1.GpuRequest
	if d.Gpu != nil {
		if strings.TrimSpace(d.Gpu.ClusterId) == "" || strings.TrimSpace(d.Gpu.PoolId) == "" || strings.TrimSpace(d.Gpu.ProfileId) == "" || d.Gpu.ProfileVersion == 0 {
			return nil, status.Error(codes.InvalidArgument, "gpu selection must include cluster_id, pool_id, profile_id and profile_version")
		}
		gpuReplicas := int64(d.Replicas)
		container := "kserve-container"
		if mode == inferencev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET {
			gpuReplicas *= int64(d.WorkerReplicas) + 1
			container = "main"
		}
		if gpuReplicas <= 0 || gpuReplicas > int64(^uint32(0)) {
			return nil, status.Error(codes.InvalidArgument, "GPU replica count is out of range")
		}
		gpu = &inferencev1.GpuRequest{ClusterId: d.Gpu.ClusterId, PoolId: d.Gpu.PoolId, ProfileId: d.Gpu.ProfileId, ProfileVersion: d.Gpu.ProfileVersion, Replicas: uint32(gpuReplicas), DevicesPerReplica: 1, ContainerName: container}
	}
	return &inferencev1.CreateInferenceServiceRequest{
		Name: d.Name, ModelVersionId: d.ModelVersionId, ServedModelName: d.ServedModelName, Replicas: d.Replicas,
		Resource: &inferencev1.ResourceSpec{Requests: map[string]string{"cpu": d.Cpu, "memory": d.Memory}, Gpu: gpu},
		// Governance exposes the business-facing flat request. The inference
		// runtime still needs an explicit HTTP endpoint to mount the verified
		// model PVC and publish the KServe predictor. Keep this transport
		// contract separate from the caller-owned startup command.
		Runtime: &inferencev1.RuntimeSpec{
			Mode: mode, WorkerReplicas: d.WorkerReplicas, Provider: "kserve",
			Endpoint: &inferencev1.EndpointSpec{ContainerPort: 8000, ServicePort: 80, TargetPort: "8000", Protocol: "TCP"},
		},
		Engine: &inferencev1.EngineSpec{Type: d.EngineType, Image: d.EngineImage, Command: d.Command},
	}, nil
}

func (s *InferenceService) DeleteInference(ctx context.Context, in *view.DeleteInferenceRequest) (*view.InferenceAcceptance, error) {
	if in == nil || in.Data == nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "inference data is required")
	}
	managedGPU := false
	principal, _ := auth.PrincipalFromContext(ctx)
	// API Key commands retain the trusted CPU owner path. GPU deletion remains
	// subject to GpuAcceptance's tenant-user authorization below.
	if s != nil && s.acceptance != nil && (principal == nil || principal.Type != auth.SubjectAPIKey) {
		var err error
		managedGPU, err = s.acceptance.HasGpuCreate(ctx, in.Data.ResourceId)
		if err != nil {
			return nil, err
		}
	}
	if !managedGPU && s != nil && s.owner != nil && s.tenants != nil {
		if trusted, ok := s.owner.(interface {
			GetTrusted(context.Context, string, string, *inferencev1.GetInferenceServiceRequest) (*inferencev1.InferenceService, error)
		}); ok {
			tenant, actor, err := s.trustedInferenceOperator(ctx)
			if err != nil {
				return nil, err
			}
			current, getErr := trusted.GetTrusted(ctx, tenant, actor, &inferencev1.GetInferenceServiceRequest{ResourceId: in.Data.ResourceId})
			if getErr != nil {
				return nil, mapInferenceOwnerError(getErr)
			}
			if current == nil || current.Id != in.Data.ResourceId {
				return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
			}
			if current.GetResource().GetGpu() == nil {
				if in.Data.ExpectedGeneration != 0 && in.Data.ExpectedGeneration != current.Generation {
					return nil, status.Error(codes.FailedPrecondition, "inference generation changed")
				}
				return s.deleteDirect(ctx, in, tenant, actor, current.Generation)
			}
		}
	}
	if s == nil || s.acceptance == nil {
		return nil, quotapb.ErrorQuotaAdapterUnavailable("%s", "inference unavailable")
	}
	result, err := s.acceptance.AcceptGpuDelete(ctx, in.Data.IdempotencyKey, in.Data.ResourceId)
	if err != nil {
		return nil, err
	}
	return &view.InferenceAcceptance{OperationId: result.DeleteOperationId, ResourceId: result.ResourceId, Result: result.Result, DispatchOperationId: result.DispatchOperationId}, nil
}

// createDirect forwards an inference request without a GPU reservation. It
// preserves the caller-owned complete engine command byte-for-byte.
func (s *InferenceService) createDirect(ctx context.Context, data *view.CreateInferenceData, request *inferencev1.CreateInferenceServiceRequest, tenant, actor string) (*view.InferenceAcceptance, error) {
	if s == nil || s.owner == nil || s.tenants == nil {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	trusted, ok := s.owner.(interface {
		CreateTrusted(context.Context, string, string, *inferencev1.CreateInferenceServiceRequest) (*inferencev1.OperationResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	if strings.TrimSpace(data.IdempotencyKey) == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}
	request.RequestId = data.IdempotencyKey
	result, err := trusted.CreateTrusted(ctx, tenant, actor, request)
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	if result == nil || result.Resource == nil || result.Operation == nil || result.Resource.Id == "" || result.Operation.Id == "" {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	return &view.InferenceAcceptance{OperationId: result.Operation.Id, ResourceId: result.Resource.Id, Result: "ACCEPTED", DispatchOperationId: result.Operation.Id}, nil
}

func (s *InferenceService) deleteDirect(ctx context.Context, in *view.DeleteInferenceRequest, tenant, actor string, generation int64) (*view.InferenceAcceptance, error) {
	trusted, ok := s.owner.(interface {
		DeleteTrusted(context.Context, string, string, *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	if generation < 1 {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	requestID := in.Data.IdempotencyKey
	if strings.TrimSpace(requestID) == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}
	result, err := trusted.DeleteTrusted(ctx, tenant, actor, &inferencev1.DeleteInferenceServiceRequest{RequestId: requestID, ResourceId: in.Data.ResourceId, ExpectedGeneration: generation})
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	if result == nil || result.Operation == nil || result.Operation.Id == "" {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	return &view.InferenceAcceptance{OperationId: result.Operation.Id, ResourceId: in.Data.ResourceId, Result: "ACCEPTED", DispatchOperationId: result.Operation.Id}, nil
}

func (s *InferenceService) trustedInferenceOperator(ctx context.Context) (string, string, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return "", "", status.Error(codes.Unauthenticated, "login required")
	}
	if p.TenantID == 0 || s == nil || s.tenants == nil {
		return "", "", status.Error(codes.PermissionDenied, "tenant identity required")
	}
	tenant, err := s.tenants.ResourceTenantID(ctx, p.TenantID)
	if err != nil || strings.TrimSpace(tenant) == "" {
		return "", "", status.Error(codes.Unavailable, "tenant mapping unavailable")
	}
	actor, err := p.Actor()
	if err != nil {
		return "", "", status.Error(codes.Unauthenticated, "login required")
	}
	return tenant, actor, nil
}

func mapInferenceOwnerError(err error) error {
	switch status.Code(err) {
	case codes.InvalidArgument, codes.FailedPrecondition:
		return status.Error(codes.InvalidArgument, "invalid inference request")
	case codes.Unauthenticated:
		return status.Error(codes.Unauthenticated, "inference authentication failed")
	case codes.PermissionDenied:
		return status.Error(codes.PermissionDenied, "inference access denied")
	case codes.NotFound:
		return status.Error(codes.NotFound, "inference resource not found")
	case codes.AlreadyExists:
		return status.Error(codes.AlreadyExists, "inference request conflicts with an existing operation")
	case codes.Canceled:
		return status.Error(codes.Canceled, "inference request canceled")
	default:
		return status.Error(codes.Unavailable, "inference service unavailable")
	}
}

func mapInferenceView(source *inferencev1.InferenceService) *view.InferenceView {
	if source == nil {
		return nil
	}
	projected := &view.InferenceView{
		Id:                source.Id,
		Name:              source.Name,
		DesiredState:      source.DesiredState,
		Generation:        source.Generation,
		ModelVersionId:    source.ModelVersionId,
		ServedModelName:   source.ServedModelName,
		Phase:             source.RuntimePhase,
		Health:            source.InvocationHealth,
		ObservedAt:        source.ObservedAt,
		Replicas:          source.Replicas,
		PublicationPhase:  source.PublicationPhase,
		AppliedGeneration: source.AppliedGeneration,
	}
	if source.Resource != nil {
		projected.Cpu = source.Resource.Requests["cpu"]
		projected.Memory = source.Resource.Requests["memory"]
		if source.Resource.Gpu != nil {
			projected.Gpu = &view.GpuSelection{
				ClusterId:      source.Resource.Gpu.ClusterId,
				PoolId:         source.Resource.Gpu.PoolId,
				ProfileId:      source.Resource.Gpu.ProfileId,
				ProfileVersion: source.Resource.Gpu.ProfileVersion,
			}
		}
	}
	if source.Engine != nil {
		projected.EngineImage = source.Engine.Image
		projected.EngineType = source.Engine.Type
		projected.Command = source.Engine.Command
	}
	if source.Runtime != nil {
		projected.RuntimeMode = source.Runtime.Mode
		projected.WorkerReplicas = source.Runtime.WorkerReplicas
	}
	return projected
}

func (s *InferenceService) GetInference(ctx context.Context, in *view.GetInferenceRequest) (*view.InferenceView, error) {
	if in == nil || strings.TrimSpace(in.ResourceId) == "" {
		return nil, status.Error(codes.InvalidArgument, "resource_id is required")
	}
	trusted, ok := s.owner.(interface {
		GetTrusted(context.Context, string, string, *inferencev1.GetInferenceServiceRequest) (*inferencev1.InferenceService, error)
	})
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, err
	}
	result, err := trusted.GetTrusted(ctx, tenant, actor, &inferencev1.GetInferenceServiceRequest{ResourceId: in.ResourceId})
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	if result == nil || result.Id != in.ResourceId {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	return mapInferenceView(result), nil
}

func (s *InferenceService) ListInference(ctx context.Context, in *view.ListInferenceRequest) (*view.ListInferenceResponse, error) {
	trusted, ok := s.owner.(interface {
		ListTrusted(context.Context, string, string, *inferencev1.ListInferenceServicesRequest) (*inferencev1.ListInferenceServicesResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, err
	}
	if in == nil {
		in = &view.ListInferenceRequest{}
	}
	result, err := trusted.ListTrusted(ctx, tenant, actor, &inferencev1.ListInferenceServicesRequest{PageSize: in.PageSize, PageToken: in.PageToken})
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	if result == nil || len(result.Services) > 100 {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	services := make([]*view.InferenceView, 0, len(result.Services))
	for _, service := range result.Services {
		services = append(services, mapInferenceView(service))
	}
	return &view.ListInferenceResponse{Services: services, NextPageToken: result.NextPageToken}, nil
}

func (s *InferenceService) GetInferenceOperation(ctx context.Context, in *view.GetInferenceOperationRequest) (*inferencev1.Operation, error) {
	if in == nil || strings.TrimSpace(in.OperationId) == "" {
		return nil, status.Error(codes.InvalidArgument, "operation_id is required")
	}
	trusted, ok := s.owner.(interface {
		GetOperationTrusted(context.Context, string, string, *inferencev1.GetOperationRequest) (*inferencev1.Operation, error)
	})
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, err
	}
	result, err := trusted.GetOperationTrusted(ctx, tenant, actor, &inferencev1.GetOperationRequest{OperationId: in.OperationId})
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	if result == nil || result.Id != in.OperationId {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	return result, nil
}
