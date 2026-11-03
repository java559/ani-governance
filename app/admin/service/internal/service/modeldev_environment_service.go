package service

import (
	"context"
	stderrors "errors"
	"github.com/go-kratos/kratos/v2/errors"
	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"time"
)

func (s *ModelDevService) environmentScope(ctx context.Context, ensure bool) (data.ModelDevResolveScope, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return data.ModelDevResolveScope{}, errors.Unauthorized("INVALID_LOGIN", "user login required")
	}
	if p.Type != auth.SubjectUser || p.TenantID == 0 {
		return data.ModelDevResolveScope{}, errors.Forbidden("FORBIDDEN", "tenant user required")
	}
	if s == nil || s.authorization == nil || s.tenants == nil || s.resolver == nil {
		return data.ModelDevResolveScope{}, modelDevEnvironmentUnavailable()
	}
	authorize, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err = s.authorization.AuthorizeEnvironment(authorize, p.TenantID, p.ID, ensure); err != nil {
		return data.ModelDevResolveScope{}, modelDevEnvironmentFailure(err)
	}
	tenant, err := s.tenants.ResourceTenantID(authorize, p.TenantID)
	if err != nil {
		return data.ModelDevResolveScope{}, modelDevEnvironmentFailure(err)
	}
	if !modelDevQueryUUID(tenant) {
		return data.ModelDevResolveScope{}, modelDevEnvironmentUnavailable()
	}
	actor, err := p.Actor()
	if err != nil {
		return data.ModelDevResolveScope{}, errors.Forbidden("FORBIDDEN", "tenant user required")
	}
	return data.ModelDevResolveScope{ResourceTenantID: tenant, Actor: actor}, nil
}
func (s *ModelDevService) EnsureTenantExecutionEnvironment(ctx context.Context, operationID string) (*modeldevv1.EnsureTenantExecutionEnvironmentResponse, error) {
	if !modelDevQueryUUID(operationID) {
		return nil, errors.BadRequest("INVALID_ARGUMENT", "canonical Idempotency-Key required")
	}
	scope, err := s.environmentScope(ctx, true)
	if err != nil {
		return nil, err
	}
	out, err := s.resolver.EnsureTenantExecutionEnvironment(ctx, scope, &modeldevcontractv1.EnsureTenantExecutionEnvironmentRequest{OperationId: operationID})
	if err != nil {
		return nil, modelDevEnvironmentFailure(err)
	}
	return &modeldevv1.EnsureTenantExecutionEnvironmentResponse{Environment: modelDevEnvironmentView(out.Environment)}, nil
}
func (s *ModelDevService) GetTenantExecutionEnvironment(ctx context.Context) (*modeldevv1.GetTenantExecutionEnvironmentResponse, error) {
	scope, err := s.environmentScope(ctx, false)
	if err != nil {
		return nil, err
	}
	out, err := s.resolver.GetTenantExecutionEnvironment(ctx, scope, &modeldevcontractv1.GetTenantExecutionEnvironmentRequest{})
	if err != nil {
		return nil, modelDevEnvironmentFailure(err)
	}
	return &modeldevv1.GetTenantExecutionEnvironmentResponse{Environment: modelDevEnvironmentView(out.Environment)}, nil
}
func modelDevEnvironmentView(v *modeldevcontractv1.TenantExecutionEnvironmentView) *modeldevv1.TenantExecutionEnvironmentView {
	return &modeldevv1.TenantExecutionEnvironmentView{EnvironmentId: v.EnvironmentId, ClusterId: v.ClusterId, NamespaceName: v.NamespaceName, NamespaceUid: v.NamespaceUid, State: strings.TrimPrefix(v.State.String(), "TENANT_EXECUTION_ENVIRONMENT_STATE_"), Phase: strings.TrimPrefix(v.Phase.String(), "TENANT_EXECUTION_ENVIRONMENT_PHASE_"), ReasonCode: v.ReasonCode, Retryable: v.Retryable, RetryAfterSeconds: v.RetryAfterSeconds, BindingDigest: v.BindingDigest, ExperimentId: v.ExperimentId, UpdatedAt: v.UpdatedAt, LastVerifiedAt: v.LastVerifiedAt}
}
func modelDevEnvironmentUnavailable() error {
	return errors.ServiceUnavailable("UPSTREAM_UNAVAILABLE", "environment service unavailable")
}
func modelDevEnvironmentFailure(err error) error {
	switch {
	case stderrors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled:
		return errors.New(499, "CANCELED", "environment request canceled")
	case stderrors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded:
		return errors.New(504, "DEADLINE_EXCEEDED", "environment request timed out")
	case stderrors.Is(err, data.ErrModelDevAuthorizationDenied):
		return errors.Forbidden("FORBIDDEN", "environment permission required")
	}
	failure, ok := status.FromError(err)
	if !ok || len(failure.Details()) != 1 {
		return modelDevEnvironmentUnavailable()
	}
	d, ok := failure.Details()[0].(*modeldevcontractv1.ErrorDetail)
	if ok && d != nil && failure.Code() == codes.NotFound && d.Reason == modeldevcontractv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_REGISTERED {
		return errors.NotFound("ENVIRONMENT_NOT_REGISTERED", "environment is not registered")
	}
	return modelDevEnvironmentUnavailable()
}
