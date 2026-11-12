// Package telemetry carries the phase-one logging context: the persisted W3C
// trace snapshot and the small set of business attributes bound once per
// operation.
//
// It deliberately contains no storage, no Kubernetes access and no logger
// registry: values are captured from the caller's context, saved next to the
// existing business record, and restored when a worker picks that record up.
package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HeaderTraceparent and HeaderTracestate are the fixed W3C names. They are only
// used to build/read a carrier, never to hand-assemble a header.
const (
	HeaderTraceparent = "traceparent"
	HeaderTracestate  = "tracestate"
)

// TelemetryContext is the persisted trace snapshot. It is an independent JSON
// column/attribute; it must never be folded into canonical payloads, hashes,
// idempotency keys or frozen specs.
type TelemetryContext struct {
	Traceparent   string `json:"traceparent,omitempty"`
	Tracestate    string `json:"tracestate,omitempty"`
	OriginTraceID string `json:"origin_trace_id,omitempty"`
}

// Valid reports whether a traceparent was captured. A record without one is
// treated as missing metadata by its worker, not as an empty trace.
func (t TelemetryContext) Valid() bool {
	return strings.TrimSpace(t.Traceparent) != ""
}

// Capture snapshots the current W3C trace context using the standard
// propagator. OriginTraceID is the trace of the span that accepted the
// operation; it is never regenerated later.
func Capture(ctx context.Context) TelemetryContext {
	if ctx == nil {
		return TelemetryContext{}
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	out := TelemetryContext{
		Traceparent: carrier.Get(HeaderTraceparent),
		Tracestate:  carrier.Get(HeaderTracestate),
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() && sc.TraceID().IsValid() {
		out.OriginTraceID = sc.TraceID().String()
	}
	return out
}

// Decode parses only the independent logging metadata. An empty column yields
// the zero value so the caller can distinguish missing metadata; a malformed
// value returns an error that the worker records and then ignores while it
// continues the original business work.
func Decode(raw json.RawMessage) (TelemetryContext, error) {
	var out TelemetryContext
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" || trimmed == "{}" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return TelemetryContext{}, fmt.Errorf("decode telemetry context: %w", err)
	}
	return out, nil
}

// Restore extracts the saved W3C trace into base. It restores span metadata
// only: the original HTTP deadline, cancellation, tenant, authorization and
// credentials are deliberately not carried over.
func Restore(base context.Context, saved TelemetryContext) (context.Context, error) {
	if base == nil {
		base = context.Background()
	}
	carrier := propagation.MapCarrier{}
	if saved.Traceparent != "" {
		carrier.Set(HeaderTraceparent, saved.Traceparent)
	}
	if saved.Tracestate != "" {
		carrier.Set(HeaderTracestate, saved.Tracestate)
	}
	ctx := otel.GetTextMapPropagator().Extract(base, carrier)
	if sc := trace.SpanContextFromContext(ctx); !sc.IsValid() {
		return base, errors.New("telemetry context does not contain a valid traceparent")
	}
	return ctx, nil
}

// StartRun opens the span for one background pass on top of base. The persisted
// trace was captured from a request span that has already ended, so reusing it
// would attribute every later pass to that request span; a child span keeps the
// same trace_id and gives this pass its own span_id. Without a parent the
// provider assigns a new trace, which keeps trace_id/span_id present for the
// required worker events.
func StartRun(base context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer("ani-governance/worker").Start(base, name)
}

// BusinessContext is the small, fixed set of business attributes bound once an
// operation has been accepted or claimed. Empty fields are omitted.
type BusinessContext struct {
	OperationID   string
	ResourceType  string
	ResourceID    string
	TenantID      string
	Action        string
	OriginTraceID string
}

type businessKey struct{}

// WithBusiness replaces the bound business attributes. A nested task must bind
// its own values rather than inherit the previous operation's leftovers.
func WithBusiness(ctx context.Context, value BusinessContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, businessKey{}, value)
}

// BusinessAttrs renders the bound attributes for a log record.
func BusinessAttrs(ctx context.Context) []slog.Attr {
	if ctx == nil {
		return nil
	}
	v, ok := ctx.Value(businessKey{}).(BusinessContext)
	if !ok {
		return nil
	}
	out := make([]slog.Attr, 0, 6)
	for _, f := range []struct{ key, value string }{
		{"operation_id", v.OperationID},
		{"resource_type", v.ResourceType},
		{"resource_id", v.ResourceID},
		{"tenant_id", v.TenantID},
		{"action", v.Action},
		{"origin_trace_id", v.OriginTraceID},
	} {
		if f.value != "" {
			out = append(out, slog.String(f.key, f.value))
		}
	}
	return out
}

// BusinessFrom returns the bound business attributes, or the zero value.
func BusinessFrom(ctx context.Context) BusinessContext {
	if ctx == nil {
		return BusinessContext{}
	}
	v, _ := ctx.Value(businessKey{}).(BusinessContext)
	return v
}
