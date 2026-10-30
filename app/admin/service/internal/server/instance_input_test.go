package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	computeview "go-wind-admin/api/gen/go/compute/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"google.golang.org/protobuf/proto"
)

func TestInstanceHTTPCreateSchedulingPresence(t *testing.T) {
	const fields = `"idempotency_key":"10000000-0000-4000-8000-000000000001","name":"workspace","image_id":"image-a","image_scope":"platform","preset_id":"20000000-0000-4000-8000-000000000001"`
	for _, tc := range []struct {
		name, scheduling string
		placement        *view.SchedulingOptions_PodPlacement
		priority         *string
	}{
		{name: "omitted"},
		{name: "explicit none", scheduling: `{"pod_placement":"NONE"}`, placement: view.SchedulingOptions_NONE.Enum()},
		{name: "colocate", scheduling: `{"pod_placement":"COLOCATE_PREFERRED"}`, placement: view.SchedulingOptions_COLOCATE_PREFERRED.Enum()},
		{name: "spread", scheduling: `{"pod_placement":"SPREAD_PREFERRED"}`, placement: view.SchedulingOptions_SPREAD_PREFERRED.Enum()},
		{name: "priority only", scheduling: `{"priority_class_name":"workload-priority"}`, priority: proto.String("workload-priority")},
		{name: "both", scheduling: `{"pod_placement":"NONE","priority_class_name":"workload-priority"}`, placement: view.SchedulingOptions_NONE.Enum(), priority: proto.String("workload-priority")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"data":{` + fields
			if tc.scheduling != "" {
				body += `,"scheduling":` + tc.scheduling
			}
			body += `}}`
			request := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			message := new(view.CreateInstanceRequest)
			require.NoError(t, decodeInstanceHTTP(request, message))
			require.NotNil(t, message.Data)
			if tc.scheduling == "" {
				require.Nil(t, message.Data.Scheduling, "omitted raw intent must remain absent")
				return
			}
			require.NotNil(t, message.Data.Scheduling)
			require.Equal(t, tc.placement, message.Data.Scheduling.PodPlacement)
			require.Equal(t, tc.priority, message.Data.Scheduling.PriorityClassName)
		})
	}
}

func TestInstanceHTTPRejectsInvalidSchedulingAndInternalFields(t *testing.T) {
	const fields = `"idempotency_key":"10000000-0000-4000-8000-000000000001","name":"workspace","image_id":"image-a","image_scope":"platform","preset_id":"20000000-0000-4000-8000-000000000001"`
	for _, tc := range []struct{ name, value string }{
		{"null", `null`}, {"empty object", `{}`}, {"array", `[]`}, {"string", `"NONE"`},
		{"null placement", `{"pod_placement":null}`}, {"null priority", `{"priority_class_name":null}`},
		{"unspecified placement", `{"pod_placement":"POD_PLACEMENT_UNSPECIFIED"}`},
		{"unknown placement", `{"pod_placement":"SPREAD"}`},
		{"numeric unspecified", `{"pod_placement":0}`}, {"numeric placement", `{"pod_placement":1}`},
		{"empty priority", `{"priority_class_name":""}`}, {"whitespace priority", `{"priority_class_name":" "}`},
		{"padded priority", `{"priority_class_name":" workload-priority"}`},
		{"internal defaults", `{"scheduling_defaults":{"pod_placement":"NONE"}}`},
		{"internal actual", `{"actual":{"pod_placement":"NONE"}}`},
		{"internal namespace", `{"namespace":"other-tenant"}`},
		{"unknown alongside valid", `{"pod_placement":"NONE","cluster_id":"other-cluster"}`},
		{"duplicate placement", `{"pod_placement":"NONE","pod_placement":"SPREAD_PREFERRED"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(`{"data":{`+fields+`,"scheduling":`+tc.value+`}}`))
			request.Header.Set("Content-Type", "application/json")
			err := decodeInstanceHTTP(request, new(view.CreateInstanceRequest))
			require.Error(t, err)
			require.EqualValues(t, 400, kerrors.Code(err))
		})
	}
	for _, field := range []string{"preset_version", "presetVersion", "instance_type", "instanceType", "instance_kind", "compute_type", "namespace", "cluster_id", "gpu_plan", "charges", "scheduling_defaults"} {
		t.Run("forbidden create field "+field, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(`{"data":{`+fields+`,"`+field+`":"injected"}}`))
			request.Header.Set("Content-Type", "application/json")
			err := decodeInstanceHTTP(request, new(view.CreateInstanceRequest))
			require.Error(t, err)
			require.EqualValues(t, 400, kerrors.Code(err))
		})
	}
}

func TestInstanceHTTPRejectsAmbiguousJSONAndCallerIdentity(t *testing.T) {
	for _, body := range []string{
		`{"data":{"name":"a","name":"b"}}`,
		`{"data":{}} {"data":{}}`,
		`{"data":{"tenant_id":"forged"}}`,
		`{"data":{"gpu_owner_attachment":{}}}`,
	} {
		request := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		require.Error(t, decodeInstanceHTTP(request, new(view.CreateInstanceRequest)), body)
	}
	for _, header := range []string{"X-Ani-Tenant-Id", "X-Tenant-Id", "X-Ani-Actor", "X-Ani-Operator", "X-Resource-Tenant-Id"} {
		request := httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(`{"data":{"name":"workspace"}}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(header, "forged")
		require.Error(t, decodeInstanceHTTP(request, new(view.CreateInstanceRequest)), header)
	}
	request := httptest.NewRequest("POST", "/api/v1/instances?tenant_id=forged", strings.NewReader(`{"data":{}}`))
	request.Header.Set("Content-Type", "application/json")
	require.Error(t, decodeInstanceHTTP(request, new(view.CreateInstanceRequest)))

	request = httptest.NewRequest("POST", "/api/v1/instances", strings.NewReader(`{"data":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.URL.RawPath = request.URL.Path
	require.NoError(t, decodeInstanceHTTP(request, new(view.CreateInstanceRequest)), "canonical RawPath must remain accepted")
}

func TestInstanceHTTPPresetPatchPreservesGPUClearAndFalse(t *testing.T) {
	request := httptest.NewRequest("PATCH", "/api/v1/admin/compute-presets/20000000-0000-4000-8000-000000000001", strings.NewReader(`{"data":{"gpu":null,"enabled":false},"update_mask":"gpu,enabled"}`))
	request.Header.Set("Content-Type", "application/json")
	message := new(computeview.UpdatePresetRequest)
	require.NoError(t, decodeInstanceHTTP(request, message))
	require.NotNil(t, message.Data)
	require.Nil(t, message.Data.Gpu, "gpu:null remains an explicit mask-directed clear")
	require.NotNil(t, message.Data.Enabled)
	require.False(t, *message.Data.Enabled)
	require.Equal(t, []string{"gpu", "enabled"}, message.UpdateMask.Paths)
}

func TestInstanceHTTPSettingsPresenceBeforeProtoJSON(t *testing.T) {
	for _, body := range []string{
		`{"data":{"defaults":{},"priority_class_allowlists":[]}}`,
		`{"data":{"defaults":{"pod_placement":"NONE"},"priority_class_allowlists":[{"tenant_id":1,"priority_class_names":["batch-low"]}]}}`,
	} {
		r := httptest.NewRequest("PUT", "/api/v1/admin/instance-scheduling-settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		require.NoError(t, decodeInstanceHTTP(r, new(view.UpdateInstanceSchedulingSettingsRequest)))
	}
	for _, body := range []string{
		`{}`, `{"data":null}`, `{"data":{}}`,
		`{"data":{"defaults":null,"priority_class_allowlists":[]}}`,
		`{"data":{"defaults":{},"priority_class_allowlists":null}}`,
		`{"data":{"defaults":{},"priority_class_allowlists":{}}}`,
		`{"data":{"defaults":{"pod_placement":null},"priority_class_allowlists":[]}}`,
		`{"data":{"defaults":{"pod_placement":1},"priority_class_allowlists":[]}}`,
		`{"data":{"defaults":{"priority_class_name":""},"priority_class_allowlists":[]}}`,
		`{"data":{"defaults":{"namespace":"other"},"priority_class_allowlists":[]}}`,
		`{"data":{"defaults":{},"priority_class_allowlists":[],"cluster_id":"other"}}`,
		`{"data":{"defaults":{},"defaults":{"pod_placement":"NONE"},"priority_class_allowlists":[]}}`,
	} {
		r := httptest.NewRequest("PUT", "/api/v1/admin/instance-scheduling-settings", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		err := decodeInstanceHTTP(r, new(view.UpdateInstanceSchedulingSettingsRequest))
		require.Error(t, err, body)
		require.EqualValues(t, 400, kerrors.Code(err))
	}
}

func TestInstanceQueryPagePresenceAndFilters(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		page, size  *uint32
	}{
		{name: "omitted"},
		{name: "page only", query: "page=3", page: proto.Uint32(3)},
		{name: "size only", query: "pageSize=2", size: proto.Uint32(2)},
		{name: "direct third page", query: "page=3&pageSize=2", page: proto.Uint32(3), size: proto.Uint32(2)},
		{name: "maximum", query: "page=4294967295&pageSize=100", page: proto.Uint32(4294967295), size: proto.Uint32(100)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, message := range []proto.Message{new(view.ListInstancesRequest), new(computeview.ListPresetsRequest), new(computeview.AdminListPresetsRequest)} {
				query := tc.query
				if _, tenant := message.(*computeview.ListPresetsRequest); tenant {
					query = "usage=CONTAINER_INSTANCE&" + query
				}
				require.NoError(t, decodeInstanceQuery(httptest.NewRequest("GET", "/api/v1/instances?"+query, nil), message))
				var page, size *uint32
				switch m := message.(type) {
				case *view.ListInstancesRequest:
					page, size = m.Page, m.PageSize
				case *computeview.ListPresetsRequest:
					page, size = m.Page, m.PageSize
				case *computeview.AdminListPresetsRequest:
					page, size = m.Page, m.PageSize
				}
				require.Equal(t, tc.page, page, "page and pageSize absence must remain independently visible")
				require.Equal(t, tc.size, size)
			}
		})
	}
	instances := new(view.ListInstancesRequest)
	require.NoError(t, decodeInstanceQuery(httptest.NewRequest("GET", "/api/v1/instances?instance_kind=container&compute_type=cpu&observedState=running&name=%20work%25_%20&id=%20E3DD3EC3-293C%20", nil), instances))
	require.Equal(t, "container", instances.InstanceKind)
	require.Equal(t, "cpu", instances.ComputeType)
	require.Equal(t, "running", instances.ObservedState)
	require.Equal(t, "work%_", instances.Name)
	require.Equal(t, "E3DD3EC3-293C", instances.Id)
	presets := new(computeview.ListPresetsRequest)
	require.NoError(t, decodeInstanceQuery(httptest.NewRequest("GET", "/api/v1/compute-presets?usage=CONTAINER_INSTANCE&compute_type=gpu&gpu_mode=SHARED_FIXED&code=gpu-small", nil), presets))
	require.Equal(t, "gpu", presets.ComputeType)
	require.Equal(t, "SHARED_FIXED", presets.GpuMode)
	require.Equal(t, "gpu-small", presets.Code)
	admin := new(computeview.AdminListPresetsRequest)
	require.NoError(t, decodeInstanceQuery(httptest.NewRequest("GET", "/api/v1/admin/compute-presets?enabled=false", nil), admin))
	require.NotNil(t, admin.Enabled)
	require.False(t, *admin.Enabled)
}

func TestInstanceQueryRejectsNonCanonicalPaging(t *testing.T) {
	for _, query := range []string{
		"page=0", "page=-1", "page=", "page=01", "page=%2B1", "page=1.0", "page=4294967296", "page=1&page=2",
		"pageSize=0", "pageSize=-1", "pageSize=", "pageSize=01", "pageSize=1.0", "pageSize=101", "pageSize=4294967296", "pageSize=1&pageSize=2",
		"page=1;pageSize=2", "page=%ZZ", "after_id=cursor", "afterId=cursor", "cursor=cursor", "size=20", "limit=20", "page_size=20", "sort=name",
	} {
		t.Run(query, func(t *testing.T) {
			for _, message := range []proto.Message{new(view.ListInstancesRequest), new(computeview.ListPresetsRequest), new(computeview.AdminListPresetsRequest)} {
				err := decodeInstanceQuery(httptest.NewRequest("GET", "/api/v1/instances?"+query, nil), message)
				require.Error(t, err)
				require.EqualValues(t, 400, kerrors.Code(err))
			}
		})
	}
	for _, query := range []string{"instance_kind=gpu", "compute_type=CPU", "observedState=unknown", "enabled=true", "gpu_mode=WHOLE_EXCLUSIVE", "name=" + strings.Repeat("界", 81), "id=" + strings.Repeat("a", 37)} {
		err := decodeInstanceQuery(httptest.NewRequest("GET", "/api/v1/instances?"+query, nil), new(view.ListInstancesRequest))
		require.Error(t, err, query)
	}
	for _, query := range []string{"instance_kind=container", "compute_type=CPU", "gpu_mode=unknown", "enabled=true", "name=" + strings.Repeat("界", 129), "code=" + strings.Repeat("a", 65)} {
		err := decodeInstanceQuery(httptest.NewRequest("GET", "/api/v1/compute-presets?usage=CONTAINER_INSTANCE&"+query, nil), new(computeview.ListPresetsRequest))
		require.Error(t, err, query)
	}
	err := decodeInstanceQuery(httptest.NewRequest("GET", "/api/v1/admin/compute-presets?enabled=0", nil), new(computeview.AdminListPresetsRequest))
	require.Error(t, err)
}
