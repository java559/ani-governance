package service

import (
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestModelDevResolutionHTTPMappingRetainsExactLimitFacts(t *testing.T) {
	actual, limit := int64(9007199254740993), int64(9007199254740992)
	id := uuid.NewString()
	for _, tc := range []struct {
		grpc   codes.Code
		reason modeldevv1.ErrorReason
		http   int
	}{
		{codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED, 422},
		{codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_CONTEXT_LIMIT_UNKNOWN, 422},
		{codes.ResourceExhausted, modeldevv1.ErrorReason_ERROR_REASON_INPUT_VALIDATION_BUSY, 429},
		{codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_RUNTIME_UNAVAILABLE, 503},
		{codes.PermissionDenied, modeldevv1.ErrorReason_ERROR_REASON_FORBIDDEN, 403},
		{codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, 404},
		{codes.AlreadyExists, modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT, 409},
	} {
		detail := &modeldevv1.ErrorDetail{Reason: tc.reason, CorrelationId: id}
		if tc.reason == modeldevv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED {
			detail.Kind = "dataset"
			detail.LimitName = "max_content_bytes"
			detail.Actual = &actual
			detail.Limit = &limit
		}
		failure, err := status.New(tc.grpc, "sanitized").WithDetails(detail)
		require.NoError(t, err)
		out := errors.FromError(modelDevCreateResolutionFailure(failure.Err()))
		require.EqualValues(t, tc.http, out.Code)
		if detail.Actual != nil {
			require.Equal(t, "9007199254740993", out.Metadata["actual"])
			require.Equal(t, "9007199254740992", out.Metadata["limit"])
			require.Equal(t, id, out.Metadata["upstream_request_id"])
		}
	}
	require.EqualValues(t, 504, errors.FromError(modelDevCreateResolutionFailure(status.Error(codes.DeadlineExceeded, "remote"))).Code)
}
