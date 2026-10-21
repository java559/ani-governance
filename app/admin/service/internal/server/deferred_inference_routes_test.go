package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewRestServerRegistersInferenceManagement(t *testing.T) {
	srv := newRestServerWithSwagger(t, false)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/inference/services"},
		{http.MethodPost, "/api/v1/inference/services"},
		{http.MethodGet, "/api/v1/inference/services/service-1"},
		{http.MethodGet, "/api/v1/inference/services/service-1/operations"},
		{http.MethodGet, "/api/v1/inference/operations/operation-1"},
		{http.MethodPost, "/api/v1/inference/services/service-1:update"},
		{http.MethodPost, "/api/v1/inference/services/service-1:delete"},
		{http.MethodPost, "/api/v1/inference/services/service-1:start"},
		{http.MethodPost, "/api/v1/inference/services/service-1:stop"},
		{http.MethodPost, "/api/v1/inference/services/service-1:restart"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{"data":{}}`))
			request.Header.Set("Content-Type", "application/json")
			srv.ServeHTTP(response, request)
			require.NotEqual(t, http.StatusNotFound, response.Code, "inference management route must be registered on Governance")
		})
	}

	response := httptest.NewRecorder()
	srv.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
	require.Equal(t, http.StatusUnauthorized, response.Code, "the independent Model route must still reach its authenticated business boundary")
}
