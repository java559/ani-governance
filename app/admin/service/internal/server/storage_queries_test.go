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
	storagepb "go-wind-admin/api/gen/go/storage/service/v1"
)

func TestStorageQueriesHTTPContract(t *testing.T) {
	var calls int
	srv := khttp.NewServer(khttp.Filter(storageQueryHTTPFilter), khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			calls++
			switch in := req.(type) {
			case *storagepb.ListVolumesRequest:
				require.Equal(t, uint32(2), in.GetPage())
				require.Equal(t, uint32(50), in.GetPageSize())
				require.Equal(t, "数据", in.GetName())
				require.Equal(t, "ani-vol-abc", in.GetId())
				require.Equal(t, "Bound", in.GetState())
				return &storagepb.ListVolumesResponse{}, nil
			case *storagepb.ListBucketsRequest:
				require.Equal(t, "files", in.GetName())
				return &storagepb.ListBucketsResponse{}, nil
			case *storagepb.ListObjectsRequest:
				require.Equal(t, "docs/", in.GetPrefix())
				require.Equal(t, uint32(10), in.GetLimit())
				require.Equal(t, "cursor-1", in.GetCursor())
				return &storagepb.ListObjectsResponse{}, nil
			case *storagepb.ListStorageClassesRequest:
				return &storagepb.ListStorageClassesResponse{}, nil
			case *storagepb.CreateVolumeRequest:
				require.Equal(t, "数据盘", in.GetData().GetDisplayName())
				return &storagepb.CreateVolumeResponse{}, nil
			default:
				t.Fatalf("unexpected request %T", req)
				return nil, nil
			}
		}
	}))
	admin.RegisterStorageServiceHTTPServer(srv, nil)

	valid := []string{
		"/api/v1/storage/classes?page=1&pageSize=20",
		"/api/v1/storage/volumes?page=2&pageSize=50&name=%E6%95%B0%E6%8D%AE&id=ani-vol-abc&state=Bound",
		"/api/v1/storage/buckets?name=files",
		"/api/v1/storage/buckets/files/objects?prefix=docs/&limit=10&cursor=cursor-1",
	}
	for _, path := range valid {
		r := httptest.NewRecorder()
		srv.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, 200, r.Code, path+": "+r.Body.String())
	}
	require.Equal(t, len(valid), calls)

	// 写接口带 JSON body 不带查询串必须放行（过滤器只校验查询参数）。
	body := strings.NewReader(`{"data":{"create_token":"00000000-0000-4000-8000-000000000010","display_name":"数据盘","capacity_gb":20,"storage_class_name":"ani-block"}}`)
	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/storage/volumes", body)
	createRequest.Header.Set("Content-Type", "application/json")
	r := httptest.NewRecorder()
	srv.ServeHTTP(r, createRequest)
	require.Equal(t, 200, r.Code, r.Body.String())
	require.Equal(t, len(valid)+1, calls)

	invalid := []string{
		"/api/v1/storage/classes?page=",
		"/api/v1/storage/classes?page=0",
		"/api/v1/storage/classes?page=01",
		"/api/v1/storage/classes?page=abc",
		"/api/v1/storage/classes?page=1&page=2",
		"/api/v1/storage/classes?page_size=2",
		"/api/v1/storage/classes?pageSize=101",
		"/api/v1/storage/classes?unknown=1",
		"/api/v1/storage/volumes?state=bound",
		"/api/v1/storage/volumes?state=",
		"/api/v1/storage/volumes?name=",
		"/api/v1/storage/volumes?id=",
		"/api/v1/storage/buckets?id=",
		"/api/v1/storage/volumes/ani-vol-a?page=1",
		"/api/v1/storage/buckets/files/objects?page=1",
		"/api/v1/storage/buckets/files/objects?limit=0",
		"/api/v1/storage/buckets/files/objects?limit=101",
		"/api/v1/storage/buckets/files/objects?prefix=",
		"/api/v1/storage/buckets/files/objects?cursor=",
	}
	for _, path := range invalid {
		r := httptest.NewRecorder()
		srv.ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, 400, r.Code, path+": "+r.Body.String())
		require.Contains(t, r.Body.String(), "STORAGE_INVALID_REQUEST")
	}

	r = httptest.NewRecorder()
	srv.ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/api/v1/storage/volumes?page=1", nil))
	require.Equal(t, 400, r.Code, r.Body.String())
	for _, path := range []string{"/api/v1/storage/buckets", "/api/v1/storage/buckets/files/objects:upload-url", "/api/v1/storage/buckets/files/objects:delete"} {
		for _, query := range []string{"?page=1", "?unknown=1", "?"} {
			r := httptest.NewRecorder()
			srv.ServeHTTP(r, httptest.NewRequest(http.MethodPost, path+query, strings.NewReader(`{"data":{}}`)))
			require.Equal(t, 400, r.Code, path+query)
		}
	}
	require.Equal(t, len(valid)+1, calls, "rejected requests must not reach the service")
}
