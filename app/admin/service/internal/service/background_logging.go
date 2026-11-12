package service

import (
	"context"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/trace"

	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/telemetry"
)

// Gov background lanes share one structured entry so every worker emits the
// same registered event fields instead of free text. Errors always pass through
// telemetry.ErrorFields, which unwraps the real cause before redaction.

// reportedTelemetry holds the record identities whose missing/malformed trace has
// already been reported. The contract requires the event once per record, not
// once per claim: a stuck record is re-claimed for as long as its operation
// stays open, so an unguarded ERROR would repeat every poll and bury the real
// failure. Insertion-ordered bounded set: when it fills, the oldest half is
// dropped, so a long-running process cannot grow it without limit.
var reportedTelemetry = struct {
	sync.Mutex
	seen  map[string]struct{}
	order []string
	limit int
}{seen: map[string]struct{}{}, limit: 4096}

// shouldReportTelemetry reports true the first time this event is recorded for
// the given identity, and false on every later poll of the same record.
func shouldReportTelemetry(event, identity string) bool {
	key := event + "\x00" + identity
	reportedTelemetry.Lock()
	defer reportedTelemetry.Unlock()
	if _, dup := reportedTelemetry.seen[key]; dup {
		return false
	}
	reportedTelemetry.seen[key] = struct{}{}
	reportedTelemetry.order = append(reportedTelemetry.order, key)
	if len(reportedTelemetry.order) > reportedTelemetry.limit {
		drop := reportedTelemetry.limit / 2
		for _, old := range reportedTelemetry.order[:drop] {
			delete(reportedTelemetry.seen, old)
		}
		reportedTelemetry.order = append([]string(nil), reportedTelemetry.order[drop:]...)
	}
	return true
}

// identityKey renders the attrs slice into a stable dedup key.
func identityKey(attrs []any) string {
	var b strings.Builder
	for i := 0; i+1 < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if !ok {
			continue
		}
		value, ok := attrs[i+1].(string)
		if !ok || value == "" {
			continue
		}
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(value)
		b.WriteByte(';')
	}
	return b.String()
}

// backgroundIdentity is the fixed operation identity a Gov worker carries.
func backgroundIdentity(operationID, resourceType, resourceID, tenantID, action string) []any {
	out := []any{}
	if operationID != "" {
		out = append(out, "operation_id", operationID)
	}
	if resourceType != "" {
		out = append(out, "resource_type", resourceType)
	}
	if resourceID != "" {
		out = append(out, "resource_id", resourceID)
	}
	if tenantID != "" {
		out = append(out, "tenant_id", tenantID)
	}
	if action != "" {
		out = append(out, "action", action)
	}
	return out
}

// stepFailed records a real failure at the point it becomes durable state.
func stepFailed(ctx context.Context, log bLogger.Logger, step, reason string, attrs []any, err error) {
	if log == nil || err == nil {
		return
	}
	kv := append([]any{"event", "step.failed", "step", step, "result", "failed", "reason", reason}, attrs...)
	kv = append(kv, telemetry.ErrorFields(err)...)
	log.Error(ctx, "background step failed", kv...)
}

// retryScheduled records the existing backoff decision; it never adds a retry.
func retryScheduled(ctx context.Context, log bLogger.Logger, reason string, attrs []any, nextRetry string) {
	if log == nil {
		return
	}
	kv := append([]any{"event", "operation.retry_scheduled", "reason", reason}, attrs...)
	if nextRetry != "" {
		kv = append(kv, "next_retry", nextRetry)
	}
	log.Warn(ctx, "background retry scheduled", kv...)
}

// statePersistFailed records a failed write of worker state, distinct from the
// business error that preceded it.
func statePersistFailed(ctx context.Context, log bLogger.Logger, action, step string, attrs []any, err error) {
	if log == nil || err == nil {
		return
	}
	kv := append([]any{"event", "state.persist_failed", "action", action, "step", step}, attrs...)
	kv = append(kv, telemetry.ErrorFields(err)...)
	log.Error(ctx, "background state save failed", kv...)
}

// dependencyFailed records a real dependency (DB/RPC) failure outside a step.
func dependencyFailed(ctx context.Context, log bLogger.Logger, dependency, action string, attrs []any, err error) {
	if log == nil || err == nil {
		return
	}
	kv := append([]any{"event", "dependency.failed", "dependency", dependency, "action", action}, attrs...)
	kv = append(kv, telemetry.ErrorFields(err)...)
	log.Error(ctx, "background dependency failed", kv...)
}

// telemetryUnavailable records a missing/malformed persisted trace; the caller
// continues the business work on its own run trace.
func telemetryUnavailable(ctx context.Context, log bLogger.Logger, event string, attrs []any, err error) {
	if log == nil {
		return
	}
	if !shouldReportTelemetry(event, identityKey(attrs)) {
		return
	}
	kv := append([]any{"event", event}, attrs...)
	if event == "telemetry.context_invalid" {
		kv = append(kv, "reason", "TELEMETRY_CONTEXT_INVALID")
		if err != nil {
			kv = append(kv, telemetry.ErrorFields(err)...)
		}
	} else {
		kv = append(kv, "reason", "TELEMETRY_CONTEXT_MISSING")
	}
	log.Error(ctx, "background telemetry context unavailable", kv...)
}

// restoreBackgroundTrace restores a persisted trace into a fresh worker context
// and opens this pass's own span on it. A missing/invalid value is recorded once
// on that new span, so the ERROR still carries a valid trace_id/span_id, and the
// pass continues on a fresh trace. The caller must End the returned span.
func restoreBackgroundTrace(base context.Context, saved telemetry.TelemetryContext, decodeErr error, attrs []any, log bLogger.Logger, name string) (context.Context, trace.Span) {
	restored := base
	event, cause := "", error(nil)
	switch {
	case decodeErr != nil:
		event, cause = "telemetry.context_invalid", decodeErr
	case !saved.Valid():
		event = "telemetry.context_missing"
	default:
		next, e := telemetry.Restore(base, saved)
		if e != nil {
			event, cause = "telemetry.context_invalid", e
		} else {
			restored = next
		}
	}
	ctx, span := telemetry.StartRun(restored, name)
	if event != "" {
		telemetryUnavailable(ctx, log, event, attrs, cause)
	}
	return ctx, span
}

// decodeClaimTelemetry parses the raw persisted telemetry carried by a claimed
// record. A malformed value yields a decode error the caller reports once.
func decodeClaimTelemetry(raw []byte) (telemetry.TelemetryContext, error) {
	if len(raw) == 0 {
		return telemetry.TelemetryContext{}, nil
	}
	return telemetry.Decode(raw)
}

// decodeAcceptanceTelemetry parses the modeldev create-side raw telemetry.
func decodeAcceptanceTelemetry(raw []byte) telemetry.TelemetryContext {
	if len(raw) == 0 {
		return telemetry.TelemetryContext{}
	}
	tc, err := telemetry.Decode(raw)
	if err != nil {
		return telemetry.TelemetryContext{}
	}
	return tc
}
