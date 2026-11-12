package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkTrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func init() {
	otel.SetTextMapPropagator(propagation.TraceContext{})
}

func fields(err error) map[string]any {
	out := make(map[string]any)
	kv := ErrorFields(err)
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func TestErrorFieldsKubernetesForbidden(t *testing.T) {
	err := apierrors.NewForbidden(schema.GroupResource{Resource: "persistentvolumeclaims"}, "src-pvc", errors.New(`User "system:serviceaccount:ani:res" cannot get resource "persistentvolumeclaims"`))
	f := fields(err)
	if f["error_source"] != "kubernetes" {
		t.Errorf("error_source = %v", f["error_source"])
	}
	if f["error_code"] != "Forbidden" {
		t.Errorf("error_code = %v, want Forbidden", f["error_code"])
	}
	if !strings.Contains(f["error"].(string), "cannot get resource") {
		t.Errorf("error must keep the actionable message: %v", f["error"])
	}
	if f["error_type"] != "StatusError" {
		t.Errorf("error_type = %v", f["error_type"])
	}
}

func TestErrorFieldsPostgresSQLState(t *testing.T) {
	err := fmt.Errorf("insert release: %w", &pgconn.PgError{Code: "42501", Message: "permission denied for table quota_release_receipt"})
	f := fields(err)
	if f["error_source"] != "postgresql" {
		t.Errorf("error_source = %v", f["error_source"])
	}
	if f["error_code"] != "42501" {
		t.Errorf("error_code = %v, want 42501", f["error_code"])
	}
	if !strings.Contains(f["error"].(string), "permission denied for table") {
		t.Errorf("driver message lost: %v", f["error"])
	}
}

func TestErrorFieldsGRPCStatus(t *testing.T) {
	err := status.Error(codes.Unavailable, "connection refused")
	f := fields(err)
	if f["error_source"] != "grpc" {
		t.Errorf("error_source = %v", f["error_source"])
	}
	if f["error_code"] != "Unavailable" {
		t.Errorf("error_code = %v", f["error_code"])
	}
	if !strings.Contains(f["error"].(string), "connection refused") {
		t.Errorf("message lost: %v", f["error"])
	}
}

func TestErrorFieldsKratosReason(t *testing.T) {
	err := kerrors.BadRequest("QUOTA_EXCEEDED", "quota exceeded for tenant")
	f := fields(err)
	if f["error_code"] != "QUOTA_EXCEEDED" {
		t.Errorf("error_code = %v, want the kratos reason", f["error_code"])
	}
}

func TestRedactKeepsActionableContent(t *testing.T) {
	raw := `GET https://user:s3cr3t@bucket.example.com/obj failed; password=hunter2; Authorization: Bearer abcdefghijklmnop; state=S3_ACCESS_DENIED`
	got := Redact(raw)
	for _, secret := range []string{"s3cr3t", "hunter2", "abcdefghijklmnop"} {
		if strings.Contains(got, secret) {
			t.Errorf("secret %q leaked: %s", secret, got)
		}
	}
	// Actionable content must survive redaction.
	for _, keep := range []string{"bucket.example.com", "S3_ACCESS_DENIED", "state="} {
		if !strings.Contains(got, keep) {
			t.Errorf("redaction removed actionable text %q: %s", keep, got)
		}
	}
	if strings.Contains(got, "[redacted]") == false {
		t.Errorf("expected targeted redaction markers, got: %s", got)
	}
}

func TestErrorTruncation(t *testing.T) {
	huge := strings.Repeat("x", 40*1024)
	f := fields(errors.New(huge))
	if f["error_truncated"] != true {
		t.Errorf("error_truncated = %v", f["error_truncated"])
	}
	if f["error_original_bytes"].(int) != len(huge) {
		t.Errorf("error_original_bytes = %v, want %d", f["error_original_bytes"], len(huge))
	}
	if len(f["error"].(string)) > errorOutputCap+len(errorTruncatedOn) {
		t.Errorf("truncated error too long: %d", len(f["error"].(string)))
	}
}

func TestCaptureRestoreRoundTrip(t *testing.T) {
	traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	spanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	saved := Capture(ctx)
	if saved.OriginTraceID != traceID.String() {
		t.Fatalf("origin trace = %s, want %s", saved.OriginTraceID, traceID)
	}
	if saved.Traceparent == "" {
		t.Fatalf("traceparent not captured")
	}

	restored, err := Restore(context.Background(), saved)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := trace.SpanContextFromContext(restored).TraceID().String(); got != traceID.String() {
		t.Errorf("restored trace = %s, want %s", got, traceID)
	}
}

func TestRestoreRejectsInvalidTraceparent(t *testing.T) {
	_, err := Restore(context.Background(), TelemetryContext{Traceparent: "garbage"})
	if err == nil {
		t.Fatal("expected an error for a malformed traceparent")
	}
}

func TestDecodeBadMetadataIsIsolated(t *testing.T) {
	if _, err := Decode(json.RawMessage(`{"traceparent": 123}`)); err == nil {
		t.Fatal("expected decode error for a wrongly typed traceparent")
	}
	// Empty / absent columns are "missing", not an error.
	for _, raw := range []string{"", "null", "{}"} {
		got, err := Decode(json.RawMessage(raw))
		if err != nil {
			t.Errorf("Decode(%q) unexpected error: %v", raw, err)
		}
		if got.Valid() {
			t.Errorf("Decode(%q) should be invalid", raw)
		}
	}
}

func TestBusinessAttrsOmitEmpty(t *testing.T) {
	ctx := WithBusiness(context.Background(), BusinessContext{OperationID: "op-1"})
	attrs := BusinessAttrs(ctx)
	if len(attrs) != 1 || attrs[0].Key != "operation_id" {
		t.Fatalf("expected only operation_id, got %v", attrs)
	}
}

// A worker pass must keep the accepted request's trace_id but report its own
// span_id: the persisted traceparent belongs to a request span that already
// ended. This is the LG13/LG14 requirement, asserted at the helper.
func TestStartRunKeepsTraceAndOpensNewSpan(t *testing.T) {
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdkTrace.NewTracerProvider())
	defer otel.SetTracerProvider(previous)

	traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	requestSpanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	request := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: requestSpanID, TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), request)
	restored, err := Restore(context.Background(), Capture(ctx))
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	runCtx, span := StartRun(restored, "quota.dispatch")
	defer span.End()
	got := trace.SpanContextFromContext(runCtx)
	if got.TraceID() != traceID {
		t.Errorf("worker trace_id = %s, want the accepted trace %s", got.TraceID(), traceID)
	}
	if got.SpanID() == requestSpanID || !got.SpanID().IsValid() {
		t.Errorf("worker span_id = %s, want a new valid span distinct from the request span", got.SpanID())
	}
}

// A record without a usable persisted trace still gets a valid run span, so the
// required worker events keep trace_id/span_id.
func TestStartRunWithoutParentStillHasIdentity(t *testing.T) {
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdkTrace.NewTracerProvider())
	defer otel.SetTracerProvider(previous)

	runCtx, span := StartRun(context.Background(), "quota.dispatch")
	defer span.End()
	sc := trace.SpanContextFromContext(runCtx)
	if !sc.IsValid() || !sc.TraceID().IsValid() || !sc.SpanID().IsValid() {
		t.Fatalf("run span without a parent must still be valid, got %+v", sc)
	}
}
