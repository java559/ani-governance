package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/middleware"
	kratoshttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
)

func TestInferenceQueriesRegisteredBehindMiddleware(t *testing.T) {
	called := 0
	srv := kratoshttp.NewServer(kratoshttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			called++
			return &view.InferenceAcceptance{ResourceId: "probe"}, nil
		}
	}))
	RegisterInferenceHTTPServer(srv, service.NewInferenceService(nil))
	for _, path := range []string{"/api/v1/inference/services", "/api/v1/inference/services/service-1", "/api/v1/inference/operations/operation-1"} {
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, response.Code, path)
	}
	require.Equal(t, 3, called)
}

func TestInferenceDeletePathResourceIDUsesNestedRouteVariable(t *testing.T) {
	vars := url.Values{}
	vars.Set("data.resource_id", "service-1")
	require.Equal(t, "service-1", inferenceDeletePathResourceID(vars))
	vars = url.Values{}
	vars.Set("resource_id", "service-2")
	require.Equal(t, "service-2", inferenceDeletePathResourceID(vars))
}

func TestInferencePublicHTTPRejectsTrustedFieldsAndAmbiguousJSON(t *testing.T) {
	valid := `{"data":{"idempotency_key":"20000000-0000-4000-8000-000000000001","name":"wire","model_version_id":"m","served_model_name":"wire","engine_image":"image","engine_type":"vllm","command":"server","preset_id":"40000000-0000-4000-8000-000000000001","replicas":1}}`
	request := httptest.NewRequest("POST", "/api/v1/inference/services", strings.NewReader(valid))
	request.Header.Set("Content-Type", "application/json")
	require.NoError(t, decodeInferenceHTTP(request, new(view.CreateInferenceRequest)))
	for _, body := range []string{`{"data":{"tenant_id":"forged"}}`, `{"data":{"gpu_owner_attachment":{}}}`, `{"data":{"original_charges":[]}}`, `{"data":{"plan":{}}}`, `{"data":{"cpu":""}}`, `{"data":{"memory":""}}`, `{"data":{"gpu":null}}`, `{"data":{"name":"a","name":"b"}}`, `{"data":{}} {"data":{}}`} {
		request = httptest.NewRequest("POST", "/api/v1/inference/services", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		require.Error(t, decodeInferenceHTTP(request, new(view.CreateInferenceRequest)))
	}
	request = httptest.NewRequest("POST", "/api/v1/inference/services?tenant_id=forged", strings.NewReader(valid))
	request.Header.Set("Content-Type", "application/json")
	require.Error(t, decodeInferenceHTTP(request, new(view.CreateInferenceRequest)))
	request = httptest.NewRequest("POST", "/api/v1/inference/services", strings.NewReader(valid))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Ani-Tenant-Id", "forged")
	require.Error(t, decodeInferenceHTTP(request, new(view.CreateInferenceRequest)))
}

func TestInferenceDeleteHTTPDecodesDataEnvelope(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/v1/inference/services/service-1:delete", strings.NewReader(`{"data":{"idempotency_key":"20000000-0000-4000-8000-000000000001","resource_id":"service-1","expected_generation":"1"}}`))
	request.Header.Set("Content-Type", "application/json")
	decoded := new(view.DeleteInferenceRequest)
	require.NoError(t, decodeInferenceHTTP(request, decoded))
	require.Equal(t, "service-1", decoded.Data.ResourceId)
}

func TestInferenceDeleteHTTPBindsRouteResourceID(t *testing.T) {
	called := 0
	srv := kratoshttp.NewServer(kratoshttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			called++
			return &view.InferenceAcceptance{ResourceId: "service-1"}, nil
		}
	}))
	RegisterInferenceHTTPServer(srv, service.NewInferenceService(nil))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/api/v1/inference/services/service-1:delete", strings.NewReader(`{"data":{"idempotency_key":"20000000-0000-4000-8000-000000000001"}}`))
	request.Header.Set("Content-Type", "application/json")
	srv.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.Equal(t, 1, called)
}

func TestInferenceHTTPAcceptsCanonicalRawPath(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/v1/inference/services/service-1:delete", strings.NewReader(`{"data":{"idempotency_key":"20000000-0000-4000-8000-000000000001"}}`))
	request.URL.RawPath = request.URL.Path
	request.Header.Set("Content-Type", "application/json")
	require.NoError(t, decodeInferenceHTTP(request, new(view.DeleteInferenceRequest)))
}
