package service

import (
	"testing"
	"time"

	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The public detail must expose the derived timeline and curve unchanged; any
// missing timestamp stays empty instead of becoming an epoch value.
func TestModelDevPublicExecutionPassesThroughStepsAndMetrics(t *testing.T) {
	started := time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC)
	step := int64(7)
	in := &modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
		States:     &modeldevcontractv1.ExecutionStates{},
		AcceptedAt: timestamppb.New(started), DeadlineAt: timestamppb.New(started), ObservedAt: timestamppb.New(started),
		Steps: []*modeldevcontractv1.StepProgress{
			{Role: "prepare", State: "SUCCEEDED", StartedAt: timestamppb.New(started), FinishedAt: timestamppb.New(started.Add(time.Minute)), ExitCode: 0},
			{Role: "train-wait", State: "RUNNING", StartedAt: timestamppb.New(started.Add(time.Minute)), DeadlineAt: timestamppb.New(started.Add(time.Hour))},
			{Role: "export", State: "PENDING"},
		},
		Metrics: []*trainingcontractv1.Metric{{Name: "train.loss", DecimalValue: "0.87", Step: &step}},
	}
	out := modelDevPublicExecution(in)
	if len(out.Steps) != 3 || len(out.Metrics) != 1 {
		t.Fatalf("steps=%d metrics=%d, want 3 and 1", len(out.Steps), len(out.Metrics))
	}
	prepare := out.Steps[0]
	if prepare.Role != "prepare" || prepare.State != "SUCCEEDED" || prepare.StartedAt != started.Format(time.RFC3339Nano) || prepare.FinishedAt != started.Add(time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("prepare=%+v", prepare)
	}
	trainWait := out.Steps[1]
	if trainWait.FinishedAt != "" || trainWait.DeadlineAt != started.Add(time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("train-wait=%+v", trainWait)
	}
	pending := out.Steps[2]
	if pending.StartedAt != "" || pending.FinishedAt != "" || pending.DeadlineAt != "" {
		t.Fatalf("pending=%+v, want no times", pending)
	}
	metric := out.Metrics[0]
	if metric.Name != "train.loss" || metric.DecimalValue != "0.87" || metric.Step == nil || *metric.Step != 7 {
		t.Fatalf("metric=%+v", metric)
	}
}

// A failed execution must explain itself. The reason is rendered without the
// ERROR_REASON_ prefix so it matches the vocabulary used elsewhere, and the
// owner-supplied safe_message is forwarded verbatim.
func TestModelDevPublicExecutionExposesFailure(t *testing.T) {
	in := &modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
		States:     &modeldevcontractv1.ExecutionStates{},
		AcceptedAt: timestamppb.New(time.Now()), DeadlineAt: timestamppb.New(time.Now()), ObservedAt: timestamppb.New(time.Now()),
		Failure: &modeldevcontractv1.ErrorDetail{
			Reason:        modeldevcontractv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE,
			SafeMessage:   "the trainer exited before writing the export",
			CorrelationId: "corr-1",
			Violations:    []*modeldevcontractv1.FieldViolation{{Field: "parameters.epochs", Reason: "out of range"}},
		},
	}
	out := modelDevPublicExecution(in)
	if out.Failure == nil {
		t.Fatal("failure dropped")
	}
	if out.Failure.Reason != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("reason=%q", out.Failure.Reason)
	}
	if out.Failure.SafeMessage != "the trainer exited before writing the export" || out.Failure.CorrelationId != "corr-1" {
		t.Fatalf("failure=%+v", out.Failure)
	}
	if len(out.Failure.Violations) != 1 || out.Failure.Violations[0].Field != "parameters.epochs" || out.Failure.Violations[0].Reason != "out of range" {
		t.Fatalf("violations=%+v", out.Failure.Violations)
	}

	// An execution that never failed must not invent an empty diagnostic.
	healthy := modelDevPublicExecution(&modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
		States:     &modeldevcontractv1.ExecutionStates{},
		AcceptedAt: timestamppb.New(time.Now()), DeadlineAt: timestamppb.New(time.Now()), ObservedAt: timestamppb.New(time.Now()),
	})
	if healthy.Failure != nil {
		t.Fatalf("unexpected failure=%+v", healthy.Failure)
	}
}

// A training failure closes with the failed step in the timeline but no
// owner-supplied ErrorDetail. The detail must still explain itself; returning
// null would read as "no problem".
func TestModelDevPublicExecutionDerivesFailureFromFailedStep(t *testing.T) {
	now := time.Now()
	failed := &modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
		States:     &modeldevcontractv1.ExecutionStates{ComputeState: modeldevcontractv1.ComputeState_COMPUTE_STATE_FAILED},
		AcceptedAt: timestamppb.New(now), DeadlineAt: timestamppb.New(now), ObservedAt: timestamppb.New(now),
		Steps: []*modeldevcontractv1.StepProgress{
			{Role: "prepare", State: "SUCCEEDED"},
			{Role: "train-wait", State: "FAILED"},
			{Role: "export", State: "PENDING"},
		},
	}
	out := modelDevPublicExecution(failed)
	if out.Failure == nil {
		t.Fatal("training failure produced no diagnostic")
	}
	if out.Failure.Reason != "STEP_FAILED" {
		t.Fatalf("reason=%q", out.Failure.Reason)
	}
	if out.Failure.SafeMessage != "step train-wait failed" {
		t.Fatalf("message=%q", out.Failure.SafeMessage)
	}
	if len(out.Failure.Violations) != 1 || out.Failure.Violations[0].Field != "steps.train-wait" || out.Failure.Violations[0].Reason != "FAILED" {
		t.Fatalf("violations=%+v", out.Failure.Violations)
	}

	// The owner's own ErrorDetail wins over the derivation.
	owner := &modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
		States:     &modeldevcontractv1.ExecutionStates{ComputeState: modeldevcontractv1.ComputeState_COMPUTE_STATE_FAILED},
		AcceptedAt: timestamppb.New(now), DeadlineAt: timestamppb.New(now), ObservedAt: timestamppb.New(now),
		Steps:   []*modeldevcontractv1.StepProgress{{Role: "train-wait", State: "FAILED"}},
		Failure: &modeldevcontractv1.ErrorDetail{Reason: modeldevcontractv1.ErrorReason_ERROR_REASON_UPSTREAM_UNAVAILABLE, SafeMessage: "owner text"},
	}
	if got := modelDevPublicExecution(owner).Failure; got.Reason != "UPSTREAM_UNAVAILABLE" || got.SafeMessage != "owner text" {
		t.Fatalf("owner detail overridden: %+v", got)
	}

	// Only a genuinely failed execution gets a derived diagnostic.
	for _, state := range []modeldevcontractv1.ComputeState{
		modeldevcontractv1.ComputeState_COMPUTE_STATE_SUCCEEDED,
		modeldevcontractv1.ComputeState_COMPUTE_STATE_TRAINING,
		modeldevcontractv1.ComputeState_COMPUTE_STATE_CANCELED,
	} {
		view := modelDevPublicExecution(&modeldevcontractv1.ExecutionView{
			Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
			States:     &modeldevcontractv1.ExecutionStates{ComputeState: state},
			AcceptedAt: timestamppb.New(now), DeadlineAt: timestamppb.New(now), ObservedAt: timestamppb.New(now),
			Steps: []*modeldevcontractv1.StepProgress{{Role: "train-wait", State: "FAILED"}},
		})
		if view.Failure != nil {
			t.Fatalf("%s produced %+v", state, view.Failure)
		}
	}
}

// The managed settings are the read-only half of the training configuration.
// They must survive the mapping unchanged, including the decimal strings.
func TestModelDevPublicExecutionExposesManagedSettings(t *testing.T) {
	in := &modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
		States:     &modeldevcontractv1.ExecutionStates{},
		AcceptedAt: timestamppb.New(time.Now()), DeadlineAt: timestamppb.New(time.Now()), ObservedAt: timestamppb.New(time.Now()),
		ManagedSettings: &modeldevcontractv1.FinetuningManagedSettings{
			Stage: "sft", FinetuningType: "lora", LoraTarget: "all", LoraRank: 8, LoraAlpha: 16,
			CutoffLen: 128, PerDeviceTrainBatchSize: 1, GradientAccumulationSteps: 1, Seed: 42, DataSeed: 42,
			Device: "cpu", FlashAttn: "disabled", ReportTo: "none", SaveStrategy: "no", Template: "qwen",
			TrainDtype: "float32", ExportDtype: "float32", VerifyDtype: "float32", LrSchedulerType: "linear",
			WarmupRatio: "0", WeightDecay: "0", LoraDropout: "0", NumTrainEpochs: 3, Optim: "adamw_torch", LoggingSteps: 1,
		},
	}
	out := modelDevPublicExecution(in)
	want := in.ManagedSettings
	got := out.ManagedSettings
	if got == nil {
		t.Fatal("managed settings dropped")
	}
	if got.Stage != want.Stage || got.FinetuningType != want.FinetuningType || got.LoraTarget != want.LoraTarget {
		t.Fatalf("type fields=%+v", got)
	}
	if got.LoraRank != want.LoraRank || got.LoraAlpha != want.LoraAlpha || got.CutoffLen != want.CutoffLen ||
		got.PerDeviceTrainBatchSize != want.PerDeviceTrainBatchSize || got.GradientAccumulationSteps != want.GradientAccumulationSteps ||
		got.Seed != want.Seed || got.DataSeed != want.DataSeed || got.NumTrainEpochs != want.NumTrainEpochs || got.LoggingSteps != want.LoggingSteps {
		t.Fatalf("integer fields=%+v", got)
	}
	if got.WarmupRatio != want.WarmupRatio || got.WeightDecay != want.WeightDecay || got.LoraDropout != want.LoraDropout {
		t.Fatalf("decimal strings=%+v", got)
	}
	if got.Fp16 != want.Fp16 || got.Bf16 != want.Bf16 {
		t.Fatalf("precision flags=%+v", got)
	}
	if got.Device != want.Device || got.FlashAttn != want.FlashAttn || got.ReportTo != want.ReportTo || got.SaveStrategy != want.SaveStrategy {
		t.Fatalf("runtime fields=%+v", got)
	}
	if got.Template != want.Template || got.TrainDtype != want.TrainDtype || got.ExportDtype != want.ExportDtype ||
		got.VerifyDtype != want.VerifyDtype || got.LrSchedulerType != want.LrSchedulerType || got.Optim != want.Optim {
		t.Fatalf("dtype fields=%+v", got)
	}

	// A general-training execution has no finetuning settings block.
	plain := modelDevPublicExecution(&modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
		States:     &modeldevcontractv1.ExecutionStates{},
		AcceptedAt: timestamppb.New(time.Now()), DeadlineAt: timestamppb.New(time.Now()), ObservedAt: timestamppb.New(time.Now()),
	})
	if plain.ManagedSettings != nil {
		t.Fatalf("unexpected managed settings=%+v", plain.ManagedSettings)
	}
}
