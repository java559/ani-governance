package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
)

type groupPolicyHTTPBoundary struct {
	admin.UnimplementedAcceleratorAdminServiceServer
	configuration *view.GetAcceleratorSupplyConfigurationRequest
	preview       *view.PreviewAcceleratorSupplyGroupPolicyRequest
}

func (b *groupPolicyHTTPBoundary) GetSupplyConfiguration(_ context.Context, input *view.GetAcceleratorSupplyConfigurationRequest) (*view.AcceleratorRegisteredSupplyConfiguration, error) {
	b.configuration = input
	limit := uint32(2)
	return &view.AcceleratorRegisteredSupplyConfiguration{ConnectionRef: input.ConnectionRef, ProviderConfigRef: input.ProviderConfigRef, Version: 1, Capability: &view.AcceleratorSupplyCapability{SourceKind: "PLUGIN", WholeMaxDevicesPerReplica: &limit}}, nil
}
func (b *groupPolicyHTTPBoundary) PreviewSupplyGroupPolicy(_ context.Context, input *view.PreviewAcceleratorSupplyGroupPolicyRequest) (*view.AcceleratorSupplyGroupPolicyPreview, error) {
	b.preview = input
	parts := uint32(8)
	return &view.AcceleratorSupplyGroupPolicyPreview{ConnectionRef: input.Data.ConnectionRef, ProviderConfigRef: input.Data.ProviderConfigRef, ProviderConfigVersion: input.Data.ProviderConfigVersion, DiscoveryRevision: input.Data.DiscoveryRevision, Options: []*view.AcceleratorSupplyGroupPolicyOption{{Policy: &view.AcceleratorSupplyGroupPolicy{PolicyKind: "EQUAL_SHARE", Parts: &parts}, BlockingReasons: []string{"GROUP_POLICY_SPLIT_LIMIT"}, MissingFields: []string{"effective_memory_mib"}}}}, nil
}
func TestAcceleratorGroupPolicyGeneratedHTTPContract(t *testing.T) {
	boundary := &groupPolicyHTTPBoundary{}
	server := khttp.NewServer(khttp.Filter(acceleratorInitializationHTTPFilter))
	admin.RegisterAcceleratorAdminServiceHTTPServer(server, boundary)
	request := httptest.NewRequest("GET", "/api/v1/admin/accelerator/supply-configurations?connection_ref=managed&provider_config_ref=supply", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "managed", boundary.configuration.ConnectionRef)
	require.Contains(t, response.Body.String(), `"whole_max_devices_per_replica":2`)
	const body = `{"data":{"connection_ref":"managed","provider_config_ref":"supply","provider_config_version":"1","discovery_revision":"revision","nodes":[{"uid":"uid","name":"node"}]}}`
	request = httptest.NewRequest("POST", "/api/v1/admin/accelerator/supply-groups:preview-policy", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "GROUP_POLICY_SPLIT_LIMIT")
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	require.EqualValues(t, 1, boundary.preview.Data.ProviderConfigVersion)
	require.Equal(t, "revision", boundary.preview.Data.DiscoveryRevision)
}
func TestAcceleratorGroupPolicyHTTPStrictInputs(t *testing.T) {
	for _, test := range []struct {
		path, body string
		valid      bool
	}{
		{"/supply-configurations?connection_ref=managed&provider_config_ref=supply", "", true},
		{"/supply-configurations?connection_ref=managed", "", false},
		{"/supply-configurations?connection_ref=managed&provider_config_ref=supply&actor=other", "", false},
		{"/supply-configurations?connection_ref=managed&provider_config_ref=supply&provider_config_ref=other", "", false},
		{"/supply-groups:preview-policy", `{"data":{"connection_ref":"managed","provider_config_ref":"supply","provider_config_version":"1"}}`, true},
		{"/supply-groups:preview-policy", `{"data":{"actor":"other"}}`, false},
		{"/supply-groups:preview-policy", `{"data":{"provider_config_version":1}}`, false},
		{"/initializations", `{"data":{"allow_cluster_registration":true}}`, false},
		{"/initializations", `{"data":{"profiles":[{"derive_from_group_policy":true,"memory_mib_per_device":"0"}]}}`, false},
		{"/initializations", `{"data":{"profiles":[{"derive_from_group_policy":true,"core_limit_percent":0}]}}`, false},
		{"/initializations", `{"data":{"profiles":[{"derive_from_group_policy":true}]}}`, true},
		{"/initializations", `{"data":{"groups":[{"new_group":{"policy":{"policy_kind":"WHOLE","parts":null}}}]}}`, false},
		{"/initializations", `{"data":{"groups":[{"new_group":{"policy":{"policy_kind":"WHOLE"}}}]}}`, true},
		{"/profiles", `{"data":{"group_id":"group","spec":null}}`, false},
		{"/profiles", `{"data":{"group_id":"group"}}`, true},
		{"/supply-registrations", `{"data":{"registrations":[{"whole_max_devices_per_replica":2,"max_devices_per_replica":0}]}}`, false},
	} {
		t.Run(test.path+test.body, func(t *testing.T) {
			method := "POST"
			if test.body == "" {
				method = "GET"
			}
			called := false
			handler := acceleratorInitializationHTTPFilter(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
			request := httptest.NewRequest(method, "/api/v1/admin/accelerator"+test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, test.valid, called, response.Body.String())
			if !test.valid {
				require.Equal(t, http.StatusBadRequest, response.Code)
			}
		})
	}
}
