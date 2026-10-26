package service

import (
	"context"
	stderrors "errors"

	"github.com/go-kratos/kratos/v2/errors"
	ownerpb "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingpb "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Resume reauthorizes the current operator and reads the original frozen
// source. It neither reaccepts an execution nor manufactures a registration key.
func (s *ModelDevService) ResumeModelRegistration(ctx context.Context, in *modeldevv1.ResumeModelRegistrationRequest) (reply *modeldevv1.GetExecutionResponse, result error) {
	defer func() {
		if err := ctx.Err(); err != nil {
			reply, result = nil, modelDevRegistrationFailure(err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, modelDevRegistrationFailure(err)
	}
	principal, err := auth.PrincipalFromContext(ctx)
	if err != nil || principal == nil || principal.ID == 0 {
		return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
	}
	if principal.Type != auth.SubjectUser || principal.TenantID == 0 {
		return nil, errors.Forbidden("FORBIDDEN", "tenant user required")
	}
	if s == nil || s.authorization == nil || s.tenants == nil || s.acceptances == nil || s.resolver == nil {
		return nil, modelDevRegistrationFailure(nil)
	}
	if err := s.authorization.AuthorizeResume(ctx, principal.TenantID, principal.ID); err != nil {
		return nil, modelDevRegistrationFailure(err)
	}
	if in == nil || !modelDevQueryUUID(in.ExecutionId) || len(in.ProtoReflect().GetUnknown()) != 0 {
		return nil, errors.BadRequest("INVALID_MODELDEV_REGISTRATION", "invalid original execution")
	}
	tenant, err := s.tenants.ResourceTenantID(ctx, principal.TenantID)
	if err != nil || !modelDevQueryUUID(tenant) {
		return nil, modelDevRegistrationFailure(err)
	}
	original, err := s.acceptances.FindRegistrationSource(ctx, principal.TenantID, tenant, in.ExecutionId)
	if err != nil {
		return nil, modelDevRegistrationFailure(err)
	}
	if original == nil || original.Scope.TenantID != principal.TenantID || original.Scope.ResourceTenantID != tenant || original.ExecutionID != in.ExecutionId {
		return nil, modelDevRegistrationFailure(nil)
	}
	if original.Intent.Kind != "FINETUNING" || original.Snapshot.Finetuning == nil ||
		original.Intent.NewModelName == nil && original.Intent.TargetModelID == nil {
		return nil, errors.New(412, "MODEL_REGISTRATION_UNAVAILABLE", "model registration unavailable")
	}
	if err := s.authorization.AuthorizeFinetuning(ctx, principal.TenantID, principal.ID, original.Intent.NewModelName != nil); err != nil {
		return nil, modelDevRegistrationFailure(err)
	}
	actor, err := principal.Actor()
	if err != nil {
		return nil, errors.Forbidden("FORBIDDEN", "tenant user required")
	}
	out, err := s.resolver.ResumeModelRegistration(ctx, data.ModelDevResolveScope{ResourceTenantID: tenant, Actor: actor}, original.ExecutionID)
	if err != nil {
		return nil, modelDevRegistrationFailure(err)
	}
	if !modelDevRegistrationMatchesSource(original, out.Execution) {
		return nil, modelDevRegistrationFailure(nil)
	}
	return &modeldevv1.GetExecutionResponse{Execution: modelDevPublicExecution(out.Execution)}, nil
}

func modelDevRegistrationMatchesSource(original *data.ModelDevAcceptance, view *ownerpb.ExecutionView) bool {
	if view == nil || view.Identity == nil || view.ModelRegistration == nil || view.Kind != trainingpb.ExecutionKind_EXECUTION_KIND_FINETUNING || view.Identity.OperationId != original.OperationID || view.Identity.ExecutionId != original.ExecutionID || view.Identity.ExecutionSpecHash != original.ExecutionSpecHash || view.ImplementationId != original.Snapshot.Implementation.ImplementationID || view.ComputePresetId != original.Snapshot.ComputeOrigin.ComputePresetID {
		return false
	}
	frozen, err := contractpb.EncodeSnapshot(original.Snapshot)
	if err != nil || !proto.Equal(view.ComputeOrigin, frozen.ComputeOrigin) || !proto.Equal(view.Timing, frozen.Timing) || !proto.Equal(view.AcceptedAt, frozen.Timing.AcceptedAt) || !proto.Equal(view.DeadlineAt, frozen.Timing.ExecutionDeadlineAt) {
		return false
	}
	target := original.Intent.TargetModelID
	return view.ModelRegistration.State != "READY" || target == nil || view.ModelRegistration.ModelId == *target
}

func modelDevRegistrationFailure(err error) error {
	switch {
	case stderrors.Is(err, data.ErrModelDevAuthorizationDenied):
		return errors.Forbidden("FORBIDDEN", "model registration forbidden")
	case stderrors.Is(err, data.ErrModelDevAcceptanceNotFound):
		return errors.NotFound("RESOURCE_NOT_FOUND", "execution unavailable")
	case stderrors.Is(err, context.Canceled), status.Code(err) == codes.Canceled:
		return errors.New(499, "MODELDEV_REGISTRATION_CANCELED", "model registration canceled")
	case stderrors.Is(err, context.DeadlineExceeded), status.Code(err) == codes.DeadlineExceeded:
		return errors.New(504, "MODELDEV_REGISTRATION_TIMEOUT", "model registration timed out")
	}
	switch status.Code(err) {
	case codes.NotFound, codes.PermissionDenied, codes.Unauthenticated:
		return errors.NotFound("RESOURCE_NOT_FOUND", "execution unavailable")
	case codes.InvalidArgument:
		return errors.BadRequest("INVALID_MODELDEV_REGISTRATION", "invalid original execution")
	case codes.FailedPrecondition:
		return errors.New(412, "MODEL_REGISTRATION_UNAVAILABLE", "model registration unavailable")
	default:
		return errors.ServiceUnavailable("MODELDEV_REGISTRATION_UNAVAILABLE", "model registration unavailable")
	}
}
