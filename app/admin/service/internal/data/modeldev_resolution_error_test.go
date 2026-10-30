package data

import (
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A typed catalogue rejection survives sanitization; an unknown reason does not.
func TestModelDevResolutionErrorKeepsCatalogueRejection(t *testing.T) {
	requestID := "11111111-1111-4111-8111-111111111111"
	rejection, err := status.New(codes.FailedPrecondition, "implementation software materials unavailable").
		WithDetails(&modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_IMPLEMENTATION_UNAVAILABLE, CorrelationId: requestID})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := status.FromError(modelDevResolutionError(rejection.Err(), requestID))
	if !ok || got.Code() != codes.FailedPrecondition || len(got.Details()) != 1 {
		t.Fatalf("catalogue rejection lost: %v", got)
	}
	detail, ok := got.Details()[0].(*modeldevv1.ErrorDetail)
	if !ok || detail.Reason != modeldevv1.ErrorReason_ERROR_REASON_IMPLEMENTATION_UNAVAILABLE {
		t.Fatalf("reason lost: %v", detail)
	}

	foreign, err := status.New(codes.FailedPrecondition, "x").
		WithDetails(&modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT, CorrelationId: requestID})
	if err != nil {
		t.Fatal(err)
	}
	if mapped, ok := status.FromError(modelDevResolutionError(foreign.Err(), requestID)); ok && mapped.Code() != codes.Unavailable {
		t.Fatalf("unexpected reason accepted: %v", mapped)
	}
}
