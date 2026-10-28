package data

import (
	"testing"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/protobuf/proto"
)

// The owner added ExecutionView.steps in the execution-detail contract. A pinned
// older contract cannot decode that field and would report it as unknown, which
// is what produced MODELDEV_QUERY_UNAVAILABLE for every read. This guards the
// pinned version: a response carrying steps must decode without unknown fields.
func TestModelDevClientDecodesExecutionViewSteps(t *testing.T) {
	owner := &modeldevv1.ExecutionView{
		Steps: []*modeldevv1.StepProgress{
			{Role: "prepare", State: "SUCCEEDED", ExitCode: 0},
			{Role: "train", State: "RUNNING"},
		},
	}
	encoded, err := proto.Marshal(owner)
	if err != nil {
		t.Fatalf("marshal owner view: %v", err)
	}
	decoded := &modeldevv1.ExecutionView{}
	if err := proto.Unmarshal(encoded, decoded); err != nil {
		t.Fatalf("unmarshal owner view: %v", err)
	}
	if len(decoded.GetSteps()) != 2 || decoded.GetSteps()[0].GetRole() != "prepare" {
		t.Fatalf("steps were not decoded: %+v", decoded.GetSteps())
	}
	if modelDevUnknownFields(decoded.ProtoReflect()) {
		t.Fatal("steps is reported as an unknown field: the pinned contract predates ExecutionView.steps")
	}
}
