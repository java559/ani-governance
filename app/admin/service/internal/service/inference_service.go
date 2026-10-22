package service

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	contractv1 "github.com/liangzai006/ani-inference-service/api/inference/contract/v1"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	commandv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/command/v1"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	viewv1 "go-wind-admin/api/gen/go/inference/view/v1"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Command actions are the owner's authoritative RPC methods; public callers
// cannot select an action or destination.
const InferenceCreateAction = runtimev1.InferenceServiceManager_CreateInferenceService_FullMethodName
const InferenceDeleteAction = runtimev1.InferenceServiceManager_DeleteInferenceService_FullMethodName

type InferenceOwnerClient interface {
	Create(context.Context, *runtimev1.CreateInferenceServiceRequest) (*runtimev1.OperationResponse, error)
	Delete(context.Context, *runtimev1.DeleteInferenceServiceRequest) (*runtimev1.OperationResponse, error)
}

// InferenceModelClient is deliberately the narrow readiness surface used by
// create. The Model service remains the authority for tenant scoped model
// state; Governance only verifies that the selected version is ready.
type InferenceModelClient interface {
	GetModelVersion(context.Context, string, string, *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error)
}

type InferenceGpuAcceptance interface {
	AcceptGpuCreate(context.Context, string, *acc.GpuRequest, proto.Message) (*data.QuotaOccupyResult, error)
	AcceptGpuDelete(context.Context, string, string) (*commandv1.GpuDeleteAcceptance, error)
	HasGpuCreate(context.Context, string) (bool, error)
}

type inferenceTrustedOwnerClient interface {
	CreateTrusted(context.Context, string, string, *runtimev1.CreateInferenceServiceRequest) (*runtimev1.OperationResponse, error)
	DeleteTrusted(context.Context, string, string, *runtimev1.DeleteInferenceServiceRequest) (*runtimev1.OperationResponse, error)
}

type InferenceActionAuthorizer interface {
	Authorize(context.Context, uint32, uint32, string) error
}

type InferenceGpuBinding struct {
	client        InferenceOwnerClient
	authorization InferenceActionAuthorizer
	model         InferenceModelClient
	compute       InferenceComputeClient
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
	request, ok := message.(*runtimev1.CreateInferenceServiceRequest)
	if !ok || request == nil || request.GetRequestId() != "" {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid inference user business")
	}
	return contractv1.CanonicalBusinessPayload(request)
}

// Model readiness is a new-admission dependency. GpuAcceptance invokes this
// resolver only after current authorization and the durable replay gate.
func (b *InferenceGpuBinding) ResolveGpuBusiness(ctx context.Context, p *auth.Principal, tenant string, message proto.Message) (*acc.GpuRequest, proto.Message, error) {
	request, ok := message.(*runtimev1.CreateInferenceServiceRequest)
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
	r, ok := message.(*runtimev1.CreateInferenceServiceRequest)
	if !ok || r == nil || r.GetResource().GetGpu() == nil || r.GetRequestId() != "" {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid inference user business")
	}
	if err := contractv1.ValidateBusinessPayload(r); err != nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid inference business or GPU topology")
	}
	// This slice adds the established GPU meter. Dispatch below preserves all
	// original mixed charges already present in a frozen business command.
	return nil, nil
}

// FinalizeGpuBusiness binds the ACC resolved execution/display snapshot to the
// immutable Inference business payload. Governance keeps the original quota
// vector in its ledger; Inference receives only this frozen owner payload.
func (b *InferenceGpuBinding) FinalizeGpuBusiness(_ context.Context, tenant, actorType, actorID, operationID, resourceID string, message proto.Message, plan *acc.ResolvedGpuRequest) ([]byte, error) {
	request, ok := proto.Clone(message).(*runtimev1.CreateInferenceServiceRequest)
	if !ok || request == nil || plan == nil || plan.Execution == nil || plan.Selection == nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid resolved inference GPU plan")
	}
	for _, identity := range []string{tenant, operationID, resourceID} {
		if parsed, err := uuid.Parse(identity); err != nil || parsed == uuid.Nil || parsed.String() != identity {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid inference command identity")
		}
	}
	request.Context = &runtimev1.CommandContext{TenantId: tenant, ResourceId: resourceID, OperationId: operationID}
	request.ClusterId = request.GetResource().GetGpu().GetClusterId()
	request.GpuExecution = proto.Clone(plan.Execution).(*acc.GpuExecution)
	request.GpuDisplay = proto.Clone(plan.Selection).(*acc.GpuDisplay)
	digest, err := contractv1.ManagedCreateRequestHash(request, tenant, actorType, actorID)
	if err != nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid inference managed request")
	}
	request.Context.RequestDigest = digest
	if err := contractv1.ValidateBusinessPayload(request); err != nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", err.Error())
	}
	return contractv1.CanonicalBusinessPayload(request)
}

func (b *InferenceGpuBinding) Dispatch(ctx context.Context, command *QuotaDispatchCommand) ([]byte, error) {
	return b.dispatchManaged(ctx, command)
}

func validateInferenceDurableAck(command *QuotaDispatchCommand, reply *runtimev1.OperationResponse) error {
	if reply == nil || reply.DurableOwnerAck == nil || reply.DurableOwnerAck.OperationId != command.OperationID || reply.DurableOwnerAck.ResourceId != command.ResourceID || !reply.DurableOwnerAck.Accepted {
		return quotapb.ErrorInvalidQuotaRequest("%s", "invalid inference durable ACK")
	}
	return nil
}

func inferenceGpuRequest(g *runtimev1.GpuRequest) *acc.GpuRequest {
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
	if in == nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "inference request is required")
	}
	d := in
	// New creates must go through the compute-preset admission path. The
	// deprecated CPU/memory/GPU fields are rejected before any owner call.
	if err := validateInferenceCreateIntent(d); err != nil {
		return nil, err
	}
	if s == nil || s.acceptance == nil {
		return nil, quotapb.ErrorQuotaAdapterUnavailable("%s", "inference unavailable")
	}
	result, err := s.acceptance.AcceptGpuCreate(ctx, d.IdempotencyKey, nil, d)
	if err != nil {
		return nil, err
	}
	return &view.InferenceAcceptance{OperationId: result.OperationID, ResourceId: result.ResourceID, Replayed: result.Replayed, Result: "ACCEPTED", DispatchOperationId: result.OperationID}, nil
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

func (s *InferenceService) buildCreateRequest(d *view.CreateInferenceRequest) (*runtimev1.CreateInferenceServiceRequest, error) {
	if d == nil {
		return nil, status.Error(codes.InvalidArgument, "inference request is required")
	}
	if err := validateInferenceCreateIntent(d); err != nil {
		return nil, err
	}
	mode := runtimev1.RuntimeMode(d.RuntimeMode)
	if mode == runtimev1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		mode = runtimev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT
	}
	switch mode {
	case runtimev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT:
		if d.WorkerReplicas != 0 {
			return nil, status.Error(codes.InvalidArgument, "worker_replicas is only valid for leader worker set")
		}
	case runtimev1.RuntimeMode_RUNTIME_MODE_LEADER_WORKER_SET:
		if d.WorkerReplicas <= 0 {
			return nil, status.Error(codes.InvalidArgument, "worker_replicas must be positive")
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "invalid runtime_mode")
	}
	return &runtimev1.CreateInferenceServiceRequest{
		Name: d.Name, ModelVersionId: d.ModelVersionId, ServedModelName: d.ServedModelName, Replicas: d.Replicas,
		// Governance exposes the business-facing flat request. The inference
		// runtime still needs an explicit HTTP endpoint to mount the verified
		// model PVC and publish the KServe predictor. Keep this transport
		// contract separate from the caller-owned startup command.
		Runtime: &runtimev1.RuntimeSpec{
			Mode: mode, WorkerReplicas: d.WorkerReplicas, Provider: "kserve",
			Endpoint: &runtimev1.EndpointSpec{ContainerPort: 8000, ServicePort: 80, TargetPort: "8000", Protocol: "TCP"},
		},
		Engine: &runtimev1.EngineSpec{Type: d.EngineType, Image: d.EngineImage, Command: d.Command},
	}, nil
}

func (s *InferenceService) DeleteInference(ctx context.Context, in *view.DeleteInferenceRequest) (*view.InferenceAcceptance, error) {
	if in == nil {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "inference request is required")
	}
	managedGPU := false
	principal, _ := auth.PrincipalFromContext(ctx)
	// API Key commands retain the trusted CPU owner path. GPU deletion remains
	// subject to GpuAcceptance's tenant-user authorization below.
	if s != nil && s.acceptance != nil && (principal == nil || principal.Type != auth.SubjectAPIKey) {
		var err error
		managedGPU, err = s.acceptance.HasGpuCreate(ctx, in.ResourceId)
		if err != nil {
			return nil, err
		}
	}
	if !managedGPU && s != nil && s.owner != nil && s.tenants != nil {
		if trusted, ok := s.owner.(interface {
			GetTrusted(context.Context, string, string, *runtimev1.GetInferenceServiceRequest) (*runtimev1.InferenceService, error)
		}); ok {
			tenant, actor, err := s.trustedInferenceOperator(ctx)
			if err != nil {
				return nil, err
			}
			current, getErr := trusted.GetTrusted(ctx, tenant, actor, &runtimev1.GetInferenceServiceRequest{ResourceId: in.ResourceId})
			if getErr != nil {
				return nil, mapInferenceOwnerError(getErr)
			}
			if current == nil || current.Id != in.ResourceId {
				return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
			}
			if current.GetResource().GetGpu() == nil {
				if in.ExpectedGeneration != 0 && in.ExpectedGeneration != current.Generation {
					return nil, status.Error(codes.FailedPrecondition, "inference generation changed")
				}
				return s.deleteDirect(ctx, in, tenant, actor, current.Generation)
			}
		}
	}
	if s == nil || s.acceptance == nil {
		return nil, quotapb.ErrorQuotaAdapterUnavailable("%s", "inference unavailable")
	}
	result, err := s.acceptance.AcceptGpuDelete(ctx, in.IdempotencyKey, in.ResourceId)
	if err != nil {
		return nil, err
	}
	return &view.InferenceAcceptance{OperationId: result.DeleteOperationId, ResourceId: result.ResourceId, Result: result.Result, DispatchOperationId: result.DispatchOperationId}, nil
}

func (s *InferenceService) deleteDirect(ctx context.Context, in *view.DeleteInferenceRequest, tenant, actor string, generation int64) (*view.InferenceAcceptance, error) {
	trusted, ok := s.owner.(interface {
		DeleteTrusted(context.Context, string, string, *runtimev1.DeleteInferenceServiceRequest) (*runtimev1.OperationResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	if generation < 1 {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	requestID := in.IdempotencyKey
	if strings.TrimSpace(requestID) == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}
	result, err := trusted.DeleteTrusted(ctx, tenant, actor, &runtimev1.DeleteInferenceServiceRequest{RequestId: requestID, ResourceId: in.ResourceId, ExpectedGeneration: generation})
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	if result == nil || result.Operation == nil || result.Operation.Id == "" {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	return &view.InferenceAcceptance{OperationId: result.Operation.Id, ResourceId: in.ResourceId, Result: "ACCEPTED", DispatchOperationId: result.Operation.Id}, nil
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

func mapInferenceView(source *runtimev1.InferenceService) *view.InferenceView {
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
		projected.RuntimeMode = viewv1.RuntimeMode(source.Runtime.Mode)
		projected.WorkerReplicas = source.Runtime.WorkerReplicas
	}
	return projected
}

func (s *InferenceService) GetInference(ctx context.Context, in *view.GetInferenceRequest) (*view.InferenceView, error) {
	if in == nil || strings.TrimSpace(in.ResourceId) == "" {
		return nil, status.Error(codes.InvalidArgument, "resource_id is required")
	}
	var original *ent.QuotaOperation
	var frozen *commandcontract.InferenceCanonical
	if admission, ok := s.acceptance.(*GpuAcceptance); ok {
		var lookupErr error
		original, frozen, lookupErr = admission.inferenceOriginal(ctx, in.ResourceId)
		if lookupErr != nil && !ent.IsNotFound(lookupErr) {
			return nil, lookupErr
		}
		if original != nil && original.DispatchState != "ACKED" {
			return acceptedInferenceView(original, frozen), nil
		}
	}
	trusted, ok := s.owner.(interface {
		GetTrusted(context.Context, string, string, *runtimev1.GetInferenceServiceRequest) (*runtimev1.InferenceService, error)
	})
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, err
	}
	result, err := trusted.GetTrusted(ctx, tenant, actor, &runtimev1.GetInferenceServiceRequest{ResourceId: in.ResourceId})
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	if result == nil || result.Id != in.ResourceId {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	out := mapInferenceView(result)
	applyInferenceSource(out, frozen)
	return out, nil
}

func resolveInferencePage(page, pageSize *int32) (*int32, *int32, error) {
	p, size := int32(1), int32(20)
	if page != nil {
		p = *page
	}
	if pageSize != nil {
		size = *pageSize
	}
	if p < 1 || size < 1 || size > 100 {
		return nil, nil, status.Error(codes.InvalidArgument, "page must be >= 1 and pageSize within 1..100")
	}
	return &p, &size, nil
}

func (s *InferenceService) ListInference(ctx context.Context, in *view.ListInferenceRequest) (*view.ListInferenceResponse, error) {
	trusted, ok := s.owner.(interface {
		ListTrusted(context.Context, string, string, *runtimev1.ListInferenceServicesRequest) (*runtimev1.ListInferenceServicesResponse, error)
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
	page, pageSize, err := resolveInferencePage(in.Page, in.PageSize)
	if err != nil {
		return nil, err
	}
	result, err := trusted.ListTrusted(ctx, tenant, actor, &runtimev1.ListInferenceServicesRequest{Page: page, PageSize: pageSize, Status: in.Status, Capability: in.Capability})
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	if result == nil || len(result.Items) > 100 {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	services := make([]*view.InferenceView, 0, len(result.Items))
	for _, service := range result.Items {
		services = append(services, mapInferenceView(service))
	}
	return &view.ListInferenceResponse{Items: services, Total: result.Total}, nil
}

func (s *InferenceService) GetInferenceOperation(ctx context.Context, in *view.GetInferenceOperationRequest) (*viewv1.Operation, error) {
	if in == nil || strings.TrimSpace(in.OperationId) == "" {
		return nil, status.Error(codes.InvalidArgument, "operation_id is required")
	}
	var accepted *ent.QuotaOperation
	refund := ""
	if admission, ok := s.acceptance.(*GpuAcceptance); ok {
		var lookupErr error
		accepted, refund, lookupErr = admission.inferenceOperation(ctx, in.OperationId)
		if lookupErr != nil && !ent.IsNotFound(lookupErr) {
			return nil, lookupErr
		}
		if accepted != nil && accepted.DispatchState != "ACKED" {
			return acceptedInferenceOperation(accepted, refund), nil
		}
	}
	trusted, ok := s.owner.(interface {
		GetOperationTrusted(context.Context, string, string, *runtimev1.GetOperationRequest) (*runtimev1.Operation, error)
	})
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, err
	}
	result, err := trusted.GetOperationTrusted(ctx, tenant, actor, &runtimev1.GetOperationRequest{OperationId: in.OperationId})
	if err != nil {
		if accepted != nil && status.Code(err) == codes.NotFound {
			return acceptedInferenceOperation(accepted, refund), nil
		}
		return nil, mapInferenceOwnerError(err)
	}
	if result == nil || result.Id != in.OperationId {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	out := mapInferenceOperation(result)
	if accepted != nil {
		out.GovDispatchState = string(accepted.DispatchState)
		out.RefundState = refund
	}
	return out, nil
}

func mapInferenceOperation(source *runtimev1.Operation) *viewv1.Operation {
	if source == nil {
		return nil
	}
	return &viewv1.Operation{Id: source.Id, ServiceId: source.ServiceId, Kind: source.Kind, Phase: source.Phase, Step: source.Step, TargetGeneration: source.TargetGeneration, ErrorCode: source.ErrorCode, ErrorMessage: source.ErrorMessage, CreatedAt: source.CreatedAt, CompletedAt: source.CompletedAt}
}

type inferenceLifecycleOwner interface {
	UpdateTrusted(context.Context, string, string, *runtimev1.UpdateInferenceServiceRequest) (*runtimev1.OperationResponse, error)
	StartTrusted(context.Context, string, string, *runtimev1.ServiceCommandRequest) (*runtimev1.OperationResponse, error)
	StopTrusted(context.Context, string, string, *runtimev1.ServiceCommandRequest) (*runtimev1.OperationResponse, error)
	RestartTrusted(context.Context, string, string, *runtimev1.ServiceCommandRequest) (*runtimev1.OperationResponse, error)
	ListOperationsTrusted(context.Context, string, string, *runtimev1.ListOperationsRequest) (*runtimev1.ListOperationsResponse, error)
}

func (s *InferenceService) UpdateInference(ctx context.Context, in *view.UpdateInferenceRequest) (*view.InferenceAcceptance, error) {
	if in == nil {
		return nil, status.Error(codes.InvalidArgument, "inference request is required")
	}
	d := in
	if strings.TrimSpace(d.ResourceId) == "" || strings.TrimSpace(d.IdempotencyKey) == "" || d.ExpectedGeneration < 1 {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key, resource_id and positive expected_generation are required")
	}
	if d.Cpu != "" || d.Memory != "" || d.Replicas != 0 || d.Gpu != nil || d.RuntimeMode != 0 || d.WorkerReplicas != 0 || d.PresetId != "" {
		return nil, status.Error(codes.InvalidArgument, "inference resources and topology are frozen")
	}
	request := &runtimev1.UpdateInferenceServiceRequest{RequestId: d.IdempotencyKey, ResourceId: d.ResourceId, ExpectedGeneration: d.ExpectedGeneration, UpdateMask: d.UpdateMask, ModelVersionId: d.ModelVersionId, ServedModelName: d.ServedModelName}
	if d.EngineImage != "" || d.EngineType != "" || d.Command != "" {
		request.Engine = &runtimev1.EngineSpec{Image: d.EngineImage, Type: d.EngineType, Command: d.Command}
	}
	if err := contractv1.ValidateUpdatePatch(request); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	trusted, ok := s.owner.(inferenceLifecycleOwner)
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, err
	}
	result, err := trusted.UpdateTrusted(ctx, tenant, actor, request)
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	return inferenceAcceptance(result, d.ResourceId)
}

func (s *InferenceService) StartInference(ctx context.Context, in *view.InferenceServiceCommandRequest) (*view.InferenceAcceptance, error) {
	return s.commandInference(ctx, in, "start")
}

func (s *InferenceService) StopInference(ctx context.Context, in *view.InferenceServiceCommandRequest) (*view.InferenceAcceptance, error) {
	return s.commandInference(ctx, in, "stop")
}

func (s *InferenceService) RestartInference(ctx context.Context, in *view.InferenceServiceCommandRequest) (*view.InferenceAcceptance, error) {
	return s.commandInference(ctx, in, "restart")
}

func (s *InferenceService) commandInference(ctx context.Context, in *view.InferenceServiceCommandRequest, kind string) (*view.InferenceAcceptance, error) {
	if in == nil || strings.TrimSpace(in.ResourceId) == "" || strings.TrimSpace(in.IdempotencyKey) == "" || in.ExpectedGeneration < 1 {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key, resource_id and positive expected_generation are required")
	}
	trusted, ok := s.owner.(inferenceLifecycleOwner)
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, err
	}
	request := &runtimev1.ServiceCommandRequest{RequestId: in.IdempotencyKey, ResourceId: in.ResourceId, ExpectedGeneration: in.ExpectedGeneration}
	var result *runtimev1.OperationResponse
	switch kind {
	case "start":
		result, err = trusted.StartTrusted(ctx, tenant, actor, request)
	case "stop":
		result, err = trusted.StopTrusted(ctx, tenant, actor, request)
	case "restart":
		result, err = trusted.RestartTrusted(ctx, tenant, actor, request)
	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported inference command")
	}
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	return inferenceAcceptance(result, in.ResourceId)
}

func inferenceAcceptance(result *runtimev1.OperationResponse, resourceID string) (*view.InferenceAcceptance, error) {
	if result == nil || result.Operation == nil || strings.TrimSpace(result.Operation.Id) == "" {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	if resourceID == "" && result.Resource != nil {
		resourceID = result.Resource.Id
	}
	return &view.InferenceAcceptance{OperationId: result.Operation.Id, ResourceId: resourceID, Result: "ACCEPTED", DispatchOperationId: result.Operation.Id}, nil
}

func (s *InferenceService) ListInferenceOperations(ctx context.Context, in *view.ListInferenceOperationsRequest) (*view.ListInferenceOperationsResponse, error) {
	if in == nil || strings.TrimSpace(in.ResourceId) == "" {
		return nil, status.Error(codes.InvalidArgument, "resource_id is required")
	}
	trusted, ok := s.owner.(inferenceLifecycleOwner)
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, err
	}
	page, pageSize, err := resolveInferencePage(in.Page, in.PageSize)
	if err != nil {
		return nil, err
	}
	result, err := trusted.ListOperationsTrusted(ctx, tenant, actor, &runtimev1.ListOperationsRequest{ResourceId: in.ResourceId, Page: page, PageSize: pageSize})
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	if result == nil || len(result.Items) > 100 {
		return nil, status.Error(codes.Unavailable, "INVALID_INFERENCE_RESPONSE")
	}
	items := make([]*viewv1.Operation, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, mapInferenceOperation(item))
	}
	return &view.ListInferenceOperationsResponse{Items: items, Total: result.Total}, nil
}
