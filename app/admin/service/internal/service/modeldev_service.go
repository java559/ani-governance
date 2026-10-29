package service

import (
	"context"
	stderrors "errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/modeldevbudget"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ModelDevCreateInput preserves the parsed user intent, including the
// distinction between omitted parameter fields and explicit default values.
// Identity is obtained separately from the trusted request context.
type ModelDevCreateInput struct {
	Intent         cpup01.Intent
	IdempotencyKey string
}

// ModelDevService owns the current authorization, original-key lookup and
// durable Governance acceptance sequence. Resolving a candidate does not
// deliver an execution command or start computation.
type ModelDevComputeResolver interface {
	ResolveComputePreset(context.Context, string, string, *compute.ResolveComputePresetRequest) (*compute.ComputeSelection, error)
}

type ModelDevService struct {
	authorization *data.ModelDevAuthorizationRepo
	tenants       ResourceTenantResolver
	acceptances   *data.ModelDevAcceptanceRepo
	compute       ModelDevComputeResolver
	resolver      *data.ModelDevClient
	gpu           ModelDevGPUAdmission
	monitoring    ModelDevMonitoringClient
	metricGate    *modelDevMetricGate
}

type ModelDevGPUAdmission struct {
	Plans       GpuPlanResolver
	Eligibility GpuUseEligibility
}

func NewModelDevService(
	authorization *data.ModelDevAuthorizationRepo,
	tenants ResourceTenantResolver,
	acceptances *data.ModelDevAcceptanceRepo,
	resolver *data.ModelDevClient,
	computeResolver ModelDevComputeResolver,
	gpu ...ModelDevGPUAdmission,
) *ModelDevService {
	s := &ModelDevService{
		authorization: authorization,
		tenants:       tenants,
		acceptances:   acceptances,
		resolver:      resolver,
		compute:       computeResolver,
		metricGate:    &modelDevMetricGate{},
	}
	if len(gpu) == 1 {
		s.gpu = gpu[0]
	}
	return s
}

// SetMonitoring attaches the observability client after construction. The
// resource curve is an optional dependency: an absent client only disables the
// metrics route and never blocks admission, query or dispatch.
func (s *ModelDevService) SetMonitoring(monitoring ModelDevMonitoringClient) {
	if s != nil {
		s.monitoring = monitoring
	}
}

// CreateExecution is called by the strict JSON HTTP adapter. It intentionally
// does not accept a protobuf request: the strict shared parser retains field
// presence and rejects null, duplicate and unknown input before resolution.
func (s *ModelDevService) CreateExecution(ctx context.Context, in ModelDevCreateInput) (reply *modeldevv1.CreateExecutionResponse, result error) {
	defer func() {
		if err := ctx.Err(); err != nil {
			reply, result = nil, modelDevCreateFailure(err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, modelDevCreateFailure(err)
	}
	principal, err := auth.PrincipalFromContext(ctx)
	if err != nil || principal == nil || principal.ID == 0 {
		return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
	}
	if principal.Type != auth.SubjectUser || principal.TenantID == 0 {
		return nil, errors.Forbidden("FORBIDDEN", "tenant user required")
	}
	if s == nil || s.authorization == nil || s.tenants == nil || s.acceptances == nil {
		return nil, modelDevCreateUnavailable()
	}
	preflight, cancelPreflight := context.WithTimeout(ctx, s.resolver.OperationTimeout())
	defer cancelPreflight()
	// A still-valid JWT or historical acceptance never substitutes for the
	// current database grant, including on original-key replay.
	grant, err := authorizeGpuBusiness(preflight, principal, compute.Usage_TRAINING, data.ModelDevCreateAction, func() error {
		return s.authorization.AuthorizeCreate(preflight, principal.TenantID, principal.ID)
	})
	if err != nil {
		return nil, modelDevCreateFailure(err)
	}
	tenant, err := s.tenants.ResourceTenantID(preflight, principal.TenantID)
	if err != nil {
		return nil, modelDevCreateFailure(err)
	}
	tenantID, err := uuid.Parse(tenant)
	if err != nil || tenantID == uuid.Nil || tenantID.String() != tenant {
		return nil, modelDevCreateUnavailable()
	}
	actor, err := principal.Actor()
	if err != nil {
		return nil, errors.Forbidden("FORBIDDEN", "tenant user required")
	}
	if in.IdempotencyKey == "" || !utf8.ValidString(in.IdempotencyKey) {
		return nil, modelDevCreateInvalid()
	}
	for _, character := range in.IdempotencyKey {
		if unicode.IsControl(character) {
			return nil, modelDevCreateInvalid()
		}
	}
	canonical, _, err := cpup01.CanonicalIntent(in.Intent)
	if err != nil {
		return nil, modelDevCreateInvalid()
	}
	normalized, err := cpup01.DecodeCanonicalIntent(canonical)
	if err != nil {
		return nil, modelDevCreateUnavailable()
	}
	if normalized.Kind == "FINETUNING" {
		var err error
		if normalized.NewModelName == nil && normalized.TargetModelID == nil {
			err = s.authorization.AuthorizeFinetuningArtifacts(preflight, principal.TenantID, principal.ID)
		} else {
			err = s.authorization.AuthorizeFinetuning(preflight, principal.TenantID, principal.ID, normalized.NewModelName != nil)
		}
		if err != nil {
			return nil, modelDevCreateFailure(err)
		}
	}
	scope := data.ModelDevAdmissionScope{
		TenantID: principal.TenantID, ResourceTenantID: tenant, Actor: actor,
		Action: data.ModelDevCreateAction, IdempotencyKey: in.IdempotencyKey,
	}
	// Replay needs only current authorization and original persisted intent.
	// It never observes current compute or implementation defaults.
	accepted, err := s.acceptances.FindAccepted(preflight, scope, normalized)
	if err == nil {
		if err := preflight.Err(); err != nil {
			return nil, modelDevCreateFailure(err)
		}
		return modelDevCreateReply(accepted, true)
	}
	if !stderrors.Is(err, data.ErrModelDevAcceptanceNotFound) {
		return nil, modelDevCreateFailure(err)
	}
	if s.resolver == nil || s.compute == nil {
		return nil, modelDevCreateUnavailable()
	}
	operationID, err := uuid.NewRandom()
	if err != nil {
		return nil, modelDevCreateUnavailable()
	}
	executionID, err := uuid.NewRandom()
	if err != nil {
		return nil, modelDevCreateUnavailable()
	}
	candidate := data.ModelDevFrozenCandidate{Intent: normalized, OperationID: operationID.String(), ExecutionID: executionID.String(), AcceptedAt: time.Now().UTC().Truncate(time.Microsecond)}
	selection, err := s.compute.ResolveComputePreset(preflight, tenant, actor, &compute.ResolveComputePresetRequest{TenantId: tenant, PresetId: normalized.ComputePresetID, Usage: compute.Usage_TRAINING})
	if err != nil {
		return nil, modelDevCreateResolutionFailure(err)
	}
	if compute.ValidateSelection(selection) != nil || selection.PresetId != normalized.ComputePresetID {
		return nil, modelDevCreateUnavailable()
	}
	if selection.Gpu != nil {
		if s.gpu.Plans == nil || s.gpu.Eligibility == nil {
			return nil, errors.ServiceUnavailable("MODELDEV_GPU_NOT_READY", "managed GPU admission is not yet available")
		}
		if err := authorizeNewGpuUse(preflight, grant, s.gpu.Eligibility); err != nil {
			return nil, err
		}
	}
	if err := preflight.Err(); err != nil {
		return nil, modelDevCreateFailure(err)
	}
	if err := modeldevbudget.FinishPreflight(ctx); err != nil {
		return nil, modelDevCreateFailure(err)
	}
	cancelPreflight()
	_, intentHash, err := cpup01.CanonicalIntent(normalized)
	if err != nil {
		return nil, modelDevCreateInvalid()
	}
	var resolveGPU func(context.Context, *cpup01.GPURequest) (*acc.ResolvedGpuRequest, error)
	if selection.Gpu != nil {
		resolveGPU = func(call context.Context, frozen *cpup01.GPURequest) (*acc.ResolvedGpuRequest, error) {
			return s.gpu.Plans.ResolveGpuRequest(call, &acc.ResolveGpuRequestRequest{
				Context: &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant, Actor: &acc.Actor{Type: string(principal.Type), Id: strconv.FormatUint(uint64(principal.ID), 10)}},
				Gpu:     &acc.GpuRequest{ClusterId: frozen.ClusterID, PoolId: frozen.PoolID, ProfileId: frozen.ProfileID, ProfileVersion: frozen.ProfileVersion, Replicas: frozen.Replicas, DevicesPerReplica: frozen.DevicesPerReplica, ContainerName: frozen.ContainerName},
			})
		}
	}
	resolved, err := s.resolver.ResolvePreparedWithGPU(ctx, data.ModelDevResolveScope{ResourceTenantID: tenant, Actor: actor}, normalized,
		cpup01.IdentityRef{TenantID: tenant, Actor: actor, OperationID: candidate.OperationID, ExecutionID: candidate.ExecutionID, IntentHash: intentHash}, selection, candidate.AcceptedAt, resolveGPU)
	if err != nil {
		return nil, modelDevCreateResolutionFailure(err)
	}
	candidate.Snapshot = resolved.Snapshot
	candidate.ResolvedGPU = resolved.ResolvedGPU
	executionCtx, cancelExecution := context.WithDeadline(ctx, candidate.Snapshot.Timing.ExecutionDeadlineAt)
	defer cancelExecution()
	acceptCtx, cancelAccept := context.WithTimeout(executionCtx, s.resolver.OperationTimeout())
	defer cancelAccept()
	accepted, replayed, err := s.acceptances.AcceptFrozen(acceptCtx, scope, candidate)
	if acceptCtx.Err() != nil {
		return nil, modelDevCreateFailure(acceptCtx.Err())
	}
	if err != nil {
		return nil, modelDevCreateFailure(err)
	}
	return modelDevCreateReply(accepted, replayed)
}

func modelDevCreateReply(accepted *data.ModelDevAcceptance, replayed bool) (*modeldevv1.CreateExecutionResponse, error) {
	if accepted == nil {
		return nil, modelDevCreateUnavailable()
	}
	reply := &modeldevv1.CreateExecutionResponse{
		OperationId: accepted.OperationID, ExecutionId: accepted.ExecutionID,
		Replayed: replayed,
	}
	switch accepted.DispatchState {
	case "QUEUED", "DISPATCHING", "UNKNOWN":
		if accepted.OwnerReceipt != nil {
			return nil, modelDevCreateUnavailable()
		}
		// Until a durable owner ACK exists, Governance knows only that it
		// accepted the original. Transport progress is not execution progress.
		reply.ComputeState, reply.DeliveryState = "ACCEPTED", "PENDING"
		reply.CloseState, reply.ResourceState = "OPEN", "NOT_APPLICABLE"
	case "ACKED":
		if accepted.OwnerReceipt == nil || accepted.OwnerReceipt.Validate(accepted.Envelope()) != nil {
			return nil, modelDevCreateUnavailable()
		}
		// These four states and their revision came from the same persisted
		// receipt as this admission. No separate owner query is composed here.
		reply.ComputeState = accepted.OwnerReceipt.ComputeState
		reply.DeliveryState = accepted.OwnerReceipt.DeliveryState
		reply.CloseState = accepted.OwnerReceipt.CloseState
		reply.ResourceState = accepted.OwnerReceipt.ResourceState
	default:
		return nil, modelDevCreateUnavailable()
	}
	return reply, nil
}

func modelDevCreateInvalid() error {
	return errors.BadRequest("INVALID_MODELDEV_CREATE", "invalid modeldev create request")
}

func modelDevCreateUnavailable() error {
	return errors.ServiceUnavailable("MODELDEV_CREATE_UNAVAILABLE", "modeldev create unavailable")
}

func modelDevCreateFailure(err error) error {
	switch {
	case stderrors.Is(err, context.Canceled):
		return errors.New(499, "MODELDEV_CREATE_CANCELED", "modeldev create canceled").WithCause(context.Canceled)
	case stderrors.Is(err, context.DeadlineExceeded):
		return errors.New(504, "MODELDEV_CREATE_TIMEOUT", "modeldev create timed out").WithCause(context.DeadlineExceeded)
	case stderrors.Is(err, data.ErrModelDevAuthorizationDenied):
		return errors.Forbidden("FORBIDDEN", "modeldev create forbidden")
	case stderrors.Is(err, data.ErrModelDevIdempotencyConflict):
		return errors.Conflict("IDEMPOTENCY_CONFLICT", "idempotency key has a different intent")
	case stderrors.Is(err, data.ErrModelDevAdmissionExpired):
		return errors.New(412, "EXECUTION_DEADLINE_EXCEEDED", "candidate execution deadline has passed")
	case quotapb.IsQuotaExceeded(err):
		return quotapb.ErrorQuotaExceeded("%s", "GPU quota exceeded")
	case quotapb.IsQuotaNotConfigured(err):
		return quotapb.ErrorQuotaNotConfigured("%s", "GPU quota is not configured")
	case quotapb.IsQuotaAdmissionDenied(err):
		return quotapb.ErrorQuotaAdmissionDenied("%s", "GPU quota admission denied")

	default:
		return modelDevCreateUnavailable()
	}
}

func modelDevCreateResolutionFailure(err error) error {
	switch status.Code(err) {
	case codes.Canceled:
		return modelDevCreateFailure(context.Canceled)
	case codes.DeadlineExceeded:
		return modelDevCreateFailure(context.DeadlineExceeded)
	}
	// The typed client has already validated and sanitized provider details.
	// Only fixed code/reason pairs cross the public HTTP boundary; neither its
	// message nor metadata, correlation ID or other details are copied.
	failure, ok := status.FromError(err)
	if !ok || len(failure.Details()) != 1 {
		return modelDevCreateUnavailable()
	}
	detail, ok := failure.Details()[0].(*modeldevcontractv1.ErrorDetail)
	if !ok || detail == nil {
		return modelDevCreateUnavailable()
	}
	switch {
	case failure.Code() == codes.InvalidArgument && detail.Reason == modeldevcontractv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT:
		return modelDevCreateInvalid()
	case failure.Code() == codes.NotFound && detail.Reason == modeldevcontractv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND:
		return errors.NotFound("RESOURCE_NOT_FOUND", "source version unavailable")
	case failure.Code() == codes.PermissionDenied && detail.Reason == modeldevcontractv1.ErrorReason_ERROR_REASON_FORBIDDEN:
		return errors.Forbidden("FORBIDDEN", "source access denied")
	case failure.Code() == codes.AlreadyExists && detail.Reason == modeldevcontractv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT:
		return errors.Conflict("COMMAND_CONFLICT", "immutable operation conflict")
	case failure.Code() == codes.FailedPrecondition && modelDevResolutionBusinessReason(detail.Reason):
		return modelDevPublicResolutionError(422, detail)
	case failure.Code() == codes.ResourceExhausted && detail.Reason == modeldevcontractv1.ErrorReason_ERROR_REASON_INPUT_VALIDATION_BUSY:
		return modelDevPublicResolutionError(429, detail)
	case failure.Code() == codes.Unavailable && detail.Reason == modeldevcontractv1.ErrorReason_ERROR_REASON_FINETUNING_RUNTIME_UNAVAILABLE:
		return modelDevPublicResolutionError(503, detail)
	case failure.Code() == codes.FailedPrecondition && detail.Reason == modeldevcontractv1.ErrorReason_ERROR_REASON_INPUT_NOT_READY:
		return errors.New(412, "INPUT_NOT_READY", "input version is not ready")
	case failure.Code() == codes.Unavailable && detail.Reason == modeldevcontractv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY:
		return modelDevPublicResolutionError(503, detail)
	case failure.Code() == codes.Unavailable && detail.Reason == modeldevcontractv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE:
		return modelDevPublicResolutionError(503, detail)
	default:
		return modelDevCreateUnavailable()
	}
}

func modelDevPublicResolutionError(code int, detail *modeldevcontractv1.ErrorDetail) error {
	reason := strings.TrimPrefix(detail.Reason.String(), "ERROR_REASON_")
	metadata := map[string]string{"upstream_request_id": detail.CorrelationId}
	if detail.Kind != "" {
		metadata["kind"] = detail.Kind
	}
	if detail.LimitName != "" {
		metadata["limit_name"] = detail.LimitName
	}
	if detail.Actual != nil {
		metadata["actual"] = strconv.FormatInt(*detail.Actual, 10)
	}
	if detail.Limit != nil {
		metadata["limit"] = strconv.FormatInt(*detail.Limit, 10)
	}
	return errors.New(code, reason, reason).WithMetadata(metadata)
}

func modelDevResolutionBusinessReason(reason modeldevcontractv1.ErrorReason) bool {
	switch reason {
	case modeldevcontractv1.ErrorReason_ERROR_REASON_BASE_MODEL_INVALID, modeldevcontractv1.ErrorReason_ERROR_REASON_BASE_MODEL_UNSUPPORTED,
		modeldevcontractv1.ErrorReason_ERROR_REASON_CONTEXT_LIMIT_EXCEEDED, modeldevcontractv1.ErrorReason_ERROR_REASON_CONTEXT_LIMIT_UNKNOWN,
		modeldevcontractv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_REQUIRED, modeldevcontractv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_UNSUPPORTED,
		modeldevcontractv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED, modeldevcontractv1.ErrorReason_ERROR_REASON_STRUCTURE_LIMIT_EXCEEDED,
		modeldevcontractv1.ErrorReason_ERROR_REASON_DATASET_RECORD_TOO_LARGE, modeldevcontractv1.ErrorReason_ERROR_REASON_DATASET_INVALID:
		return true
	default:
		return false
	}
}
