package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	view "go-wind-admin/api/gen/go/vm/service/v1"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

func newTestLogger(t *testing.T) (bLogger.Logger, *bytes.Buffer) {
	t.Helper()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	buf := &bytes.Buffer{}
	// Use the real production handler so the assertions cover trace injection
	// and the contract field rules, not just the filter.
	return bLogger.NewJSONLogger(buf, slog.LevelDebug), buf
}

func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad JSON line: %v\n%s", err, line)
		}
		out = append(out, m)
	}
	return out
}

func findEvent(recs []map[string]any, event string) map[string]any {
	for _, r := range recs {
		if r["event"] == event {
			return r
		}
	}
	return nil
}

func TestHTTPFilterSetsTraceHeaderAndRecordsCompletion(t *testing.T) {
	logger, buf := newTestLogger(t)
	h := httpObservabilityFilter(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vms", nil)
	h.ServeHTTP(rec, req)

	traceID := rec.Header().Get(HeaderTraceID)
	if len(traceID) != 32 {
		t.Fatalf("X-Trace-ID = %q, want a 32-hex trace", traceID)
	}

	recs := records(t, buf)
	started := findEvent(recs, "http.request.started")
	done := findEvent(recs, "http.request.completed")
	if started == nil || done == nil {
		t.Fatalf("missing started/completed records: %+v", recs)
	}
	if done["http.status_code"].(float64) != 201 {
		t.Errorf("status = %v, want 201", done["http.status_code"])
	}
	if done["result"] != "succeeded" {
		t.Errorf("result = %v, want succeeded", done["result"])
	}
	if done["trace_id"] != traceID {
		t.Errorf("log trace %v != header trace %s", done["trace_id"], traceID)
	}
	if done["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", done["level"])
	}
}

func TestHTTPFilterInfers200WhenHandlerWritesNothing(t *testing.T) {
	logger, buf := newTestLogger(t)
	h := httpObservabilityFilter(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	done := findEvent(records(t, buf), "http.request.completed")
	if done == nil || done["http.status_code"].(float64) != 200 {
		t.Fatalf("expected implied 200, got %+v", done)
	}
}

func TestHTTPFilterLevelsFollowStatus(t *testing.T) {
	cases := []struct {
		status int
		level  string
		result string
	}{
		{http.StatusOK, "INFO", "succeeded"},
		{http.StatusBadRequest, "WARN", "rejected"},
		{http.StatusUnauthorized, "WARN", "rejected"},
		{http.StatusNotFound, "WARN", "rejected"},
		{http.StatusInternalServerError, "ERROR", "failed"},
	}
	for _, c := range cases {
		logger, buf := newTestLogger(t)
		code := c.status
		h := httpObservabilityFilter(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		done := findEvent(records(t, buf), "http.request.completed")
		if done == nil {
			t.Fatalf("no completion for %d", c.status)
		}
		if done["level"] != c.level || done["result"] != c.result {
			t.Errorf("status %d: level=%v result=%v, want %s/%s", c.status, done["level"], done["result"], c.level, c.result)
		}
	}
}

func TestHTTPFilterHealthPathIsDebug(t *testing.T) {
	logger, buf := newTestLogger(t)
	h := httpObservabilityFilter(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	done := findEvent(records(t, buf), "http.request.completed")
	if done == nil || done["level"] != "DEBUG" {
		t.Fatalf("health completion should be DEBUG: %+v", done)
	}
}

func TestHTTPFilterRecords404And405(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
	}{{"not_found", http.StatusNotFound}, {"method_not_allowed", http.StatusMethodNotAllowed}} {
		logger, buf := newTestLogger(t)
		h := httpObservabilityFilter(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/missing", nil))
		if rec.Header().Get(HeaderTraceID) == "" {
			t.Fatalf("%s: missing X-Trace-ID", c.name)
		}
		if findEvent(records(t, buf), "http.request.completed") == nil {
			t.Fatalf("%s: missing completion record", c.name)
		}
	}
}

func TestHTTPFilterRecoversPanicWithStack(t *testing.T) {
	logger, buf := newTestLogger(t)
	h := httpObservabilityFilter(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	panicRec := findEvent(records(t, buf), "process.panic")
	if panicRec == nil {
		t.Fatalf("missing process.panic record")
	}
	if stack, _ := panicRec["stack"].(string); !strings.Contains(stack, "goroutine") {
		t.Errorf("panic record lacks a real stack: %v", panicRec["stack"])
	}
	if panicRec["level"] != "ERROR" {
		t.Errorf("panic level = %v", panicRec["level"])
	}
}

func TestHTTPFilterContinuesInboundTrace(t *testing.T) {
	logger, _ := newTestLogger(t)
	h := httpObservabilityFilter(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get(HeaderTraceID); got != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("X-Trace-ID = %s, want the inbound trace", got)
	}
}

func TestCaptureHTTPSavesFirstError(t *testing.T) {
	logger, _ := newTestLogger(t)
	first := context.Canceled
	var captured error
	h := httpObservabilityFilter(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captureHTTPError(r.Context(), first)
		captureHTTPError(r.Context(), http.ErrHandlerTimeout) // must not overwrite
		captured = captureFrom(r.Context()).err
		w.WriteHeader(http.StatusInternalServerError)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	if captured != first {
		t.Fatalf("capture overwrote the first error: %v", captured)
	}
}

func traceCtx(t *testing.T, first byte) context.Context {
	t.Helper()
	var traceID trace.TraceID
	var spanID trace.SpanID
	for i := range traceID {
		traceID[i] = first
	}
	for i := range spanID {
		spanID[i] = first
	}
	return trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
}

func TestRecoveryHandlerLogsPanicWithoutRequest(t *testing.T) {
	logger, buf := newTestLogger(t)
	ctx := traceCtx(t, 0x11)
	handler := recoveryMiddleware(logger)(func(context.Context, any) (any, error) { panic("kaboom") })

	_, err := handler(ctx, &struct{ Password string }{Password: "super-secret"})
	if err == nil {
		t.Fatal("recovery returned no error")
	}
	if code := errors.FromError(err).Code; code != 500 {
		t.Fatalf("code = %d, want the existing 500", code)
	}
	rec := findEvent(records(t, buf), "process.panic")
	if rec == nil {
		t.Fatalf("missing process.panic: %s", buf.String())
	}
	if rec["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR", rec["level"])
	}
	if got := rec["trace_id"]; got != "11111111111111111111111111111111" {
		t.Errorf("trace_id = %v, want the request trace", got)
	}
	if stack, _ := rec["stack"].(string); !strings.Contains(stack, "goroutine") {
		t.Errorf("panic record lacks a real stack: %v", rec["stack"])
	}
	if msg, _ := rec["error"].(string); !strings.Contains(msg, "kaboom") {
		t.Errorf("error = %v, want the panic value", rec["error"])
	}
	if strings.Contains(buf.String(), "super-secret") {
		t.Errorf("panic log serialized the decoded request: %s", buf.String())
	}
}

func TestTraceErrorEncoderClonesSharedError(t *testing.T) {
	shared := errors.InternalServer("UNKNOWN", "unknown request error")
	var seen []*errors.Error
	encoder := traceErrorEncoder(func(_ http.ResponseWriter, _ *http.Request, err error) {
		seen = append(seen, errors.FromError(err))
	})
	for _, first := range []byte{0x21, 0x22} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(traceCtx(t, first))
		encoder(httptest.NewRecorder(), req, shared)
	}
	if len(seen) != 2 || seen[0] == seen[1] {
		t.Fatalf("encoder received a shared error pointer: %+v", seen)
	}
	if seen[0].Metadata["trace_id"] == seen[1].Metadata["trace_id"] {
		t.Errorf("both responses carry the same trace: %v", seen[0].Metadata)
	}
	if len(shared.Metadata) != 0 {
		t.Errorf("shared error metadata was mutated: %v", shared.Metadata)
	}
}

func TestTraceErrorEncoderAttachesTraceToPlainError(t *testing.T) {
	var got *errors.Error
	encoder := traceErrorEncoder(func(_ http.ResponseWriter, _ *http.Request, err error) {
		got = errors.FromError(err)
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(traceCtx(t, 0x31))
	encoder(httptest.NewRecorder(), req, errors.New(500, "BOOM", "plain failure"))
	if got == nil || got.Metadata["trace_id"] != "31313131313131313131313131313131" {
		t.Fatalf("encoded error lacks the current trace: %+v", got)
	}
}

func TestDecodeQueryFailureIsCapturedWithoutValues(t *testing.T) {
	logger, buf := newTestLogger(t)
	h := httpObservabilityFilter(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := decodeInstanceQuery(r, new(view.ListVMsRequest)); err != nil {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/virtual-machines?state=bogus", nil))

	done := findEvent(records(t, buf), "http.request.completed")
	if done == nil {
		t.Fatal("missing completion record")
	}
	msg, _ := done["error"].(string)
	if !strings.Contains(msg, `"state"`) {
		t.Errorf("error = %q, want the failing query parameter", msg)
	}
	if strings.Contains(msg, "bogus") {
		t.Errorf("error = %q, must not log the query value", msg)
	}
}

func TestDecodeJSONFailureIsCapturedWithoutBody(t *testing.T) {
	logger, buf := newTestLogger(t)
	h := httpObservabilityFilter(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := decodeStrictJSONHTTP(r, new(view.CreateVMRequest)); err != nil {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	body := `{"data":{"name":"` + strings.Repeat("secret-", 20)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/virtual-machines", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), req)

	done := findEvent(records(t, buf), "http.request.completed")
	if done == nil {
		t.Fatal("missing completion record")
	}
	msg, _ := done["error"].(string)
	if !strings.Contains(msg, "invalid JSON body") {
		t.Errorf("error = %q, want the JSON parse cause", msg)
	}
	if strings.Contains(msg, "secret-") {
		t.Errorf("error = %q, must not log the request body", msg)
	}
}

func TestFlushAndUnwrapPreserved(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &observingResponseWriter{ResponseWriter: rec, capture: &httpRequestCapture{}}
	rw.WriteHeader(http.StatusOK)
	rw.Flush()
	if !rec.Flushed {
		t.Error("Flush did not reach the underlying writer")
	}
	if rw.Unwrap() != rec {
		t.Error("Unwrap did not return the original writer")
	}
}
