package data

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestModelDevResolutionPreservesFiniteCurrentErrors(t *testing.T) {
	id := uuid.NewString()
	actual, limit := int64(9007199254740993), int64(9007199254740992)
	for _, tc := range []struct {
		name   string
		code   codes.Code
		reason modeldevv1.ErrorReason
		limits bool
	}{
		{"unsupported", codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_BASE_MODEL_UNSUPPORTED, false},
		{"template required", codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_REQUIRED, false},
		{"content", codes.FailedPrecondition, modeldevv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED, true},
		{"busy", codes.ResourceExhausted, modeldevv1.ErrorReason_ERROR_REASON_INPUT_VALIDATION_BUSY, false},
		{"runtime", codes.Unavailable, modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_RUNTIME_UNAVAILABLE, false},
		{"forbidden", codes.PermissionDenied, modeldevv1.ErrorReason_ERROR_REASON_FORBIDDEN, false},
		{"absent", codes.NotFound, modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, false},
		{"conflict", codes.AlreadyExists, modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := &modeldevv1.ErrorDetail{Reason: tc.reason, SafeMessage: "private URL/token/config must be discarded", CorrelationId: id}
			if tc.limits {
				detail.Kind = "base_model"
				detail.LimitName = "max_content_bytes"
				detail.Actual = &actual
				detail.Limit = &limit
			}
			failure, err := status.New(tc.code, "private raw status").WithDetails(detail)
			require.NoError(t, err)
			out := modelDevResolutionError(failure.Err(), id)
			require.Equal(t, tc.code, status.Code(out))
			require.NotContains(t, out.Error(), "private")
			clean := status.Convert(out).Details()[0].(*modeldevv1.ErrorDetail)
			require.Equal(t, tc.reason, clean.Reason)
			require.Equal(t, id, clean.CorrelationId)
			if tc.limits {
				require.Equal(t, actual, *clean.Actual)
				require.Equal(t, limit, *clean.Limit)
			}
			for _, mutate := range []func(*modeldevv1.ErrorDetail){
				func(d *modeldevv1.ErrorDetail) { d.CorrelationId = uuid.NewString() },
				func(d *modeldevv1.ErrorDetail) { d.Kind = "https://private-source.invalid/token" },
				func(d *modeldevv1.ErrorDetail) { d.LimitName = strings.Repeat("private", 50) },
			} {
				bad := proto.Clone(detail).(*modeldevv1.ErrorDetail)
				mutate(bad)
				failure, err := status.New(tc.code, "private").WithDetails(bad)
				require.NoError(t, err)
				rejected := modelDevResolutionError(failure.Err(), id)
				require.Equal(t, codes.Unavailable, status.Code(rejected))
				require.NotContains(t, rejected.Error(), "private")
			}
		})
	}
}
