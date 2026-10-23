package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	catalog "go-wind-admin/api/gen/go/catalog/service/v1"
)

func TestModelQueriesHTTPContract(t *testing.T) {
	var calls int
	srv := khttp.NewServer(khttp.Filter(modelQueryHTTPFilter), khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			calls++
			switch in := req.(type) {
			case *catalog.ListModelOperationsRequest:
				require.Equal(t, "qwen", in.ModelId)
				require.Equal(t, int32(3), in.GetPage())
				require.Equal(t, int32(2), in.GetPageSize())
				return &catalog.ListModelOperationsResponse{Items: []*catalog.ModelOperation{{Id: "event-1", ModelId: "qwen", Result: "accepted"}}, Total: 12}, nil
			case *catalog.ListModelVersionsRequest:
				require.Equal(t, "failed", in.Status)
				return &catalog.ListModelVersionsResponse{Total: 2}, nil
			default:
				t.Fatalf("unexpected request %T", req)
				return nil, nil
			}
		}
	}))
	admin.RegisterModelServiceHTTPServer(srv, nil)
	for _, path := range []string{"/api/v1/models/qwen/operations?page=3&pageSize=2", "/api/v1/models/qwen/versions?status=failed"} {
		r := httptest.NewRecorder()
		srv.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, 200, r.Code, r.Body.String())
		require.Contains(t, r.Body.String(), `"total":"`)
	}
	require.Equal(t, 2, calls)
	for _, query := range []string{"page=0", "page=101", "pageSize=101", "page=01", "page=1&page=2", "pageSize=", "tenant_id=forged", "model_id=other", "status=ready", "unknown=1", "page_size=2"} {
		r := httptest.NewRecorder()
		srv.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/models/qwen/operations?"+query, nil))
		require.Equal(t, 400, r.Code, query+": "+r.Body.String())
	}
	for _, query := range []string{"status=deleted", "status=", "status=ready&status=error"} {
		r := httptest.NewRecorder()
		srv.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/models/qwen/versions?"+query, nil))
		require.Equal(t, 400, r.Code, query)
	}
	require.Equal(t, 2, calls)
}
