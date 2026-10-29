package data

import (
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
	"time"
)

func TestReadyModelDevInputAllowsNonBaselineRowCounts(t *testing.T) {
	for _, format := range []string{"CSV", "JSON"} {
		for _, rows := range []int64{1, 65, 1031, 9007199254740993} {
			in := &modeldevv1.InputVersionView{InputVersionId: "8f62c16d-ff9c-442f-bba8-5d2fd2e9f3e3", State: modeldevv1.InputState_INPUT_STATE_READY, Format: format, RowCount: rows, SizeBytes: 128, Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", CreatedAt: timestamppb.New(time.Now())}
			if format == "CSV" {
				in.FeatureCount = 16
			}
			if !modelDevValidInput(in) {
				t.Fatalf("READY %s with %d rows rejected", format, rows)
			}
			in.RowCount = 0
			if modelDevValidInput(in) {
				t.Fatalf("empty READY %s accepted", format)
			}
		}
	}
}
