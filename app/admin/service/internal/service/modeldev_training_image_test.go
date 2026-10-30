package service

import (
	"testing"
	"time"

	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The public detail publishes the frozen catalogue digest and Resource's
// authoritative registry host for general training, and nothing for
// finetuning, whose image identity is material-fixed.
func TestModelDevPublicExecutionPublishesTrainingImageFactsOnlyForTraining(t *testing.T) {
	at := time.Date(2026, 10, 29, 2, 0, 0, 0, time.UTC)
	training := &modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "execution"},
		Kind:       trainingcontractv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING,
		States:     &modeldevcontractv1.ExecutionStates{},
		AcceptedAt: timestamppb.New(at), DeadlineAt: timestamppb.New(at), ObservedAt: timestamppb.New(at),
		ImageId: "img_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ImageVersion: 3, ImageScope: "platform",
		TrainingImageDigest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", TrainingImageRegistryAuthority: "registry.example.test",
	}
	out := modelDevPublicExecution(training)
	if out.ImageId != training.ImageId || out.ImageVersion != training.ImageVersion || out.ImageScope != training.ImageScope {
		t.Fatalf("image identity not projected: %+v", out)
	}
	if out.TrainingImageDigest != training.TrainingImageDigest || out.TrainingImageRegistryAuthority != training.TrainingImageRegistryAuthority {
		t.Fatalf("training image facts not projected: %+v", out)
	}

	finetuning := &modeldevcontractv1.ExecutionView{
		Identity:   &trainingcontractv1.ExecutionIdentity{OperationId: "operation", ExecutionId: "finetuning"},
		Kind:       trainingcontractv1.ExecutionKind_EXECUTION_KIND_FINETUNING,
		States:     &modeldevcontractv1.ExecutionStates{},
		AcceptedAt: timestamppb.New(at), DeadlineAt: timestamppb.New(at), ObservedAt: timestamppb.New(at),
		ImageId: "img_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ImageVersion: 1, ImageScope: "platform",
	}
	out = modelDevPublicExecution(finetuning)
	if out.TrainingImageDigest != "" || out.TrainingImageRegistryAuthority != "" {
		t.Fatalf("finetuning published catalogue facts: %+v", out)
	}
	if out.ImageId != finetuning.ImageId || out.ImageVersion != finetuning.ImageVersion {
		t.Fatalf("finetuning image identity changed: %+v", out)
	}
}
