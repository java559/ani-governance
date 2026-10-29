package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const executionLogID = "11111111-2222-4333-8444-555555555555"

func TestExecutionLogStreamRejectsUnboundedQuery(t *testing.T) {
	for _, query := range []string{"sinceBytes=-1", "sinceBytes=01", "sinceBytes=" + strings.Repeat("9", 12), "pod_uid=bad", "container=Node", "previous=1", "logId=x", "since_bytes=1", "pod_uid=" + executionLogID + "&podUid=" + executionLogID} {
		if _, err := decodeExecutionLogStreamQuery(httptest.NewRequest(http.MethodGet, "/logs/stream?"+query, nil)); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
	// The implicit Pod fallback and the documented camel-case alias are resolved
	// after authentication, not rejected as unknown selectors.
	parsed, err := decodeExecutionLogStreamQuery(httptest.NewRequest(http.MethodGet, "/logs/stream", nil))
	if err != nil || parsed.GetPodUid() != "" {
		t.Fatalf("implicit pod: %+v %v", parsed, err)
	}
	aliased, err := decodeExecutionLogStreamQuery(httptest.NewRequest(http.MethodGet, "/logs/stream?podUid="+executionLogID, nil))
	if err != nil || aliased.GetPodUid() != executionLogID {
		t.Fatalf("podUid alias: %+v %v", aliased, err)
	}
	parsed, err = decodeExecutionLogStreamQuery(httptest.NewRequest(http.MethodGet, "/logs/stream?pod_uid="+executionLogID+"&sinceBytes=42&container=main&previous=true", nil))
	if err != nil || parsed.GetPodUid() != executionLogID || parsed.GetSinceBytes() != 42 || parsed.GetContainer() != "main" || !parsed.GetPrevious() {
		t.Fatalf("options: %+v %v", parsed, err)
	}
}

// Only reviewed control-frame reasons leave the BFF; an upstream string never
// reaches the client verbatim.
func TestExecutionLogReasonAllowList(t *testing.T) {
	for _, reason := range []string{"OFFSET_INVALID", "PREVIOUS_UNAVAILABLE", "POD_GONE", "STEP_LOGS_UNAVAILABLE", "LOG_UNAVAILABLE"} {
		if executionLogReason(reason) != reason {
			t.Fatalf("allowed reason %s was rewritten", reason)
		}
	}
	if executionLogReason("private/internal detail") != "LOG_UNAVAILABLE" {
		t.Fatal("unknown reason leaked")
	}
	for _, reason := range []string{"LOG_ENDED", "LIFETIME_EXCEEDED", "POD_GONE"} {
		if executionLogClosedReason(reason) != reason {
			t.Fatalf("allowed close reason %s was rewritten", reason)
		}
	}
	if executionLogClosedReason("secret") != "LOG_ENDED" {
		t.Fatal("unknown close reason leaked")
	}
}

func TestExecutionLogOpenErrorSanitizesUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		in   error
		code codes.Code
	}{
		{status.Error(codes.NotFound, "private pod name"), codes.NotFound},
		{status.Error(codes.PermissionDenied, "private"), codes.NotFound},
		{status.Error(codes.FailedPrecondition, "OFFSET_INVALID"), codes.FailedPrecondition},
		{status.Error(codes.InvalidArgument, "private"), codes.InvalidArgument},
		{status.Error(codes.Unavailable, "private"), codes.Unavailable},
	} {
		got := executionLogOpenError(tc.in)
		if status.Code(got) != tc.code {
			t.Fatalf("code=%v want %v", status.Code(got), tc.code)
		}
		if strings.Contains(status.Convert(got).Message(), "private") {
			t.Fatalf("message leaked: %s", status.Convert(got).Message())
		}
	}
}

// The stream route sits behind the authentication chain; a denied caller never
// reaches the channel.
func TestExecutionLogStreamRequiresAuthentication(t *testing.T) {
	server := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(context.Context, any) (any, error) {
			return nil, status.Error(codes.Unauthenticated, "INVALID_LOGIN")
		}
	}))
	RegisterExecutionLogStreamHTTP(server, nil, DefaultInstanceLogLimits())
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	response, err := web.Client().Get(web.URL + "/api/v1/modeldev/executions/" + executionLogID + "/logs/stream?pod_uid=" + executionLogID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v", response.StatusCode, response.Header)
	}
}

// The training channel is gated per execution, not per instance.
func TestExecutionLogStreamConnectionLimit(t *testing.T) {
	limits := DefaultInstanceLogLimits()
	limits.ProcessConnections = 1
	limits.TenantConnections = 1
	limits.InstanceConnections = 1
	h := &executionLogHTTP{limits: limits, tenants: map[string]int{}, targets: map[string]int{}}
	release, err := h.acquire("tenant", "execution")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.acquire("tenant", "execution"); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second connection admitted: %v", err)
	}
	release()
	if _, err := h.acquire("tenant", "execution"); err != nil {
		t.Fatalf("release did not free the slot: %v", err)
	}
}
