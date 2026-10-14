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
		{"/api/v1/modeldev/executions?tenant_id=other", ""},
		{"/api/v1/modeldev/executions?compute_state=RUNNING", ""},
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
		{"/api/v1/modeldev/executions/id/logs", "{}"},
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
		"/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555/logs",
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
