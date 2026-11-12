package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkTrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/telemetry"
)

type spaceDispatchTestRepo struct {
	claim               *data.TenantSpaceClaim
	completed, deferred int
	permit              bool
}

func (r *spaceDispatchTestRepo) ClaimTenantSpace(context.Context, string, time.Duration) (*data.TenantSpaceClaim, error) {
	return r.claim, nil
}
func (r *spaceDispatchTestRepo) DeferTenantSpace(context.Context, *data.TenantSpaceClaim, time.Duration) (bool, error) {
	r.deferred++
	return r.permit, nil
}
func (r *spaceDispatchTestRepo) CompleteTenantSpace(context.Context, *data.TenantSpaceClaim) (bool, error) {
	r.completed++
	return r.permit, nil
}

type spaceDispatchTestRemote struct {
	result tenantSpaceResult
	err    error
}

func (r spaceDispatchTestRemote) OpenTenantSpace(context.Context, string, string) (tenantSpaceResult, error) {
	return r.result, r.err
}

func TestTenantSpaceDispatchRequiresMatchingReady(t *testing.T) {
	claim := &data.TenantSpaceClaim{TenantID: 1, ResourceTenantID: "tenant", OperationID: "operation"}
	for _, tc := range []struct {
		name                string
		result              tenantSpaceResult
		callErr             error
		permit, wantErr     bool
		completed, deferred int
	}{
		{"ready", tenantSpaceResult{"tenant", "operation", "ready"}, nil, true, false, 1, 0},
		{"open", tenantSpaceResult{"tenant", "operation", "open"}, nil, true, false, 0, 1},
		{"wrong tenant", tenantSpaceResult{"foreign", "operation", "ready"}, nil, true, true, 0, 1},
		{"wrong operation", tenantSpaceResult{"tenant", "other", "ready"}, nil, true, true, 0, 1},
		{"closed", tenantSpaceResult{"tenant", "operation", "closed"}, nil, true, true, 0, 1},
		{"unavailable", tenantSpaceResult{}, errors.New("unavailable"), true, true, 0, 1},
		{"stale lease", tenantSpaceResult{"tenant", "operation", "ready"}, nil, false, true, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &spaceDispatchTestRepo{claim: claim, permit: tc.permit}
			d := tenantSpaceDispatcher{repository: repo, remote: spaceDispatchTestRemote{tc.result, tc.callErr}, owner: "worker"}
			worked, err := d.step(context.Background())
			if !worked || (err != nil) != tc.wantErr || repo.completed != tc.completed || repo.deferred != tc.deferred {
				t.Fatalf("worked=%v err=%v completed=%d deferred=%d", worked, err, repo.completed, repo.deferred)
			}
		})
	}
}

// captureRemote records the trace the outbound call actually ran under.
type captureRemote struct {
	traceID, spanID string
}

func (r *captureRemote) OpenTenantSpace(ctx context.Context, tenant, operation string) (tenantSpaceResult, error) {
	sc := trace.SpanContextFromContext(ctx)
	r.traceID, r.spanID = sc.TraceID().String(), sc.SpanID().String()
	return tenantSpaceResult{tenant, operation, "ready"}, nil
}

// A background pass must resume the accepted trace but report its own span:
// reusing the request span would attribute the dispatch to a finished HTTP span.
func TestTenantSpaceDispatchResumesTraceWithNewSpan(t *testing.T) {
	previousProvider, previousPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdkTrace.NewTracerProvider())
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer func() { otel.SetTracerProvider(previousProvider); otel.SetTextMapPropagator(previousPropagator) }()

	traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	requestSpanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	request := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: requestSpanID, TraceFlags: trace.FlagsSampled})
	saved := telemetry.Capture(trace.ContextWithSpanContext(context.Background(), request))
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}

	claim := &data.TenantSpaceClaim{TenantID: 1, ResourceTenantID: "tenant", OperationID: "operation", TelemetryJSON: raw}
	remote := &captureRemote{}
	d := tenantSpaceDispatcher{repository: &spaceDispatchTestRepo{claim: claim, permit: true}, remote: remote, owner: "worker"}
	if _, err := d.step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if remote.traceID != traceID.String() {
		t.Errorf("dispatch trace_id = %s, want the accepted trace %s", remote.traceID, traceID)
	}
	if remote.spanID == "" || remote.spanID == requestSpanID.String() {
		t.Errorf("dispatch span_id = %q, want a new span distinct from the request span", remote.spanID)
	}
}
