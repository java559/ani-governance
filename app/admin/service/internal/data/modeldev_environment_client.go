package data

import (
	"context"
	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"strings"
	"time"
)

var modelDevEnvironmentReasons = map[string]bool{
	"TENANT_NAMESPACE_NOT_FOUND": true, "TENANT_NAMESPACE_ACCESS_DENIED": true, "ENVIRONMENT_NOT_REGISTERED": true, "ENVIRONMENT_PROVISIONING": true, "COMPUTE_CLUSTER_MISMATCH": true,
	"TENANT_NAMESPACE_OWNERSHIP_MISMATCH": true, "TENANT_NAMESPACE_NOT_ACTIVE": true, "TENANT_NAMESPACE_UID_CHANGED": true, "ENVIRONMENT_OBJECT_CONFLICT": true, "ENVIRONMENT_CONFIG_DRIFT": true,
	"KFP_AUTHENTICATION_FAILED": true, "KFP_PERMISSION_DENIED": true, "KFP_EXPERIMENT_CONFLICT": true, "KFP_EXPERIMENT_RESULT_UNCERTAIN": true, "KFP_ARTIFACT_CONFIGURATION_NOT_READY": true,
	"ENVIRONMENT_AUTHORIZATION_UNVERIFIED": true, "KFP_ARTIFACT_ACCESS_NOT_ISOLATED": true, "FIXED_PIPELINE_MATERIAL_MISMATCH": true, "TENANT_IMAGE_SPACE_NOT_READY": true,
	"MANAGED_IMAGE_REGISTRY_MISMATCH": true, "INCOMPATIBLE_ADMISSION_POLICY": true, "ENVIRONMENT_DEPENDENCY_UNAVAILABLE": true,
}

// ModelDevEnvironmentReason accepts only the frozen typed admission failure.
func ModelDevEnvironmentReason(detail *modeldevv1.ErrorDetail) (string, bool) {
	if detail == nil || modelDevUnknownFields(detail.ProtoReflect()) || detail.Reason != modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY || detail.Kind != "tenant_environment" || detail.LimitName != "" || detail.Actual != nil || detail.Limit != nil || len(detail.Violations) != 1 || detail.Violations[0] == nil {
		return "", false
	}
	v := detail.Violations[0]
	if v.Field != "environment" || !modelDevEnvironmentReasons[v.Reason] {
		return "", false
	}
	return v.Reason, true
}
func (c *ModelDevClient) environmentContext(ctx context.Context, scope ModelDevResolveScope, ensure bool) (context.Context, context.CancelFunc, string, error) {
	if c == nil || c.connection == nil || !modelDevCanonicalUUID(scope.ResourceTenantID) || !validModelDevClientActor(scope.Actor) {
		return nil, nil, "", modelDevAdmissionUnavailable()
	}
	budget, method := 3*time.Second, modeldevv1.ModelDevTenantEnvironmentService_GetTenantExecutionEnvironment_FullMethodName
	if ensure {
		budget, method = 28*time.Second, modeldevv1.ModelDevTenantEnvironmentService_EnsureTenantExecutionEnvironment_FullMethodName
	}
	call, cancel := context.WithTimeout(ctx, budget)
	requestID := uuid.NewString()
	return metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", scope.ResourceTenantID, "x-ani-actor", scope.Actor, "x-ani-request-id", requestID, "x-ani-authorized-method", method, "x-ani-data-scope", "tenant-all")), cancel, requestID, nil
}
func (c *ModelDevClient) EnsureTenantExecutionEnvironment(ctx context.Context, scope ModelDevResolveScope, in *modeldevv1.EnsureTenantExecutionEnvironmentRequest) (*modeldevv1.EnsureTenantExecutionEnvironmentResponse, error) {
	if in == nil || modelDevUnknownFields(in.ProtoReflect()) || !modelDevCanonicalUUID(in.OperationId) {
		return nil, modelDevAdmissionUnavailable()
	}
	call, cancel, id, err := c.environmentContext(ctx, scope, true)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevTenantEnvironmentServiceClient(c.connection).EnsureTenantExecutionEnvironment(call, in, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevEnvironmentRPCError(err, id, false)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || !modelDevValidEnvironment(out.Environment, scope.ResourceTenantID) {
		return nil, modelDevAdmissionUnavailable()
	}
	return out, nil
}
func (c *ModelDevClient) GetTenantExecutionEnvironment(ctx context.Context, scope ModelDevResolveScope, in *modeldevv1.GetTenantExecutionEnvironmentRequest) (*modeldevv1.GetTenantExecutionEnvironmentResponse, error) {
	if in == nil || modelDevUnknownFields(in.ProtoReflect()) {
		return nil, modelDevAdmissionUnavailable()
	}
	call, cancel, id, err := c.environmentContext(ctx, scope, false)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevTenantEnvironmentServiceClient(c.connection).GetTenantExecutionEnvironment(call, in, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevEnvironmentRPCError(err, id, true)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || !modelDevValidEnvironment(out.Environment, scope.ResourceTenantID) {
		return nil, modelDevAdmissionUnavailable()
	}
	return out, nil
}
func modelDevEnvironmentRPCError(err error, id string, get bool) error {
	failure, ok := status.FromError(err)
	if !ok || len(failure.Proto().ProtoReflect().GetUnknown()) != 0 {
		return modelDevAdmissionUnavailable()
	}
	if len(failure.Proto().Details) == 0 {
		if failure.Code() == codes.Canceled || failure.Code() == codes.DeadlineExceeded {
			return status.Error(failure.Code(), "environment request interrupted")
		}
		return modelDevAdmissionUnavailable()
	}
	if len(failure.Proto().Details) != 1 || failure.Proto().Details[0] == nil || len(failure.Proto().Details[0].ProtoReflect().GetUnknown()) != 0 {
		return modelDevAdmissionUnavailable()
	}
	details := failure.Details()
	if len(details) != 1 {
		return modelDevAdmissionUnavailable()
	}
	d, ok := details[0].(*modeldevv1.ErrorDetail)
	if !ok || d == nil || modelDevUnknownFields(d.ProtoReflect()) || d.CorrelationId != id || d.Kind != "" || len(d.Violations) != 0 || d.LimitName != "" || d.Actual != nil || d.Limit != nil {
		return modelDevAdmissionUnavailable()
	}
	code, reason := failure.Code(), d.Reason
	if !(get && code == codes.NotFound && reason == modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_REGISTERED) && !(code == codes.Unavailable && reason == modeldevv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE) {
		return modelDevAdmissionUnavailable()
	}
	clean := &modeldevv1.ErrorDetail{Reason: reason, CorrelationId: id, SafeMessage: strings.TrimPrefix(reason.String(), "ERROR_REASON_")}
	out, e := status.New(code, clean.SafeMessage).WithDetails(clean)
	if e != nil {
		return modelDevAdmissionUnavailable()
	}
	return out.Err()
}
func modelDevValidEnvironment(v *modeldevv1.TenantExecutionEnvironmentView, tenant string) bool {
	if v == nil || modelDevUnknownFields(v.ProtoReflect()) || !modelDevCanonicalUUID(v.EnvironmentId) || !modelDevCanonicalUUID(v.ClusterId) || !modelDevCanonicalUUID(tenant) || !modelDevValidTimestamp(v.UpdatedAt) {
		return false
	}
	namespaceTenant, ok := strings.CutPrefix(v.NamespaceName, "tenant-")
	if !ok || namespaceTenant != tenant {
		return false
	}
	if v.NamespaceUid != nil && (!modelDevCanonicalUUID(*v.NamespaceUid)) {
		return false
	}
	if v.BindingDigest != nil && !modelDevDigest(*v.BindingDigest) {
		return false
	}
	if v.ExperimentId != nil && !modelDevCanonicalUUID(*v.ExperimentId) {
		return false
	}
	if v.LastVerifiedAt != nil && !modelDevValidTimestamp(v.LastVerifiedAt) {
		return false
	}
	phase := int32(v.Phase)
	if phase < 1 || phase > 6 {
		return false
	}
	switch int32(v.State) {
	case 1:
		return phase <= 5 && v.Retryable && v.RetryAfterSeconds == 2 && (v.ReasonCode == "" || v.ReasonCode == "KFP_EXPERIMENT_RESULT_UNCERTAIN")
	case 2:
		return phase == 6 && v.NamespaceUid != nil && v.BindingDigest != nil && v.ExperimentId != nil && v.LastVerifiedAt != nil && v.ReasonCode == "" && !v.Retryable && v.RetryAfterSeconds == 0
	case 3, 4:
		if !modelDevEnvironmentReasons[v.ReasonCode] || v.ReasonCode == "ENVIRONMENT_NOT_REGISTERED" || v.ReasonCode == "ENVIRONMENT_PROVISIONING" || v.ReasonCode == "COMPUTE_CLUSTER_MISMATCH" || v.ReasonCode == "KFP_EXPERIMENT_RESULT_UNCERTAIN" {
			return false
		}
		return int32(v.State) != 4 || !v.Retryable
	default:
		return false
	}
}

// Clone only validated error details before preserving a finite cause.
func modelDevCopyEnvironmentDetail(out, in *modeldevv1.ErrorDetail) bool {
	if _, ok := ModelDevEnvironmentReason(in); !ok {
		return false
	}
	out.Kind = in.Kind
	out.Violations = []*modeldevv1.FieldViolation{proto.Clone(in.Violations[0]).(*modeldevv1.FieldViolation)}
	return true
}
