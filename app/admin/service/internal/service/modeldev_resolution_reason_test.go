package service

import (
	"testing"

	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
)

// A catalogue answer that cannot change by retrying is a business rejection,
// not an upstream outage.
func TestModelDevResolutionBusinessReasons(t *testing.T) {
	for _, reason := range []modeldevcontractv1.ErrorReason{
		modeldevcontractv1.ErrorReason_ERROR_REASON_IMPLEMENTATION_UNAVAILABLE,
		modeldevcontractv1.ErrorReason_ERROR_REASON_UNSUPPORTED_DEVICE,
		modeldevcontractv1.ErrorReason_ERROR_REASON_BASE_MODEL_INVALID,
	} {
		if !modelDevResolutionBusinessReason(reason) {
			t.Fatalf("%s must be a business reason", reason)
		}
	}
	for _, reason := range []modeldevcontractv1.ErrorReason{
		modeldevcontractv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE,
		modeldevcontractv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY,
		modeldevcontractv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT,
	} {
		if modelDevResolutionBusinessReason(reason) {
			t.Fatalf("%s must stay a transport/environment failure", reason)
		}
	}
}
