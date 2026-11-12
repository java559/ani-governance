package logger

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"go-wind-admin/pkg/telemetry"
)

// RuntimeEvent is the framework fallback event name. Business code must never
// use it to stand in for a registered event.
const RuntimeEvent = "runtime.log"

// ParseRuntimeLevel converts ANI_LOG_LEVEL to a slog level.
//
// Only info and debug are accepted for the two runtime processes: WARN/ERROR
// thresholds and "off" would silently hide required INFO records.
func ParseRuntimeLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unsupported ANI_LOG_LEVEL %q: only info or debug are accepted", raw)
	}
}

// replaceContractAttr normalises the encoder-owned keys so every record uses
// the public contract shape: UTC RFC3339Nano time and an uppercase level.
func replaceContractAttr(_ []string, a slog.Attr) slog.Attr {
	switch a.Key {
	case slog.TimeKey:
		t, ok := a.Value.Any().(time.Time)
		if !ok {
			return a
		}
		return slog.String(slog.TimeKey, t.UTC().Format(time.RFC3339Nano))
	case slog.LevelKey:
		lv, ok := a.Value.Any().(slog.Level)
		if !ok {
			return a
		}
		return slog.String(slog.LevelKey, levelName(lv))
	}
	return a
}

// levelName renders a slog level using the contract vocabulary.
func levelName(lv slog.Level) string {
	switch {
	case lv < slog.LevelInfo:
		return "DEBUG"
	case lv < slog.LevelWarn:
		return "INFO"
	case lv < slog.LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
}

// contractHandler rewrites each record against the logging contract before it
// reaches the JSON encoder.
//
// It holds its own bound attributes instead of delegating WithAttrs to the base
// handler, so with-attrs and per-call attrs are filtered and de-duplicated in
// one place; the base handler stays pristine.
type contractHandler struct {
	base  slog.Handler
	attrs []slog.Attr
}

func (h *contractHandler) Enabled(ctx context.Context, lv slog.Level) bool {
	return h.base.Enabled(ctx, lv)
}

func (h *contractHandler) Handle(ctx context.Context, r slog.Record) error {
	combined := make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs()+8)
	combined = append(combined, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		combined = append(combined, a)
		return true
	})

	event := ""
	seen := make(map[string]struct{}, len(combined)+8)
	out := make([]slog.Attr, 0, len(combined)+8)
	for _, a := range combined {
		key, val, keep := normaliseAttr(a)
		if !keep {
			continue
		}
		if key == "event" {
			if s := val.Value.String(); s != "" {
				event = s
			}
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, slog.Attr{Key: key, Value: val.Value})
	}

	// Trace and business attributes are attached from the request context so
	// the caller never has to copy them by hand.
	if ctx != nil {
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			addIfMissing(&out, seen, "trace_id", sc.TraceID().String())
			addIfMissing(&out, seen, "span_id", sc.SpanID().String())
		}
		for _, a := range telemetry.BusinessAttrs(ctx) {
			addIfMissingAttr(&out, seen, a)
		}
	}

	if event == "" {
		event = RuntimeEvent
		addIfMissing(&out, seen, "event", event)
	}

	if event != RuntimeEvent {
		// A business event carries only registered fields; drop anything else
		// rather than emitting an unverified key.
		filtered := out[:0]
		for _, a := range out {
			if _, ok := businessFieldSet[a.Key]; ok {
				filtered = append(filtered, a)
			}
		}
		out = filtered
	}

	if source := sourceString(r.PC); source != "" {
		if _, dup := seen["source"]; !dup {
			out = append(out, slog.String("source", source))
		}
	}

	// The contract gives every string field a minimum length of one, so an
	// empty value is never valid. Kratos' request-logging middleware always
	// writes reason/stack, leaving them empty on success; drop those here
	// instead of emitting a record that fails validation.
	out = dropEmptyStrings(out)

	// The same middleware logs with an empty message. A record without a
	// readable message cannot be triaged, so compose one from the attributes
	// the framework did provide.
	message := r.Message
	if message == "" {
		message = fallbackMessage(event, out)
	}

	nr := slog.NewRecord(r.Time, r.Level, message, r.PC)
	nr.AddAttrs(out...)
	return h.base.Handle(ctx, nr)
}

// dropEmptyStrings removes attributes whose value is an empty string. Schema
// fields all require at least one character, so keeping them would only produce
// records the contract rejects.
func dropEmptyStrings(attrs []slog.Attr) []slog.Attr {
	kept := attrs[:0]
	for _, a := range attrs {
		if a.Value.Kind() == slog.KindString && a.Value.String() == "" {
			continue
		}
		kept = append(kept, a)
	}
	return kept
}

// fallbackMessage builds a readable message for a framework record that carried
// none. Component, operation and code are the stable facts a request log holds;
// when none exist the event name is the only honest description.
func fallbackMessage(event string, attrs []slog.Attr) string {
	value := func(key string) string {
		for _, a := range attrs {
			if a.Key == key {
				return a.Value.String()
			}
		}
		return ""
	}
	parts := make([]string, 0, 3)
	for _, key := range []string{"component", "operation", "code"} {
		if v := value(key); v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	if event != "" {
		return event
	}
	return "framework log"
}

func (h *contractHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	next = append(next, h.attrs...)
	next = append(next, attrs...)
	return &contractHandler{base: h.base, attrs: next}
}

func (h *contractHandler) WithGroup(string) slog.Handler {
	// Groups would nest keys under one object and break the flat contract.
	return h
}

// normaliseAttr maps framework aliases to their fixed public names and drops
// keys the encoder already owns.
func normaliseAttr(a slog.Attr) (string, slog.Attr, bool) {
	switch a.Key {
	case slog.TimeKey, slog.LevelKey, slog.MessageKey:
		return "", a, false
	case "ts", "timestamp":
		return "", a, false
	case "caller":
		return "source", a, true
	case "service.id", "service.instance":
		return "service.instance.id", a, true
	case "message":
		// "msg" is slog.MessageKey and already dropped above; this alias is a
		// duplicate message whose content the record already carries.
		return "", a, false
	}
	return a.Key, a, true
}

func addIfMissing(out *[]slog.Attr, seen map[string]struct{}, key, value string) {
	if value == "" {
		return
	}
	if _, dup := seen[key]; dup {
		return
	}
	seen[key] = struct{}{}
	*out = append(*out, slog.String(key, value))
}

func addIfMissingAttr(out *[]slog.Attr, seen map[string]struct{}, a slog.Attr) {
	if a.Key == "" {
		return
	}
	if _, dup := seen[a.Key]; dup {
		return
	}
	seen[a.Key] = struct{}{}
	*out = append(*out, a)
}

// sourceString renders a PC as the short file:line form used by `source`.
// sourceString renders the first caller frame that is not part of the logging
// implementation, so `source` points at the business call site instead of the
// handler's own PC (which is what slog.Record.PC holds here).
func sourceString(_ uintptr) string {
	pcs := make([]uintptr, 24)
	n := runtime.Callers(3, pcs)
	if n == 0 {
		return ""
	}
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if frame.File != "" && !isLoggingFrame(frame.Function) {
			return filepath.Base(frame.File) + ":" + itoa(frame.Line)
		}
		if !more {
			return ""
		}
	}
}

// isLoggingFrame reports frames that belong to the logging plumbing.
func isLoggingFrame(function string) bool {
	for _, marker := range []string{
		"log/slog.", "runtime.", "kratos-bootstrap/logger.",
		"github.com/go-kratos/kratos/v2/log.",
	} {
		if strings.Contains(function, marker) {
			return true
		}
	}
	return false
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// businessFieldSet is the registered field set from contracts/log.schema.json.
// Only these keys may appear on a business event.
var businessFieldSet = func() map[string]struct{} {
	fields := []string{
		"time", "level", "msg", "service.name", "service.version", "service.instance.id",
		"event", "trace_id", "span_id", "request_id", "operation_id", "resource_type",
		"resource_id", "tenant_id", "action", "step", "reason", "result",
		"origin_trace_id", "error", "error_type", "error_code", "error_source",
		"http.status_code", "rpc.code", "rpc.method", "peer.service", "target",
		"source", "stack", "worker", "dependency", "transport", "http.method",
		"http.route", "http.path", "verb", "ticket_id", "event_id",
		"release_event_id", "original_create_operation_id", "attachment_id",
		"submission_id", "binding_id", "command_id", "image_id", "vm_id",
		"mutation_id", "resource_version", "next_retry", "cluster_id",
		"connection_ref", "step_id", "group_id", "pool_id", "profile_id",
		"log_format", "log_level", "operation_state", "duration_ms", "delay_ms",
		"attempt", "epoch", "generation", "error_original_bytes",
		"response_bytes_observed", "error_unavailable", "error_truncated",
		"response_body_truncated", "response_started", "caller_canceled",
		"retryable",
	}
	out := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		out[f] = struct{}{}
	}
	return out
}()
