package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"

	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
)

func TestAcceleratorSupplyHTTPQueryBoundary(t *testing.T) {
	for _, test := range []struct {
		name, method, path string
		allowed            bool
	}{
		{"candidate defaults", "GET", "/api/v1/admin/accelerator/supply-candidates", true},
		{"candidate page", "GET", "/api/v1/admin/accelerator/supply-candidates?connection_ref=managed&page=3&pageSize=200", true},
		{"zero page", "GET", "/api/v1/admin/accelerator/supply-candidates?page=0", false},
		{"leading zero", "GET", "/api/v1/admin/accelerator/supply-candidates?page=01", false},
		{"empty page size", "GET", "/api/v1/admin/accelerator/supply-candidates?pageSize=", false},
		{"large page size", "GET", "/api/v1/admin/accelerator/supply-candidates?pageSize=201", false},
		{"overflow page", "GET", "/api/v1/admin/accelerator/supply-candidates?page=4294967296", false},
		{"duplicate page", "GET", "/api/v1/admin/accelerator/supply-candidates?page=1&page=2", false},
		{"duplicate connection", "GET", "/api/v1/admin/accelerator/supply-candidates?connection_ref=a&connection_ref=b", false},
		{"empty connection", "GET", "/api/v1/admin/accelerator/supply-candidates?connection_ref=", false},
		{"long connection", "GET", "/api/v1/admin/accelerator/supply-candidates?connection_ref=" + strings.Repeat("a", 129), false},
		{"unsupported filter", "GET", "/api/v1/admin/accelerator/supply-candidates?node_uid=other", false},
		{"forged actor", "GET", "/api/v1/admin/accelerator/supply-candidates?actor_id=other", false},
		{"forced query", "GET", "/api/v1/admin/accelerator/supply-candidates?", false},
		{"registration body only", "POST", "/api/v1/admin/accelerator/supply-registrations", true},
		{"registration query identity", "POST", "/api/v1/admin/accelerator/supply-registrations?tenant_id=other", false},
		{"registration query data", "POST", "/api/v1/admin/accelerator/supply-registrations?data.connection_ref=other", false},
		{"saved registration", "GET", "/api/v1/admin/accelerator/supply-registrations/11111111-1111-4111-8111-111111111111", true},
		{"saved registration query identity", "GET", "/api/v1/admin/accelerator/supply-registrations/11111111-1111-4111-8111-111111111111?registration_id=other", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := acceleratorInitializationHTTPFilter(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				called = true
				writer.WriteHeader(http.StatusNoContent)
			}))
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.method == http.MethodPost {
				request = httptest.NewRequest(test.method, test.path, strings.NewReader(`{"data":{}}`))
			}
			request.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(response, request)
			require.Equal(t, test.allowed, called, response.Body.String())
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			if test.allowed {
				require.Equal(t, http.StatusNoContent, response.Code)
			} else {
				require.Equal(t, http.StatusBadRequest, response.Code)
				require.Contains(t, response.Body.String(), "INVALID_ACCELERATOR_REQUEST")
			}
		})
	}
}

type supplyHTTPBoundary struct {
	admin.UnimplementedAcceleratorAdminServiceServer
	listed     *view.ListAcceleratorSupplyCandidatesRequest
	registered *view.RegisterAcceleratorSupplyConfigurationRequest
	fetched    *view.GetAcceleratorSupplyRegistrationRequest
}

func (boundary *supplyHTTPBoundary) ListSupplyCandidates(_ context.Context, request *view.ListAcceleratorSupplyCandidatesRequest) (*view.AcceleratorSupplyCandidates, error) {
	boundary.listed = request
	maximum, zero, knownFalse := uint64(18446744073709551615), uint32(0), false
	return &view.AcceleratorSupplyCandidates{ConnectionRef: request.ConnectionRef, Total: 9007199254740993, DiscoveryRevision: "stable", Items: []*view.AcceleratorSupplyCandidate{{CandidateId: "candidate", MemoryCapacityBytes: &maximum, MemoryBlockMib: &zero, CoreBudgetAdditive: &knownFalse, MissingFields: []string{"compute_units_per_device"}}}}, nil
}

func (boundary *supplyHTTPBoundary) RegisterSupplyConfiguration(_ context.Context, request *view.RegisterAcceleratorSupplyConfigurationRequest) (*view.AcceleratorSupplyRegistrationResult, error) {
	boundary.registered = request
	return &view.AcceleratorSupplyRegistrationResult{RegistrationId: "11111111-1111-4111-8111-111111111111", ConnectionRef: request.Data.ConnectionRef, Configurations: []*view.AcceleratorRegisteredSupplyConfiguration{{ProviderConfigRef: "registered-ref", Version: 1}}}, nil
}
func (boundary *supplyHTTPBoundary) GetSupplyRegistration(_ context.Context, request *view.GetAcceleratorSupplyRegistrationRequest) (*view.AcceleratorSupplyRegistrationResult, error) {
	boundary.fetched = request
	return &view.AcceleratorSupplyRegistrationResult{RegistrationId: request.RegistrationId, ConnectionRef: "managed"}, nil
}

func TestAcceleratorSupplyGeneratedHTTPContract(t *testing.T) {
	boundary := &supplyHTTPBoundary{}
	server := khttp.NewServer(khttp.Filter(acceleratorInitializationHTTPFilter))
	admin.RegisterAcceleratorAdminServiceHTTPServer(server, boundary)
	query := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accelerator/supply-candidates?connection_ref=managed&page=3&pageSize=200", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, query)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.EqualValues(t, 3, boundary.listed.GetPage())
	require.EqualValues(t, 200, boundary.listed.GetPageSize())
	require.Contains(t, response.Body.String(), `"total":"9007199254740993"`)
	require.Contains(t, response.Body.String(), `"memory_capacity_bytes":"18446744073709551615"`)
	require.Contains(t, response.Body.String(), `"memory_block_mib":0`)
	require.Contains(t, response.Body.String(), `"core_budget_additive":false`)
	var body struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.NotContains(t, body.Items[0], "compute_units_per_device")
	const registration = `{"data":{"idempotency_key":"11111111-1111-4111-8111-111111111111","connection_ref":"managed","discovery_revision":"stable","registrations":[{"client_key":"fixed","candidate_ids":["candidate"],"display_name":"fixed","max_devices_per_replica":1,"resource_mapping_confirmed":true,"resource_mapping_source":"approved type","declarations":[{"field":"memory_capacity_bytes","value":"18446744073709551615","source":"approved actual type"}]}]}}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accelerator/supply-registrations", strings.NewReader(registration))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.Contains(t, response.Body.String(), `"provider_config_ref":"registered-ref"`)
	require.NotNil(t, boundary.registered)
	declaration := boundary.registered.Data.Registrations[0].Declarations[0]
	require.Equal(t, "18446744073709551615", declaration.Value)
	require.Equal(t, "approved actual type", declaration.Source)
	require.True(t, boundary.registered.Data.Registrations[0].ResourceMappingConfirmed)
	request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/accelerator/supply-registrations/11111111-1111-4111-8111-111111111111", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "11111111-1111-4111-8111-111111111111", boundary.fetched.RegistrationId)
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
}

func TestAcceleratorSupplyRegistrationReadRejectsBody(t *testing.T) {
	boundary := &supplyHTTPBoundary{}
	server := khttp.NewServer(khttp.Filter(acceleratorInitializationHTTPFilter))
	admin.RegisterAcceleratorAdminServiceHTTPServer(server, boundary)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accelerator/supply-registrations/11111111-1111-4111-8111-111111111111", strings.NewReader(`{"registration_id":"body-must-not-win"}`))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Nil(t, boundary.fetched)
}

func TestAcceleratorSupplyRegistrationRejectsUnknownBodyFields(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"data":null}`,
		`{"actor":{"id":"other"},"data":{}}`,
		`{"data":{"tenant_id":"other"}}`,
		`{"data":{"mutation":{"actor":"other"}}}`,
		`{"data":{"registrations":[{"model_key":"override-observation"}]}}`,
		`{"data":{"registrations":[{"max_devices_per_replica":4294967296}]}}`,
		`{"data":{"connection_ref":"first","connection_ref":"second"}}`,
		`{"data":{"discovery_revision":"` + strings.Repeat("a", 1<<20) + `"}}`,
	} {
		boundary := &supplyHTTPBoundary{}
		server := khttp.NewServer(khttp.Filter(acceleratorInitializationHTTPFilter))
		admin.RegisterAcceleratorAdminServiceHTTPServer(server, boundary)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accelerator/supply-registrations", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		require.Equal(t, http.StatusBadRequest, response.Code)
		require.Nil(t, boundary.registered)
	}
}

func TestAcceleratorSupplyRoutesEnterAuthentication(t *testing.T) {
	server := khttp.NewServer(khttp.Filter(acceleratorInitializationHTTPFilter), khttp.Middleware(func(middleware.Handler) middleware.Handler {
		return func(context.Context, interface{}) (interface{}, error) {
			return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
		}
	}))
	admin.RegisterAcceleratorAdminServiceHTTPServer(server, &supplyHTTPBoundary{})
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/accelerator/supply-candidates"},
		{http.MethodPost, "/api/v1/admin/accelerator/supply-registrations"},
		{http.MethodGet, "/api/v1/admin/accelerator/supply-registrations/11111111-1111-4111-8111-111111111111"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		if test.method == http.MethodPost {
			request = httptest.NewRequest(test.method, test.path, strings.NewReader(`{"data":{}}`))
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Ani-Actor-Id", "forged")
		request.Header.Set("X-Ani-Action", "RegisterSupplyConfiguration")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		require.Equal(t, http.StatusUnauthorized, response.Code)
		raw, err := io.ReadAll(response.Result().Body)
		require.NoError(t, err)
		require.Contains(t, string(raw), "INVALID_LOGIN")
	}
}

// The public command has never required a core-budget field. Preserve strict
// decoding while accepting new omission and historical declaration bodies.
func TestAcceleratorSupplyPluginCoreDeclarationHTTPCompatibility(t *testing.T) {
	for _, value := range []string{"", "true", "false"} {
		name := value
		if name == "" {
			name = "omitted"
		}
		t.Run(name, func(t *testing.T) {
			boundary := &supplyHTTPBoundary{}
			server := khttp.NewServer(khttp.Filter(acceleratorInitializationHTTPFilter))
			admin.RegisterAcceleratorAdminServiceHTTPServer(server, boundary)
			entry := map[string]any{"client_key": "plugin", "candidate_ids": []string{"candidate"}, "display_name": "T4", "whole_max_devices_per_replica": 2, "runtime_class_name": "nvidia"}
			if value != "" {
				entry["declarations"] = []map[string]string{{"field": "core_budget_additive", "value": value, "source": "historical explicit administrator declaration"}}
			}
			body, err := json.Marshal(map[string]any{"data": map[string]any{"idempotency_key": "11111111-1111-4111-8111-111111111111", "connection_ref": "managed", "discovery_revision": "stable", "registrations": []any{entry}}})
			require.NoError(t, err)
			request := httptest.NewRequest("POST", "/api/v1/admin/accelerator/supply-registrations", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.NotNil(t, boundary.registered)
			forwarded := boundary.registered.Data.Registrations[0]
			require.EqualValues(t, 2, forwarded.GetWholeMaxDevicesPerReplica())
			require.Zero(t, forwarded.MaxDevicesPerReplica)
			if value == "" {
				require.Empty(t, forwarded.Declarations)
			} else {
				require.Len(t, forwarded.Declarations, 1)
				require.Equal(t, value, forwarded.Declarations[0].Value)
			}
		})
	}
}
