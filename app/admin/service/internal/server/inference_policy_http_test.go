package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/middleware"
	kratoshttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
)

func TestInferencePolicyHTTPBoundary(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	key := "22222222-2222-4222-8222-222222222222"
	calls := 0
	srv := kratoshttp.NewServer(kratoshttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, in interface{}) (interface{}, error) {
			calls++
			switch v := in.(type) {
			case *view.CreateInferencePolicyRequest:
				require.NotNil(t, v.Data)
				return &view.InferenceAccessPolicy{}, nil
			case *view.ListInferencePoliciesRequest:
				return &view.ListInferencePoliciesResponse{}, nil
			case *view.GetInferencePolicyRequest:
				require.Equal(t, id, v.PolicyId)
				return &view.InferenceAccessPolicy{}, nil
			case *view.UpdateInferencePolicyRequest:
				require.Equal(t, id, v.PolicyId)
				return &view.InferenceAccessPolicy{}, nil
			case *view.DeleteInferencePolicyRequest:
				require.Equal(t, id, v.PolicyId)
				return &view.DeleteInferencePolicyResponse{}, nil
			case *view.BindInferenceKeyPolicyRequest:
				require.Equal(t, key, v.KeyId)
				return &view.InferenceKey{}, nil
			default:
				t.Fatalf("unexpected request %T", in)
				return nil, nil
			}
		}
	}))
	RegisterInferenceHTTPServer(srv, service.NewInferenceService(nil))
	tests := []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/v1/inference-policies", `{"data":{"name":"p"}}`, 200},
		{"GET", "/api/v1/inference-policies?page=2&pageSize=5", "", 200},
		{"GET", "/api/v1/inference-policies/" + id, "", 200},
		{"PUT", "/api/v1/inference-policies/" + id, `{"data":{"name":"p"}}`, 200},
		{"DELETE", "/api/v1/inference-policies/" + id, "", 200},
		{"POST", "/api/v1/inference-keys/" + key + ":bind-policy", `{"data":{"policy_id":"` + id + `"}}`, 200},
		{"PUT", "/api/v1/inference-policies/" + id, `{"policy_id":"wrong","data":{}}`, 400},
		{"PUT", "/api/v1/inference-policies/" + id, `{"data":{},"data":{}}`, 400},
		{"GET", "/api/v1/inference-policies?page=01", "", 400},
		{"GET", "/api/v1/inference-policies?tenant_id=1", "", 400},
		{"POST", "/api/v1/inference-keys/" + key + ":bind-policy", `{"key_id":"wrong","data":{"policy_id":"` + id + `"}}`, 400},
		{"GET", "/api/v1/inference/services/" + id + "/policy", "", 404},
	}
	for _, tc := range tests {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		srv.ServeHTTP(w, req)
		require.Equal(t, tc.want, w.Code, "%s %s %s", tc.method, tc.path, tc.body)
	}
	require.Equal(t, 6, calls)
	request := httptest.NewRequest("GET", "/api/v1/inference-policies/"+id, nil)
	request.Header.Set("X-Ani-Tenant-Id", "forged")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, request)
	require.Equal(t, 400, w.Code)
}
