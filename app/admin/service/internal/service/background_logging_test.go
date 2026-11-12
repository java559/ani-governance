package service

import (
	"strconv"
	"testing"
)

// A stuck record is re-claimed on every poll, so the contract's "report once"
// for missing/malformed metadata must hold across passes, not just within one.
func TestTelemetryUnavailableReportsOncePerRecord(t *testing.T) {
	reportedTelemetry.Lock()
	reportedTelemetry.seen = map[string]struct{}{}
	reportedTelemetry.order = nil
	reportedTelemetry.Unlock()

	attrs := backgroundIdentity("op-dedup", "tenant_space", "res-dedup", "", "tenant_space.open")
	key := identityKey(attrs)
	// First pass reports; every later pass of the same record is suppressed.
	if !shouldReportTelemetry("telemetry.context_missing", key) {
		t.Fatal("first pass must report")
	}
	for i := 0; i < 4; i++ {
		if shouldReportTelemetry("telemetry.context_missing", key) {
			t.Fatalf("pass %d reported the same record again", i+2)
		}
	}
	// A different record is independent.
	other := backgroundIdentity("op-other", "tenant_space", "res-other", "", "tenant_space.open")
	if !shouldReportTelemetry("telemetry.context_missing", identityKey(other)) {
		t.Error("a different record must still be reported")
	}
	// A different event on the same record is independent too.
	if !shouldReportTelemetry("telemetry.context_invalid", identityKey(attrs)) {
		t.Error("a different event on the same record must be reported separately")
	}
}

// The guard must stay bounded so a long-running process cannot grow it forever.
func TestTelemetryReportGuardIsBounded(t *testing.T) {
	reportedTelemetry.Lock()
	reportedTelemetry.seen = map[string]struct{}{}
	reportedTelemetry.order = nil
	limit := reportedTelemetry.limit
	reportedTelemetry.Unlock()

	for i := 0; i < limit*3; i++ {
		shouldReportTelemetry("telemetry.context_missing", identityKey(backgroundIdentity("op-"+strconv.Itoa(i), "r", "x", "", "a")))
	}
	reportedTelemetry.Lock()
	defer reportedTelemetry.Unlock()
	if len(reportedTelemetry.order) > limit {
		t.Errorf("guard grew to %d, limit is %d", len(reportedTelemetry.order), limit)
	}
	if len(reportedTelemetry.seen) != len(reportedTelemetry.order) {
		t.Errorf("seen/order disagree: %d vs %d", len(reportedTelemetry.seen), len(reportedTelemetry.order))
	}
}
