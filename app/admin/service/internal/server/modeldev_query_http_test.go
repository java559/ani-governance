package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
)

// The deliberately denying middleware proves every public query enters the
// authentication chain before service work. It is not an authenticated-flow test.
func TestModelDevQueryRoutesEnterAuthentication(t *testing.T) {
	testModelDevAuthenticatedRoutes(t, []string{
		"/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555",
		"/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555/artifacts",
		"/api/v1/modeldev/artifacts/22222222-3333-4444-8555-666666666666/content",
	})
}

func TestModelDevListAndLogRejectUnboundedSelectors(t *testing.T) {
	server := khttp.NewServer()
	registerModelDevHTTP(server, nil)
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	for _, test := range []struct{ path, body string }{
		{"/api/v1/modeldev/executions?page_size=101", ""},
		{"/api/v1/modeldev/executions?page_size=0", ""},
		{"/api/v1/modeldev/executions?page_size=01", ""},
		{"/api/v1/modeldev/executions?page_size=1&page_size=2", ""},
		{"/api/v1/modeldev/executions?page_token=" + strings.Repeat("x", 2049), ""},
		{"/api/v1/modeldev/executions/id/artifacts?page_size=0", ""},
		{"/api/v1/modeldev/executions/id/artifacts?page_size=101", ""},
		{"/api/v1/modeldev/executions/id/artifacts?page_token=" + strings.Repeat("x", 2049), ""},
		{"/api/v1/modeldev/executions/id/artifacts?unknown=1", ""},
		{"/api/v1/modeldev/executions?id=1", "{}"},
		{"/api/v1/modeldev/executions?tenant_id=other", ""},
		// The three state selectors are declared, so the malformed shapes below
		// must still fail at this layer: lower case, empty, duplicated and
		// over-long values are all rejected rather than forwarded.
		{"/api/v1/modeldev/executions?compute_state=running", ""},
		{"/api/v1/modeldev/executions?compute_state=", ""},
		{"/api/v1/modeldev/executions?compute_state=SUCCEEDED&compute_state=FAILED", ""},
		{"/api/v1/modeldev/executions?delivery_state=published", ""},
		{"/api/v1/modeldev/executions?delivery_state=", ""},
		{"/api/v1/modeldev/executions?close_state=closed", ""},
		{"/api/v1/modeldev/executions?close_state=", ""},
		{"/api/v1/modeldev/executions?close_state=" + strings.Repeat("A", 49), ""},
		// The name selector is declared; malformed shapes must still fail here.
		{"/api/v1/modeldev/executions?name=", ""},
		{"/api/v1/modeldev/executions?name=%20%20", ""},
		{"/api/v1/modeldev/executions?name=" + strings.Repeat("x", 81), ""},
		{"/api/v1/modeldev/executions?name=a&name=b", ""},
		{"/api/v1/modeldev/executions?name=a&keyword=b", ""},
		// The kind selector is declared; malformed shapes must still fail here.
		{"/api/v1/modeldev/executions?kind=finetuning", ""},
		{"/api/v1/modeldev/executions?kind=", ""},
		{"/api/v1/modeldev/executions?kind=FINETUNING&kind=GENERAL_TRAINING", ""},
		{"/api/v1/modeldev/executions?unknown_state=SUCCEEDED", ""},
		{"/api/v1/modeldev/executions", "{}"},
		{"/api/v1/modeldev/executions/id/logs?tail_lines=1001", ""},
		{"/api/v1/modeldev/executions/id/logs?max_bytes=65537", ""},
		{"/api/v1/modeldev/executions/id/logs?tail_lines=0", ""},
		{"/api/v1/modeldev/executions/id/logs?max_bytes=-1", ""},
		{"/api/v1/modeldev/executions/id/logs?tail_lines=1&tail_lines=2", ""},
		{"/api/v1/modeldev/executions/id/logs?namespace=other", ""},
		{"/api/v1/modeldev/executions/id/logs?pod=other", ""},
		{"/api/v1/modeldev/executions/id/logs?log_id=other", ""},
		{"/api/v1/modeldev/executions/id/logs?follow=true", ""},
		{"/api/v1/modeldev/executions/id/logs?pod_uid=not-a-uuid", ""},
		{"/api/v1/modeldev/executions/id/logs?pod_uid=22222222-3333-4444-8555-666666666666&podUid=22222222-3333-4444-8555-666666666666", ""},
		{"/api/v1/modeldev/executions/id/logs?container=Node", ""},
		{"/api/v1/modeldev/executions/id/logs?container=", ""},
		{"/api/v1/modeldev/executions/id/logs?previous=1", ""},
		{"/api/v1/modeldev/executions/id/logs", "{}"},
		{"/api/v1/modeldev/executions/id/pods?layer=other", ""},
		{"/api/v1/modeldev/executions/id/pods?includeRecycled=yes", ""},
		{"/api/v1/modeldev/executions/id/pods?podUid=" + strings.Repeat("a", 36), ""},
		{"/api/v1/modeldev/executions/id/events?scope=other", ""},
		{"/api/v1/modeldev/executions/id/events?warning_only=true", ""},
		{"/api/v1/modeldev/executions/id/pods", "{}"},
		{"/api/v1/modeldev/executions/id/events", "{}"},
		{"/api/v1/modeldev/executions/id/resource-metrics?scope=other", ""},
		{"/api/v1/modeldev/executions/id/resource-metrics?step_seconds=0", ""},
		{"/api/v1/modeldev/executions/id/resource-metrics?step_seconds=abc", ""},
		{"/api/v1/modeldev/executions/id/resource-metrics?step_seconds=1&step_seconds=2", ""},
		{"/api/v1/modeldev/executions/id/resource-metrics?pod_uid=a&pod_uid=b", ""},
		{"/api/v1/modeldev/executions/id/resource-metrics", "{}"},
	} {
		t.Run(test.path, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, web.URL+test.path, strings.NewReader(test.body))
			require.NoError(t, err)
			response, err := web.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusBadRequest, response.StatusCode)
			require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		})
	}
}

func TestModelDevListAndLogRoutesEnterAuthentication(t *testing.T) {
	testModelDevAuthenticatedRoutes(t, []string{
		"/api/v1/modeldev/executions",
		"/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555/logs?pod_uid=22222222-3333-4444-8555-666666666666",
		// The documented camel-case alias and the implicit worker fallback must
		// reach authentication instead of being rejected as unknown selectors.
		"/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555/logs?podUid=22222222-3333-4444-8555-666666666666",
		"/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555/logs",
		"/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555/pods",
		"/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555/events",
		"/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555/logs/stream?pod_uid=22222222-3333-4444-8555-666666666666",
		"/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555/resource-metrics?pod_uid=66666666-7777-4888-8999-aaaaaaaaaaaa&container=node&start=2026-10-27T00:00:00Z&end=2026-10-27T01:00:00Z&step_seconds=60&metric_codes=cpu.usage_cores",
		// The byte relay carries the same authentication and artifact
		// authorization as the URL exchange.
		"/api/v1/modeldev/artifacts/11111111-2222-4333-8444-555555555555/download",
	})
}

func testModelDevAuthenticatedRoutes(t *testing.T, paths []string) {
	t.Helper()
	server := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
		}
	}))
	registerModelDevHTTP(server, nil)
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			response, err := web.Client().Get(web.URL + path)
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusUnauthorized, response.StatusCode, "CPU09_QUERY_ROUTE_NOT_IMPLEMENTED")
			require.Contains(t, string(body), "INVALID_LOGIN")
			require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		})
	}
}
