package data

import (
	"testing"
	"time"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The public detail surfaces the derived timeline and curve, so both must clear
// the same fail-closed validation as every other field.
func TestModelDevTimelineAndCurveValidation(t *testing.T) {
	started := timestamppb.New(time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC))
	step := int64(3)
	for _, test := range []struct {
		name    string
		steps   []*modeldevv1.StepProgress
		metrics []*trainingv1.Metric
		want    bool
	}{
		{name: "empty", want: true},
		{name: "derived timeline", steps: []*modeldevv1.StepProgress{
			{Role: "prepare", State: "SUCCEEDED", StartedAt: started, FinishedAt: started, DeadlineAt: started},
			{Role: "train-wait", State: "RUNNING", StartedAt: started},
			{Role: "export", State: "FAILED", StartedAt: started, FinishedAt: started, ExitCode: 2},
			{Role: "close", State: "PENDING"},
		}, want: true},
		{name: "unknown role", steps: []*modeldevv1.StepProgress{{Role: "extra", State: "PENDING"}}, want: false},
		{name: "unknown state", steps: []*modeldevv1.StepProgress{{Role: "prepare", State: "UNKNOWN"}}, want: false},
		{name: "negative exit code", steps: []*modeldevv1.StepProgress{{Role: "export", State: "FAILED", ExitCode: -1}}, want: false},
		{name: "invalid start", steps: []*modeldevv1.StepProgress{{Role: "prepare", State: "RUNNING", StartedAt: &timestamppb.Timestamp{Seconds: 1 << 40}}}, want: false},
		{name: "nil step", steps: []*modeldevv1.StepProgress{nil}, want: false},
		{name: "loss curve", metrics: []*trainingv1.Metric{{Name: "train.loss", DecimalValue: "0.87", Step: &step}}, want: true},
		{name: "unnamed metric", metrics: []*trainingv1.Metric{{DecimalValue: "0.87"}}, want: false},
		{name: "non numeric value", metrics: []*trainingv1.Metric{{Name: "train.loss", DecimalValue: "NaN-ish"}}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := modelDevValidSteps(test.steps) && modelDevValidMetrics(test.metrics)
			if got != test.want {
				t.Fatalf("valid=%v, want %v", got, test.want)
			}
		})
	}
}
