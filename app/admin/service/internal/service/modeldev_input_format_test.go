package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The base model identity must be published verbatim so a client can name what a
// finetuning run started from. Absent for general training.
func TestModelDevPublicExecutionExposesBaseModel(t *testing.T) {
	in := &modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
		States:     &modeldevcontractv1.ExecutionStates{},
		AcceptedAt: timestamppb.New(time.Now()), DeadlineAt: timestamppb.New(time.Now()), ObservedAt: timestamppb.New(time.Now()),
		BaseModel: &modeldevcontractv1.BaseModelIdentity{
			ModelId: "66666666-6666-4666-8666-666666666666", VersionId: "77777777-7777-4777-8777-777777777777",
			Family: "Qwen2.5-0.5B-Instruct", Format: "safetensors",
		},
	}
	out := modelDevPublicExecution(in)
	require.NotNil(t, out.BaseModel)
	require.Equal(t, "66666666-6666-4666-8666-666666666666", out.BaseModel.ModelId)
	require.Equal(t, "77777777-7777-4777-8777-777777777777", out.BaseModel.VersionId)
	require.Equal(t, "Qwen2.5-0.5B-Instruct", out.BaseModel.Family)
	require.Equal(t, "safetensors", out.BaseModel.Format)

	// General training carries no base model, and the detail must not invent one.
	general := &modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
		States:     &modeldevcontractv1.ExecutionStates{},
		AcceptedAt: timestamppb.New(time.Now()), DeadlineAt: timestamppb.New(time.Now()), ObservedAt: timestamppb.New(time.Now()),
	}
	require.Nil(t, modelDevPublicExecution(general).BaseModel)
}

// Only the two verified formats may be forwarded upstream; anything else has to
// be rejected here rather than becoming an empty page.
func TestModelDevInputFormatSelection(t *testing.T) {
	format, ok := modelDevInputFormat(nil)
	require.True(t, ok)
	require.Nil(t, format)

	for _, value := range []string{"CSV", "JSON"} {
		value := value
		selected, ok := modelDevInputFormat(&value)
		require.True(t, ok, value)
		require.NotNil(t, selected)
		require.Equal(t, value, *selected)
	}

	for _, value := range []string{"", "csv", "json", "PARQUET", "CSV,JSON", " CSV"} {
		value := value
		_, ok := modelDevInputFormat(&value)
		require.False(t, ok, value)
	}
}
