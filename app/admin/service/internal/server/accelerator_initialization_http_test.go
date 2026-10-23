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
	"google.golang.org/protobuf/types/known/emptypb"

	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
)

type initializationHTTPBoundary struct {
	admin.UnimplementedAcceleratorAdminServiceServer
	page        *view.ListAcceleratorDiscoveredNodesRequest
	create      *view.CreateAcceleratorInitializationRequest
	retry       *view.RetryAcceleratorInitializationOperationRequest
	operationID string
}

func (boundary *initializationHTTPBoundary) GetInitialization(context.Context, *emptypb.Empty) (*view.AcceleratorInitialization, error) {
	return &view.AcceleratorInitialization{Registered: true, ConfigurationComplete: false}, nil
}

func (boundary *initializationHTTPBoundary) ListDiscoveredNodes(_ context.Context, request *view.ListAcceleratorDiscoveredNodesRequest) (*view.AcceleratorDiscoveredNodes, error) {
	boundary.page = request
	return &view.AcceleratorDiscoveredNodes{Total: 9007199254740993, DiscoveryRevision: "revision"}, nil
}

func (boundary *initializationHTTPBoundary) CreateInitialization(_ context.Context, request *view.CreateAcceleratorInitializationRequest) (*view.AcceleratorInitializationOperation, error) {
	boundary.create = request
	return &view.AcceleratorInitializationOperation{OperationId: "11111111-1111-4111-8111-111111111111", Version: 9007199254740993, State: "ACCEPTED"}, nil
}

func (boundary *initializationHTTPBoundary) GetInitializationOperation(_ context.Context, request *view.GetAcceleratorInitializationOperationRequest) (*view.AcceleratorInitializationOperation, error) {
	boundary.operationID = request.OperationId
	return &view.AcceleratorInitializationOperation{OperationId: request.OperationId, State: "BLOCKED", Retryable: true}, nil
}

func (boundary *initializationHTTPBoundary) RetryInitializationOperation(_ context.Context, request *view.RetryAcceleratorInitializationOperationRequest) (*view.AcceleratorInitializationOperation, error) {
	boundary.retry = request
	return &view.AcceleratorInitializationOperation{OperationId: request.OperationId, Version: request.Data.ExpectedVersion, State: "ACCEPTED"}, nil
}

func TestAcceleratorInitializationGeneratedHTTPRoutes(t *testing.T) {
	boundary := &initializationHTTPBoundary{}
	server := khttp.NewServer(khttp.Filter(acceleratorInitializationHTTPFilter))
	admin.RegisterAcceleratorAdminServiceHTTPServer(server, boundary)
	const operationID = "11111111-1111-4111-8111-111111111111"
	for _, test := range []struct{ method, path, body, want string }{
		{"GET", "/api/v1/admin/accelerator/initialization", "", `"registered":true`},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes?page=3&pageSize=200", "", `"total":"9007199254740993"`},
		{"POST", "/api/v1/admin/accelerator/initializations", `{"data":{"idempotency_key":"key","discovery_revision":"revision"}}`, `"version":"9007199254740993"`},
		{"GET", "/api/v1/admin/accelerator/initializations/" + operationID, "", `"state":"BLOCKED"`},
		{"POST", "/api/v1/admin/accelerator/initializations/" + operationID + ":retry", `{"operation_id":"body-must-not-win","data":{"idempotency_key":"retry-key","expected_version":"9007199254740993"}}`, `"state":"ACCEPTED"`},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), test.want)
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
		})
	}
	require.EqualValues(t, 3, boundary.page.GetPage())
	require.EqualValues(t, 200, boundary.page.GetPageSize())
	require.Equal(t, "revision", boundary.create.GetData().GetDiscoveryRevision())
	require.Equal(t, operationID, boundary.operationID)
	require.Equal(t, operationID, boundary.retry.OperationId)
	require.EqualValues(t, 9007199254740993, boundary.retry.Data.ExpectedVersion)
}

func TestAcceleratorInitializationGeneratedHTTPRejectsAmbiguousQuery(t *testing.T) {
	server := khttp.NewServer(khttp.Filter(acceleratorInitializationHTTPFilter))
	admin.RegisterAcceleratorAdminServiceHTTPServer(server, &initializationHTTPBoundary{})
	for _, test := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/accelerator/initialization?actor=other"},
		{"GET", "/api/v1/admin/accelerator/initializations/id?operation_id=other"},
		{"POST", "/api/v1/admin/accelerator/initializations?data.idempotency_key=other"},
		{"POST", "/api/v1/admin/accelerator/initializations/id:retry?operation_id=other"},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes?page=0"},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes?page=01"},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes?pageSize=201"},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes?pageSize="},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes?page=1&page=2"},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes?pageSize=20&page_size=20"},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes?tenant_id=other"},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes?page=4294967296"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{"data":{}}`))
			request.Header.Set("Content-Type", "application/json")
			server.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
	request := httptest.NewRequest("GET", "/api/v1/admin/accelerator/clusters?page_size=50&page_token=original", nil)
	response := httptest.NewRecorder()
	called := false
	acceleratorInitializationHTTPFilter(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(response, request)
	require.True(t, called)
}

func TestAcceleratorInitializationRoutesEnterAuthentication(t *testing.T) {
	server := khttp.NewServer(khttp.Filter(acceleratorInitializationHTTPFilter), khttp.Middleware(func(middleware.Handler) middleware.Handler {
		return func(context.Context, interface{}) (interface{}, error) {
			return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
		}
	}))
	admin.RegisterAcceleratorAdminServiceHTTPServer(server, &initializationHTTPBoundary{})
	for _, test := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/accelerator/initialization"},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes"},
		{"POST", "/api/v1/admin/accelerator/initializations"},
		{"GET", "/api/v1/admin/accelerator/initializations/id"},
		{"POST", "/api/v1/admin/accelerator/initializations/id:retry"},
	} {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{"data":{}}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		require.Equal(t, http.StatusUnauthorized, response.Code)
		body, err := io.ReadAll(response.Result().Body)
		require.NoError(t, err)
		require.Contains(t, string(body), "INVALID_LOGIN")
	}
}
