package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	catalog "go-wind-admin/api/gen/go/catalog/service/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// Exercise the registered HTTP transports; service tests separately assert
// trusted identity, tenant forwarding, readiness and durable admission.
func TestModelInferenceWritesDecodeTopLevelFields(t *testing.T) {
	tests := []struct {
		name, path, body string
		want             proto.Message
		status           int
	}{
		{"create model", "/api/v1/models", `{"name":"qwen","idempotency_key":"create-1"}`, &catalog.CreateModelRequest{Name: "qwen", IdempotencyKey: "create-1"}, 200},
		{"create version", "/api/v1/model-versions", `{"model_id":"model-1","version":"v1","idempotency_key":"version-1"}`, &catalog.CreateModelVersionRequest{ModelId: "model-1", Version: "v1", IdempotencyKey: "version-1"}, 200},
		{"import", "/api/v1/models:import", `{"source":"modelscope","repo_id":"Qwen/Qwen3","idempotency_key":"import-1"}`, &catalog.ImportModelRequest{Source: "modelscope", RepoId: "Qwen/Qwen3", IdempotencyKey: "import-1"}, 200},
		{"import bundle", "/api/v1/models:import-bundle", `{"idempotency_key":"bundle-1","model":{"name":"qwen"},"version":{"version":"v1"},"source":{"provider":"modelscope","repo_id":"Qwen/Qwen3"}}`, &catalog.ImportModelBundleRequest{IdempotencyKey: "bundle-1", Model: &catalog.ImportModelBundleModelData{Name: "qwen"}, Version: &catalog.ImportModelBundleVersionData{Version: "v1"}, Source: &catalog.ImportModelBundleSourceData{Provider: "modelscope", RepoId: "Qwen/Qwen3"}}, 200},
		{"create inference", "/api/v1/inference/services", `{"name":"qwen","idempotency_key":"create-1","model_version_id":"version-1"}`, &view.CreateInferenceRequest{Name: "qwen", IdempotencyKey: "create-1", ModelVersionId: "version-1"}, 202},
		{"delete inference", "/api/v1/inference/services/service-1:delete", `{"idempotency_key":"delete-1","expected_generation":"3"}`, &view.DeleteInferenceRequest{ResourceId: "service-1", IdempotencyKey: "delete-1", ExpectedGeneration: 3}, 202},
		{"update inference", "/api/v1/inference/services/service-1:update", `{"idempotency_key":"update-1","expected_generation":"3","update_mask":"replicas","replicas":2}`, &view.UpdateInferenceRequest{ResourceId: "service-1", IdempotencyKey: "update-1", ExpectedGeneration: 3, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"replicas"}}, Replicas: 2}, 202},
	}
	for _, action := range []string{"start", "stop", "restart"} {
		tests = append(tests, struct {
			name, path, body string
			want             proto.Message
			status           int
		}{action, "/api/v1/inference/services/service-1:" + action, `{"idempotency_key":"command-1","expected_generation":"3"}`, &view.InferenceServiceCommandRequest{ResourceId: "service-1", IdempotencyKey: "command-1", ExpectedGeneration: 3}, 202})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got proto.Message
			srv := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
				return func(_ context.Context, req interface{}) (interface{}, error) {
					got = req.(proto.Message)
					switch req.(type) {
					case *catalog.CreateModelRequest:
						return &catalog.CreateModelResponse{}, nil
					case *catalog.CreateModelVersionRequest:
						return &catalog.CreateModelVersionResponse{}, nil
					case *catalog.ImportModelRequest:
						return &catalog.ImportModelResponse{}, nil
					case *catalog.ImportModelBundleRequest:
						return &catalog.ImportModelBundleResponse{}, nil
					default:
						return &view.InferenceAcceptance{}, nil
					}
				}
			}))
			admin.RegisterModelServiceHTTPServer(srv, nil)
			RegisterInferenceHTTPServer(srv, service.NewInferenceService(nil))
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			srv.ServeHTTP(recorder, request)
			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			require.True(t, proto.Equal(tc.want, got), "want %v, got %v", tc.want, got)
		})
	}
}

func TestInferenceWritesRejectEnvelopeAndPathMismatch(t *testing.T) {
	for _, action := range []string{"delete", "update", "start", "stop", "restart"} {
		t.Run(action, func(t *testing.T) {
			called := false
			srv := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
				return func(context.Context, interface{}) (interface{}, error) {
					called = true
					return &view.InferenceAcceptance{}, nil
				}
			}))
			RegisterInferenceHTTPServer(srv, service.NewInferenceService(nil))
			for _, body := range []string{`{"resource_id":"another-service","idempotency_key":"request-1","expected_generation":"3"}`, `{"data":{"idempotency_key":"request-1","expected_generation":"3"}}`} {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/api/v1/inference/services/service-1:"+action, strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				srv.ServeHTTP(recorder, request)
				require.Equal(t, 400, recorder.Code, recorder.Body.String())
				require.False(t, called, "invalid resource or obsolete envelope must not reach business dispatch")
			}
		})
	}
}

func TestModelWritesRejectLegacyEnvelope(t *testing.T) {
	srv := khttp.NewServer()
	admin.RegisterModelServiceHTTPServer(srv, service.NewModelService(nil, nil))
	for _, path := range []string{"/api/v1/models", "/api/v1/model-versions", "/api/v1/models:import", "/api/v1/models:import-bundle"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"data":{"name":"qwen","model_id":"model-1","version":"v1","source":"modelscope","repo_id":"Qwen/Qwen3","idempotency_key":"request-1"}}`))
			request.Header.Set("Content-Type", "application/json")
			srv.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
		})
	}
}
