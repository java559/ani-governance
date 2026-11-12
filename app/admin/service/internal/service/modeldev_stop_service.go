package service

import (
	"context"
	stderrors "errors"

	"github.com/go-kratos/kratos/v2/errors"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"

	"go-wind-admin/pkg/telemetry"
)

// Stop records a current authorized user's irreversible intent. It deliberately
// requires neither the original actor nor a reachable ModelDev owner.
func (s *ModelDevService) StopExecution(ctx context.Context, in *modeldevv1.StopExecutionRequest) (*modeldevv1.StopExecutionResponse, error) {
	principal, err := auth.PrincipalFromContext(ctx)
	if err != nil || principal == nil || principal.ID == 0 {
		return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
	}
	if principal.Type != auth.SubjectUser || principal.TenantID == 0 {
		return nil, errors.Forbidden("FORBIDDEN", "tenant user required")
	}
	if s == nil || s.authorization == nil || s.tenants == nil || s.acceptances == nil {
		return nil, modelDevStopFailure(nil)
	}
	if err := s.authorization.AuthorizeStop(ctx, principal.TenantID, principal.ID); err != nil {
		return nil, modelDevStopFailure(err)
	}
	if in == nil || !modelDevQueryUUID(in.ExecutionId) {
		return nil, errors.BadRequest("INVALID_MODELDEV_STOP", "invalid modeldev stop request")
	}
	tenantID, err := s.tenants.ResourceTenantID(ctx, principal.TenantID)
	if err != nil || !modelDevQueryUUID(tenantID) {
		return nil, modelDevStopFailure(err)
	}
	actor, err := principal.Actor()
	if err != nil {
		return nil, errors.Forbidden("FORBIDDEN", "tenant user required")
	}
	intent, replayed, err := s.acceptances.AcceptStop(ctx, principal.TenantID, tenantID, in.ExecutionId, actor, telemetry.Capture(ctx))
	if err != nil {
		return nil, modelDevStopFailure(err)
	}
	return &modeldevv1.StopExecutionResponse{OperationId: intent.OperationID, ExecutionId: intent.ExecutionID, StopRequested: true, IntentGeneration: intent.Generation, Replayed: replayed}, nil
}

func modelDevStopFailure(err error) error {
	switch {
	case stderrors.Is(err, data.ErrModelDevAuthorizationDenied):
		return errors.Forbidden("FORBIDDEN", "modeldev stop forbidden")
	case stderrors.Is(err, data.ErrModelDevAcceptanceNotFound):
		return errors.NotFound("RESOURCE_NOT_FOUND", "execution unavailable")
	case stderrors.Is(err, context.Canceled):
		return errors.New(499, "MODELDEV_STOP_CANCELED", "modeldev stop canceled")
	case stderrors.Is(err, context.DeadlineExceeded):
		return errors.New(504, "MODELDEV_STOP_TIMEOUT", "modeldev stop timed out")
	default:
		return errors.ServiceUnavailable("MODELDEV_STOP_UNAVAILABLE", "modeldev stop unavailable")
	}
}
