package service

import (
	"context"
	"github.com/go-kratos/kratos/v2/errors"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestModelDevEnvironmentRefusesPlatformAndAPIKeyWithoutDependencies(t *testing.T) {
	s := NewModelDevService(nil, nil, nil, nil, nil)
	for _, p := range []*auth.Principal{{Type: auth.SubjectUser, ID: 1, TenantID: 0}, {Type: auth.SubjectAPIKey, ID: 2, TenantID: 1}} {
		ctx := auth.NewPrincipalContext(context.Background(), p)
		_, err := s.EnsureTenantExecutionEnvironment(ctx, "11111111-1111-4111-8111-111111111111")
		if errors.FromError(err).Code != 403 {
			t.Fatalf("Ensure authorized principal %+v: %v", p, err)
		}
		_, err = s.GetTenantExecutionEnvironment(ctx)
		if errors.FromError(err).Code != 403 {
			t.Fatalf("Get authorized principal %+v: %v", p, err)
		}
	}
}
func TestModelDevEnvironmentPublicErrorRetainsOnlyFiniteCause(t *testing.T) {
	d := &modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY, SafeMessage: "private-sentinel", CorrelationId: "correlation", Kind: "tenant_environment", Violations: []*modeldevv1.FieldViolation{{Field: "environment", Reason: "TENANT_NAMESPACE_NOT_FOUND"}}}
	e, _ := status.New(codes.Unavailable, "private-sentinel").WithDetails(d)
	out := errors.FromError(modelDevCreateResolutionFailure(e.Err()))
	if out.Code != 503 || out.Reason != "ENVIRONMENT_NOT_READY" || len(out.Metadata) != 1 || out.Metadata["environment_reason"] != "TENANT_NAMESPACE_NOT_FOUND" {
		t.Fatalf("public error=%+v", out)
	}
	missing, _ := status.New(codes.NotFound, "private-sentinel").WithDetails(&modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_REGISTERED, CorrelationId: "correlation"})
	out = errors.FromError(modelDevEnvironmentFailure(missing.Err()))
	if out.Code != 404 || out.Reason != "ENVIRONMENT_NOT_REGISTERED" {
		t.Fatalf("missing environment: %+v", out)
	}
}
