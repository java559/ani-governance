package service

import (
	"context"
	"errors"
	"testing"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	monitoring "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/monitoring/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The window bounds are the source's own; a request outside them must fail
// before any owner or observability call.
func TestModelDevParseMetricQueryBounds(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-time.Hour)
	valid := func() *modeldevv1.QueryExecutionMetricsRequest {
		step := uint32(30)
		return &modeldevv1.QueryExecutionMetricsRequest{
			ExecutionId: "11111111-2222-4333-8444-555555555555",
			PodUid:      "66666666-7777-4888-8999-aaaaaaaaaaaa",
			Container:   "node",
			Start:       start.Format(time.RFC3339Nano),
			End:         now.Format(time.RFC3339Nano),
			StepSeconds: &step,
			MetricCodes: []string{"cpu.usage_cores", "memory.working_set_bytes"},
		}
	}
	for _, test := range []struct {
		name   string
		mutate func(*modeldevv1.QueryExecutionMetricsRequest)
		valid  bool
	}{
		{"worker window", func(*modeldevv1.QueryExecutionMetricsRequest) {}, true},
		{"missing pod uid", func(r *modeldevv1.QueryExecutionMetricsRequest) { r.PodUid = "" }, false},
		{"non-canonical pod uid", func(r *modeldevv1.QueryExecutionMetricsRequest) { r.PodUid = "ABC" }, false},
		{"uppercase container", func(r *modeldevv1.QueryExecutionMetricsRequest) { r.Container = "Node" }, false},
		{"unknown metric code", func(r *modeldevv1.QueryExecutionMetricsRequest) {
			r.MetricCodes = []string{"network.receive_bytes_per_second"}
		}, false},
		{"duplicate metric code", func(r *modeldevv1.QueryExecutionMetricsRequest) {
			r.MetricCodes = []string{"cpu.usage_cores", "cpu.usage_cores"}
		}, false},
		{"step below 30", func(r *modeldevv1.QueryExecutionMetricsRequest) {
			step := uint32(15)
			r.StepSeconds = &step
		}, false},
		{"step not multiple of 30", func(r *modeldevv1.QueryExecutionMetricsRequest) {
			step := uint32(45)
			r.StepSeconds = &step
		}, false},
		{"window above 24h", func(r *modeldevv1.QueryExecutionMetricsRequest) {
			r.Start = start.Add(-24 * time.Hour).Format(time.RFC3339Nano)
		}, false},
		{"end in the future", func(r *modeldevv1.QueryExecutionMetricsRequest) {
			r.End = now.Add(time.Minute).Format(time.RFC3339Nano)
		}, false},
		{"more than 3000 points", func(r *modeldevv1.QueryExecutionMetricsRequest) {
			step := uint32(30)
			r.StepSeconds = &step
			r.Start = now.Add(-25 * time.Hour).Format(time.RFC3339Nano)
		}, false},
		{"sub-second start", func(r *modeldevv1.QueryExecutionMetricsRequest) {
			r.Start = start.Add(500 * time.Millisecond).Format(time.RFC3339Nano)
		}, false},
		{"end before start", func(r *modeldevv1.QueryExecutionMetricsRequest) {
			r.End = start.Add(-time.Minute).Format(time.RFC3339Nano)
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := valid()
			test.mutate(request)
			_, err := modelDevParseMetricQuery(request)
			if test.valid != (err == nil) {
				t.Fatalf("valid=%v, error=%v", test.valid, err)
			}
		})
	}
}

func TestModelDevMetricContainerPinsLayerNames(t *testing.T) {
	for _, test := range []struct {
		layer modeldevcontractv1.PodLayer
		want  string
	}{
		{modeldevcontractv1.PodLayer_POD_LAYER_TRAINER, "node"},
		{modeldevcontractv1.PodLayer_POD_LAYER_PIPELINE, "main"},
		{modeldevcontractv1.PodLayer_POD_LAYER_UNSPECIFIED, ""},
	} {
		got, ok := modelDevMetricContainer(test.layer)
		if ok != (test.want != "") || got != test.want {
			t.Fatalf("layer=%v got=%q ok=%v", test.layer, got, ok)
		}
	}
}

// A gap must stay a gap: the point carries a reason and never a fabricated
// zero, and the series status must agree with the number of real values.
func TestModelDevMetricSeriesReportsGaps(t *testing.T) {
	start := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	point := func(offset time.Duration, value *float64, reason monitoring.GapReason) *monitoring.MetricPoint {
		return &monitoring.MetricPoint{Timestamp: timestamppb.New(start.Add(offset)), Value: value, GapReason: reason}
	}
	one, two := 1.5, 0.0
	reply := []*monitoring.MetricSeries{
		{MetricCode: "cpu.usage_cores", Unit: "cores", Status: monitoring.SeriesStatus_SERIES_STATUS_OK, Points: []*monitoring.MetricPoint{point(0, &one, 0), point(30*time.Second, &two, 0)}},
		{MetricCode: "memory.working_set_bytes", Unit: "bytes", Status: monitoring.SeriesStatus_SERIES_STATUS_PARTIAL, Points: []*monitoring.MetricPoint{point(0, nil, monitoring.GapReason_GAP_REASON_NO_DATA), point(30*time.Second, &one, 0)}},
	}
	out, err := modelDevMetricSeries(reply, []string{"cpu.usage_cores", "memory.working_set_bytes"}, start, 30, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].Status != "OK" || out[0].Points[1].GetValue() != "0" || out[0].Points[1].GapReason != nil {
		t.Fatalf("cpu series=%+v", out[0])
	}
	if out[1].Status != "PARTIAL" || out[1].Points[0].Value != nil || out[1].Points[0].GetGapReason() != "NO_DATA" {
		t.Fatalf("memory series=%+v", out[1])
	}
}

func TestModelDevMetricSeriesRejectsMismatchedReplies(t *testing.T) {
	start := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	value := 1.0
	aligned := timestamppb.New(start)
	for _, test := range []struct {
		name  string
		reply []*monitoring.MetricSeries
	}{
		{"missing series", nil},
		{"wrong unit", []*monitoring.MetricSeries{{MetricCode: "cpu.usage_cores", Unit: "bytes", Status: monitoring.SeriesStatus_SERIES_STATUS_OK, Points: []*monitoring.MetricPoint{{Timestamp: aligned, Value: &value}}}}},
		{"point count mismatch", []*monitoring.MetricSeries{{MetricCode: "cpu.usage_cores", Unit: "cores", Status: monitoring.SeriesStatus_SERIES_STATUS_OK, Points: nil}}},
		{"misaligned point", []*monitoring.MetricSeries{{MetricCode: "cpu.usage_cores", Unit: "cores", Status: monitoring.SeriesStatus_SERIES_STATUS_OK, Points: []*monitoring.MetricPoint{{Timestamp: timestamppb.New(start.Add(time.Second)), Value: &value}}}}},
		{"negative value", []*monitoring.MetricSeries{{MetricCode: "cpu.usage_cores", Unit: "cores", Status: monitoring.SeriesStatus_SERIES_STATUS_OK, Points: []*monitoring.MetricPoint{{Timestamp: aligned, Value: func() *float64 { v := -1.0; return &v }()}}}}},
		{"value with gap reason", []*monitoring.MetricSeries{{MetricCode: "cpu.usage_cores", Unit: "cores", Status: monitoring.SeriesStatus_SERIES_STATUS_OK, Points: []*monitoring.MetricPoint{{Timestamp: aligned, Value: &value, GapReason: monitoring.GapReason_GAP_REASON_NO_DATA}}}}},
		{"status disagrees", []*monitoring.MetricSeries{{MetricCode: "cpu.usage_cores", Unit: "cores", Status: monitoring.SeriesStatus_SERIES_STATUS_PARTIAL, Points: []*monitoring.MetricPoint{{Timestamp: aligned, Value: &value}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := modelDevMetricSeries(test.reply, []string{"cpu.usage_cores"}, start, 30, 1); err == nil {
				t.Fatal("expected an invalid-response error")
			}
		})
	}
}

// The tenant gate queues instead of rejecting: four calls run, the fifth waits
// until one slot frees, and a cancelled waiter leaves without a slot.
func TestModelDevMetricGateQueuesPerTenant(t *testing.T) {
	var gate modelDevMetricGate
	ctx := context.Background()
	releases := make([]func(), 0, modelDevMetricTenantConcurrency)
	for i := 0; i < modelDevMetricTenantConcurrency; i++ {
		release, err := gate.acquire(ctx, 7)
		if err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
		releases = append(releases, release)
	}
	waiting, cancel := context.WithCancel(ctx)
	blocked := make(chan error, 1)
	go func() {
		_, err := gate.acquire(waiting, 7)
		blocked <- err
	}()
	select {
	case err := <-blocked:
		t.Fatalf("fifth call returned before a slot freed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	// Another tenant is not blocked by this one.
	otherRelease, err := gate.acquire(ctx, 8)
	if err != nil {
		t.Fatalf("other tenant: %v", err)
	}
	otherRelease()
	releases[0]()
	select {
	case err := <-blocked:
		if err != nil {
			t.Fatalf("waiter failed after a slot freed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter did not proceed after a slot freed")
	}
	cancel()
	for _, release := range releases[1:] {
		release()
	}
}

func TestModelDevMetricGateReleasesOnCancellation(t *testing.T) {
	var gate modelDevMetricGate
	for i := 0; i < modelDevMetricTenantConcurrency; i++ {
		if _, err := gate.acquire(context.Background(), 9); err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := gate.acquire(ctx, 9); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v, want deadline exceeded", err)
	}
}

// modelDevMetricFailure must keep the source's own distinctions instead of
// collapsing every failure into one unavailable answer.
func TestModelDevMetricFailureMapping(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code int32
	}{
		{"deadline", status.Error(codes.DeadlineExceeded, "slow"), 504},
		{"invalid", status.Error(codes.InvalidArgument, "bad"), 400},
		{"exhausted", status.Error(codes.ResourceExhausted, "busy"), 429},
		{"unavailable", status.Error(codes.Unavailable, "down"), 503},
		{"not found", status.Error(codes.NotFound, "gone"), 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			converted, ok := modelDevMetricFailure(test.err).(*kratoserrors.Error)
			if !ok || converted.Code != test.code {
				t.Fatalf("error=%v, want HTTP %d", converted, test.code)
			}
		})
	}
}
