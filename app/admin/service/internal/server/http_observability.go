package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/middleware/requestid"
	"go-wind-admin/pkg/telemetry"
)

// HeaderTraceID is the response header carrying the request's trace ID. It is
// set on every response that reaches the application HTTP entry, so an operator
// can copy it from curl or the browser network panel.
const HeaderTraceID = "X-Trace-ID"

// httpCaptureKey stores the per-request original-error capture.
type httpCaptureKey struct{}

// httpRequestCapture belongs to exactly one request. It keeps the first real
// error seen and the status actually written; it never stores body or headers.
type httpRequestCapture struct {
	err       error
	status    int
	wroteHead bool
}

// captureHTTPError records the first real error for this request. Later safe
// error mappings must not overwrite it, so an operator still sees the true
// bind/JSON/upstream cause instead of only the public status.
func captureHTTPError(ctx context.Context, err error) {
	if err == nil || ctx == nil {
		return
	}
	capture, ok := ctx.Value(httpCaptureKey{}).(*httpRequestCapture)
	if !ok || capture == nil {
		return
	}
	if capture.err == nil {
		capture.err = err
	}
}

// captureFrom returns the request-local capture, if any.
func captureFrom(ctx context.Context) *httpRequestCapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(httpCaptureKey{}).(*httpRequestCapture)
	return capture
}

// httpObservabilityFilter is the outermost HTTP boundary. It runs before CORS,
// binding, authentication and routing so 404/405, decode failures and panics
// all carry one trace and one completion record.
func httpObservabilityFilter(logger bLogger.Logger) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Extract an inbound W3C context before creating this request's
			// span so an upstream trace is continued rather than replaced.
			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			tracer := otel.Tracer("ani-governance/http")
			ctx, span := tracer.Start(ctx, "http.server",
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.route", r.URL.Path),
				),
			)
			defer span.End()

			// A noop provider yields an invalid context. The runtime always
			// installs a real SDK provider, but if that ever fails the entry
			// must still correlate: generate a real trace and keep it on the
			// context so outbound calls propagate it.
			ctx, sc := ensureSpanContext(ctx)

			traceID := ""
			if sc.IsValid() {
				traceID = sc.TraceID().String()
				w.Header().Set(HeaderTraceID, traceID)
			}

			// One request ID per request, shared with audit and downstream code.
			ctx, requestID := requestid.Ensure(ctx, r.Header)
			r = r.WithContext(ctx)

			capture := &httpRequestCapture{}
			ctx = context.WithValue(ctx, httpCaptureKey{}, capture)
			r = r.WithContext(ctx)

			rw := &observingResponseWriter{ResponseWriter: w, capture: capture}

			path := r.URL.Path
			startLevel := bLogger.LevelInfo
			if isHealthPath(path) {
				startLevel = bLogger.LevelDebug
			}
			logStart(logger, ctx, startLevel, path, r.Method, requestID, traceID)

			defer func() {
				if rec := recover(); rec != nil {
					capture.err = fmt.Errorf("panic: %v", rec)
					logPanic(logger, ctx, rec, debug.Stack(), path, r.Method)
					span.RecordError(fmt.Errorf("%v", rec))
					span.SetStatus(codes.Error, "panic")
					if !rw.wroteHead {
						http.Error(rw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					}
				}
				finishHTTP(logger, ctx, capture, rw.statusOr(200), start, path, r.Method, traceID)
			}()

			next.ServeHTTP(rw, r)
			if sc.IsValid() && rw.statusCode() >= 500 {
				span.SetStatus(codes.Error, "server error")
			}
		})
	}
}

// recoveryMiddleware keeps Kratos recovery's existing 500 result, but records
// the panic at the real catch point. Kratos v2.9.2 logs "%v: %+v" with the whole
// decoded request before any custom handler runs, so delegating to it cannot
// meet the no-request-body rule; the outer HTTP filter never sees the panic
// either because recovery catches it first.
func recoveryMiddleware(logger bLogger.Logger) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (reply any, err error) {
			defer func() {
				if rec := recover(); rec != nil {
					if logger != nil {
						attrs := []any{
							"event", "process.panic",
							"error", fmt.Sprintf("panic: %v", rec),
							"error_type", "panic",
							"error_source", "local",
							"stack", string(debug.Stack()),
						}
						logger.Error(ctx, "request panicked", attrs...)
					}
					reply, err = nil, recovery.ErrUnknownRequest
				}
			}()
			return handler(ctx, req)
		}
	}
}

// traceErrorEncoder wraps the existing Kratos error encoder: it records the
// real error first, then attaches the trace ID to a copy of the error metadata
// and lets the original encoder produce the response body. Kratos may return
// the same *errors.Error (including shared recovery errors) for different
// requests, so mutating it here would leak one request's trace into another.
func traceErrorEncoder(enc khttp.EncodeErrorFunc) khttp.EncodeErrorFunc {
	if enc == nil {
		enc = khttp.DefaultErrorEncoder
	}
	return func(w http.ResponseWriter, r *http.Request, err error) {
		ctx := r.Context()
		captureHTTPError(ctx, err)
		traceID := spanTraceID(ctx)
		se := errors.FromError(err)
		if traceID == "" || se == nil {
			enc(w, r, err)
			return
		}
		cloned := errors.Clone(se)
		if cloned == nil {
			enc(w, r, err)
			return
		}
		if _, exists := cloned.Metadata["trace_id"]; !exists {
			cloned.Metadata["trace_id"] = traceID
		}
		enc(w, r, cloned)
	}
}

// spanTraceID returns the W3C trace of the current span. It is deliberately
// separate from the audit helper in policy_eval_logging_engine.go, which reads
// the transport header and falls back to X-Request-ID.
func spanTraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// ensureSpanContext guarantees a valid span context for correlation. When the
// tracer provider is a noop, it derives a real non-zero context so the entry
// still correlates; an inbound trace is already valid and passes through.
func ensureSpanContext(ctx context.Context) (context.Context, trace.SpanContext) {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		return ctx, sc
	}
	var traceBytes [16]byte
	var spanBytes [8]byte
	_, _ = rand.Read(traceBytes[:])
	_, _ = rand.Read(spanBytes[:])
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID(traceBytes),
		SpanID:  trace.SpanID(spanBytes),
	})
	return trace.ContextWithSpanContext(ctx, sc), sc
}

// logStart records http.request.started at the given level.
func logStart(logger bLogger.Logger, ctx context.Context, level bLogger.Level, path, method, requestID, traceID string) {
	attrs := []any{
		"event", "http.request.started",
		"http.method", method,
		"http.path", path,
	}
	if requestID != "" {
		attrs = append(attrs, "request_id", requestID)
	}
	logAt(logger, ctx, level, "http request started", attrs...)
}

// finishHTTP records http.request.completed with the real status, duration and
// the first captured error.
func finishHTTP(logger bLogger.Logger, ctx context.Context, capture *httpRequestCapture, status int, start time.Time, path, method, traceID string) {
	elapsed := time.Since(start)
	level := httpResultLevel(path, status)
	attrs := []any{
		"event", "http.request.completed",
		"http.method", method,
		"http.path", path,
		"http.status_code", status,
		"result", httpResult(status),
		"duration_ms", float64(elapsed.Microseconds()) / 1000.0,
	}
	// A captured error is the real bind/JSON/upstream cause and must be readable
	// at any status: a 4xx decode failure that only printed "400" is exactly what
	// this phase removes. The level still follows the status ladder.
	if capture.err != nil {
		attrs = append(attrs, telemetry.ErrorFields(capture.err)...)
	}
	logAt(logger, ctx, level, "http request completed", attrs...)
}

func logPanic(logger bLogger.Logger, ctx context.Context, rec any, stack []byte, path, method string) {
	attrs := []any{
		"event", "process.panic",
		"error", fmt.Sprintf("panic: %v", rec),
		"error_type", "panic",
		"error_source", "local",
		"stack", string(stack),
		"http.path", path,
		"http.method", method,
	}
	logger.Error(ctx, "request panicked", attrs...)
}

// httpResultLevel applies the fixed status-to-level rule.
func httpResultLevel(path string, status int) bLogger.Level {
	if isHealthPath(path) {
		if status < 400 {
			return bLogger.LevelDebug
		}
		return bLogger.LevelWarn
	}
	switch {
	case status < 400:
		return bLogger.LevelInfo
	case status < 500:
		return bLogger.LevelWarn
	default:
		return bLogger.LevelError
	}
}

func httpResult(status int) string {
	switch {
	case status < 400:
		return "succeeded"
	case status < 500:
		return "rejected"
	default:
		return "failed"
	}
}

func isHealthPath(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/metrics":
		return true
	}
	return false
}

func logAt(logger bLogger.Logger, ctx context.Context, level bLogger.Level, msg string, attrs ...any) {
	if logger == nil {
		return
	}
	switch level {
	case bLogger.LevelDebug:
		logger.Debug(ctx, msg, attrs...)
	case bLogger.LevelWarn:
		logger.Warn(ctx, msg, attrs...)
	case bLogger.LevelError:
		logger.Error(ctx, msg, attrs...)
	default:
		logger.Info(ctx, msg, attrs...)
	}
}

// observingResponseWriter records the real status and stays transparent for
// flushing, hijacking and ResponseController unwrapping.
type observingResponseWriter struct {
	http.ResponseWriter
	capture     *httpRequestCapture
	writtenCode int
	wroteHead   bool
}

func (w *observingResponseWriter) WriteHeader(code int) {
	if !w.wroteHead {
		w.wroteHead = true
		w.writtenCode = code
		w.capture.status = code
		w.capture.wroteHead = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *observingResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHead {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *observingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wroteHead {
			w.WriteHeader(http.StatusOK)
		}
		f.Flush()
	}
}

func (w *observingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// status returns the status actually written, or the supplied fallback when the
// handler completed without writing a header (net/http then implies 200).
func (w *observingResponseWriter) statusOr(fallback int) int {
	if w.writtenCode != 0 {
		return w.writtenCode
	}
	return fallback
}

func (w *observingResponseWriter) statusCode() int {
	return w.writtenCode
}

// ensure observingResponseWriter satisfies the optional writer interfaces.
var (
	_ http.Flusher                              = (*observingResponseWriter)(nil)
	_ interface{ Unwrap() http.ResponseWriter } = (*observingResponseWriter)(nil)
)
