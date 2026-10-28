package data

import (
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func modelDevArtifactWithFilename(name string) *modeldevv1.ArtifactView {
	return &modeldevv1.ArtifactView{
		ArtifactId:    "0377b660-dd42-50a1-85a6-9a95c54b6bb9",
		ExecutionId:   "64320bf3-0a9a-41cc-8b03-557b6dc75d58",
		Filename:      name,
		Role:          trainingv1.FileRole_FILE_ROLE_HF_MODEL,
		SizeBytes:     2776833,
		Sha256:        "ca10d7e9fb3ed18575dd1e277a2579c16d108e32f27439684afa0e10b1440910",
		DeliveryState: modeldevv1.DeliveryState_DELIVERY_STATE_PUBLISHED,
		VerifiedAt:    timestamppb.Now(),
	}
}

// The owner publishes nested artifact paths (export/merged/..., training/adapter/...).
// This pins that the client accepts exactly the real owner shape, so the
// validation can never silently reject a whole list again.
func TestModelDevValidArtifactAcceptsRealOwnerShape(t *testing.T) {
	accepted := []string{
		"export/merged/vocab.json",
		"export/merged/model.safetensors",
		"training/adapter/adapter_config.json",
		"reports/export/export.json",
		"reports/train/metrics.jsonl",
		"model.pt",
	}
	for _, name := range accepted {
		if !modelDevValidArtifact(modelDevArtifactWithFilename(name)) {
			t.Errorf("real owner artifact path rejected: %q", name)
		}
	}
}

// Traversal and control characters must still be rejected after allowing nesting.
func TestModelDevValidArtifactRejectsTraversal(t *testing.T) {
	bad := []string{
		"../escape.json",
		"a/../../escape.json",
		"/absolute/path.json",
		"trailing/slash/",
		"windows\\path.json",
		".",
		"..",
		"",
		"a/\x00b.json",
		"a/\tb.json",
	}
	for _, name := range bad {
		if modelDevValidArtifact(modelDevArtifactWithFilename(name)) {
			t.Errorf("unsafe filename accepted: %q", name)
		}
	}
}
