package service

import (
	"testing"

	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
)

// The public state names reuse the response vocabulary. Anything the owner
// would not accept — lower case, a missing member, or the UNSPECIFIED
// placeholder — must be rejected here; forwarding UNSPECIFIED would return an
// unfiltered page that looks like a filtered one.
func TestModelDevExecutionFilterConvertsAndRejectsState(t *testing.T) {
	text := func(value string) *string { return &value }
	for _, test := range []struct {
		name      string
		in        *modeldevv1.ListExecutionsRequest
		wantErr   bool
		wantCode  modeldevcontractv1.ComputeState
		wantClose modeldevcontractv1.CloseState
		wantNil   bool
	}{
		{name: "empty request means no filter", in: &modeldevv1.ListExecutionsRequest{}, wantNil: true},
		{name: "compute state maps to the owner enum", in: &modeldevv1.ListExecutionsRequest{ComputeState: text("SUCCEEDED")}, wantCode: modeldevcontractv1.ComputeState_COMPUTE_STATE_SUCCEEDED},
		{name: "close state maps to the owner enum", in: &modeldevv1.ListExecutionsRequest{CloseState: text("NEEDS_REVIEW")}, wantClose: modeldevcontractv1.CloseState_CLOSE_STATE_NEEDS_REVIEW},
		{name: "lower case is rejected", in: &modeldevv1.ListExecutionsRequest{ComputeState: text("succeeded")}, wantErr: true},
		{name: "unknown member is rejected", in: &modeldevv1.ListExecutionsRequest{ComputeState: text("DONE")}, wantErr: true},
		{name: "the unspecified placeholder is rejected", in: &modeldevv1.ListExecutionsRequest{ComputeState: text("UNSPECIFIED")}, wantErr: true},
		{name: "empty value is rejected", in: &modeldevv1.ListExecutionsRequest{DeliveryState: text("")}, wantErr: true},
		{name: "a member of another enum is rejected", in: &modeldevv1.ListExecutionsRequest{DeliveryState: text("SUCCEEDED")}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := modelDevExecutionFilter(test.in)
			if test.wantErr {
				if err == nil {
					t.Fatalf("accepted %+v", test.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("rejected valid filter: %v", err)
			}
			if test.wantNil {
				if got.ComputeState != nil || got.DeliveryState != nil || got.CloseState != nil {
					t.Fatalf("unexpected filter %+v", got)
				}
				return
			}
			if test.wantCode != 0 && (got.ComputeState == nil || *got.ComputeState != test.wantCode) {
				t.Fatalf("compute=%v", got.ComputeState)
			}
			if test.wantClose != 0 && (got.CloseState == nil || *got.CloseState != test.wantClose) {
				t.Fatalf("close=%v", got.CloseState)
			}
		})
	}
}
