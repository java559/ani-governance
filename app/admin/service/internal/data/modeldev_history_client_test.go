package data

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestModelDevExecutionHistoryRejectsInventedTerminalFacts(t *testing.T) {
	f := modelDevResponseFixture(t)
	execution := &modeldevv1.ExecutionView{Identity: &trainingv1.ExecutionIdentity{ExecutionId: uuid.NewString(), OperationId: uuid.NewString(), ExecutionSpecHash: strings.Repeat("a", 64)}, Kind: trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING, ImplementationId: "builtin.mlp", ComputePresetId: f.selection.PresetId, ComputeOrigin: f.response.Snapshot.ComputeOrigin, Timing: f.response.Snapshot.Timing, AcceptedAt: f.response.Snapshot.Timing.AcceptedAt, DeadlineAt: f.response.Snapshot.Timing.ExecutionDeadlineAt, ObservedAt: timestamppb.Now(), States: &modeldevv1.ExecutionStates{ComputeState: modeldevv1.ComputeState_COMPUTE_STATE_SUCCEEDED, DeliveryState: modeldevv1.DeliveryState_DELIVERY_STATE_PUBLISHED, CloseState: modeldevv1.CloseState_CLOSE_STATE_CLOSED, ResourceState: modeldevv1.ResourceState_RESOURCE_STATE_NOT_APPLICABLE}}
	observed := execution.AcceptedAt.AsTime().Add(time.Minute)
	execution.History = &modeldevv1.ExecutionHistoryView{RunId: uuid.NewString(), WorkflowUid: uuid.NewString(), ObservedAt: timestamppb.New(observed), RunState: modeldevv1.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_SUCCEEDED, RunFinishedAt: timestamppb.New(observed.Add(-2 * time.Second)), WorkflowPhase: modeldevv1.WorkflowTerminalPhase_WORKFLOW_TERMINAL_PHASE_SUCCEEDED, WorkflowFinishedAt: timestamppb.New(observed.Add(-time.Second)), WorkflowResourceVersion: "51930001", WorkflowNodesSha256: strings.Repeat("a", 64)}
	for _, test := range []struct {
		name   string
		change func(*modeldevv1.ExecutionView)
		valid  bool
	}{
		{"terminal owner evidence", func(*modeldevv1.ExecutionView) {}, true},
		{"absent before terminal evidence", func(e *modeldevv1.ExecutionView) { e.History = nil }, true},
		{"managed close before owner terminal observation", func(e *modeldevv1.ExecutionView) {
			h := e.History
			h.RunState = 0
			h.RunFinishedAt = nil
			h.WorkflowPhase = 0
			h.WorkflowFinishedAt = nil
			h.WorkflowResourceVersion = ""
			h.WorkflowNodesSha256 = ""
		}, true},
		{"no dispatch", func(e *modeldevv1.ExecutionView) {
			e.History = &modeldevv1.ExecutionHistoryView{ObservedAt: timestamppb.New(observed)}
		}, true},
		{"invalid run", func(e *modeldevv1.ExecutionView) { e.History.RunId = "invalid" }, false},
		{"invalid workflow", func(e *modeldevv1.ExecutionView) { e.History.WorkflowUid = "00000000-0000-0000-0000-000000000000" }, false},
		{"missing one identity", func(e *modeldevv1.ExecutionView) { e.History.WorkflowUid = "" }, false},
		{"missing observation", func(e *modeldevv1.ExecutionView) { e.History.ObservedAt = nil }, false},
		{"unknown run state", func(e *modeldevv1.ExecutionView) { e.History.RunState = 99 }, false},
		{"unknown workflow phase", func(e *modeldevv1.ExecutionView) { e.History.WorkflowPhase = 99 }, false},
		{"partial terminal", func(e *modeldevv1.ExecutionView) { e.History.RunState = 0 }, false},
		{"run time after proof", func(e *modeldevv1.ExecutionView) {
			e.History.RunFinishedAt = timestamppb.New(observed.Add(time.Second))
		}, false},
		{"workflow time before accepted", func(e *modeldevv1.ExecutionView) {
			e.History.WorkflowFinishedAt = timestamppb.New(e.AcceptedAt.AsTime().Add(-time.Second))
		}, false},
		{"missing finish", func(e *modeldevv1.ExecutionView) { e.History.WorkflowFinishedAt = nil }, false},
		{"invalid timestamp", func(e *modeldevv1.ExecutionView) {
			e.History.RunFinishedAt = &timestamppb.Timestamp{Seconds: 253402300800}
		}, false},
		{"invalid digest", func(e *modeldevv1.ExecutionView) { e.History.WorkflowNodesSha256 = strings.Repeat("A", 64) }, false},
		{"bad resource version", func(e *modeldevv1.ExecutionView) { e.History.WorkflowResourceVersion = "rv\nprivate" }, false},
		{"missing resource version", func(e *modeldevv1.ExecutionView) { e.History.WorkflowResourceVersion = "" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := proto.Clone(execution).(*modeldevv1.ExecutionView)
			test.change(e)
			require.Equal(t, test.valid, modelDevValidExecution(e))
		})
	}
}
