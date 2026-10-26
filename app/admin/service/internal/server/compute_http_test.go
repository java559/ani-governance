package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// The external Resource endpoint records the exact forwarded protocol. Gov's
// real constructor, HTTP adapter, strict decoder and platform authorization run.
type computeHTTPOwner struct {
	method     string
	request    proto.Message
	calls      int
	failUpdate bool
}

func (p *computeHTTPOwner) record(method, actor string, request proto.Message) {
	if actor != "governance:user:9" {
		panic("untrusted actor")
	}
	p.calls++
	p.method, p.request = method, proto.Clone(request)
}
func computeHTTPPreset() *compute.Preset {
	return &compute.Preset{Id: "30000000-0000-4000-8000-000000000003", Code: "cpu-test", Name: "CPU", ComputeType: "cpu", CpuMillis: 1000, MemoryMib: 1024, Enabled: true}
}
func (p *computeHTTPOwner) AdminListPresets(_ context.Context, actor string, r *compute.AdminListPresetsRequest) (*compute.ListPresetsResponse, error) {
	p.record("list", actor, r)
	return &compute.ListPresetsResponse{Items: []*compute.Preset{computeHTTPPreset()}, Total: 7}, nil
}
func (p *computeHTTPOwner) AdminGetPreset(_ context.Context, actor string, r *compute.AdminGetPresetRequest) (*compute.Preset, error) {
	p.record("get", actor, r)
	return computeHTTPPreset(), nil
}
func (p *computeHTTPOwner) AdminCreatePreset(_ context.Context, actor string, r *compute.AdminCreatePresetRequest) (*compute.Preset, error) {
	p.record("create", actor, r)
	return computeHTTPPreset(), nil
}
func (p *computeHTTPOwner) AdminUpdatePreset(_ context.Context, actor string, r *compute.AdminUpdatePresetRequest) (*compute.Preset, error) {
	p.record("update", actor, r)
	if p.failUpdate {
		return nil, status.Error(codes.Unavailable, "private downstream DSN")
	}
	return computeHTTPPreset(), nil
}
func (p *computeHTTPOwner) AdminSetPresetEnabled(_ context.Context, actor string, r *compute.AdminSetPresetEnabledRequest) (*compute.Preset, error) {
	p.record("enabled", actor, r)
	return computeHTTPPreset(), nil
}
func (p *computeHTTPOwner) AdminDeletePreset(_ context.Context, actor string, r *compute.AdminDeletePresetRequest) (*emptypb.Empty, error) {
	p.record("delete", actor, r)
	return &emptypb.Empty{}, nil
}

func TestComputeHTTPManagementOwnerAuthorizationAndFieldMask(t *testing.T) {
	owner := &computeHTTPOwner{}
	claims := &authv1.UserTokenPayload{UserId: 9, Roles: []string{"platform:catalog"}, Permissions: []string{"compute:preset:admin:list", "compute:preset:admin:read", "compute:preset:admin:create", "compute:preset:admin:update", "compute:preset:admin:delete"}}
	server := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, request any) (any, error) {
			return next(auth.NewContext(ctx, claims), request)
		}
	}))
	RegisterComputeHTTPServer(server, service.NewComputePresetService(owner, service.ComputeCatalogConfig{}))
	RegisterInstanceHTTPServer(server, service.NewInstanceService(nil, nil, nil, nil, nil))
	fire := func(method, path, body string, code int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		require.Equal(t, code, response.Code, response.Body.String())
		return response
	}
	const collection = "/api/v1/admin/compute-presets"
	const detail = collection + "/30000000-0000-4000-8000-000000000003"
	response := fire("GET", collection+"?page=3&pageSize=2&enabled=false", "", 200)
	list := owner.request.(*compute.AdminListPresetsRequest)
	require.EqualValues(t, 3, list.Page)
	require.EqualValues(t, 2, list.PageSize)
	require.NotNil(t, list.Enabled)
	require.False(t, *list.Enabled)
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	require.Equal(t, "7", body["total"])
	require.Len(t, body["items"], 1)
	require.NotContains(t, body, "data")
	fire("POST", collection, `{"data":{"code":"cpu-test","name":"CPU","compute_type":"cpu","cpu_millis":"1000","memory_mib":"1024"}}`, 201)
	require.Equal(t, "create", owner.method)
	fire("GET", detail, "", 200)
	require.Equal(t, "get", owner.method)
	fire("PATCH", detail, `{"data":{"description":""},"update_mask":"description"}`, 200)
	update := owner.request.(*compute.AdminUpdatePresetRequest)
	require.Equal(t, []string{"description"}, update.UpdateMask.Paths)
	require.Nil(t, update.Data.Gpu)
	require.Nil(t, update.Data.Enabled)
	fire("PATCH", detail, `{"data":{"gpu":null},"update_mask":"gpu"}`, 200)
	update = owner.request.(*compute.AdminUpdatePresetRequest)
	require.Equal(t, []string{"gpu"}, update.UpdateMask.Paths)
	require.Nil(t, update.Data.Gpu)
	fire("POST", detail+":set-enabled", `{"data":{"enabled":false}}`, 200)
	require.False(t, *owner.request.(*compute.AdminSetPresetEnabledRequest).Enabled)
	response = fire("DELETE", detail, "", 204)
	require.Equal(t, "delete", owner.method)
	require.Empty(t, response.Body.String())
	for _, request := range []struct{ method, path, body string }{
		{"PATCH", detail, `{"id":"different","data":{"name":"new"},"update_mask":"name"}`},
		{"PATCH", detail, `{"data":{"name":"new"},"update_mask":"name,name"}`},
		{"PATCH", detail, `{"data":{"code":"immutable"},"update_mask":"name"}`},
		{"GET", collection + "?enabled=0", ""},
	} {
		before := owner.calls
		fire(request.method, request.path, request.body, 400)
		require.Equal(t, before, owner.calls)
	}
	for _, denied := range []*authv1.UserTokenPayload{
		{UserId: 9, Roles: []string{"platform:catalog"}, Permissions: []string{"instance:preset:admin:list"}},
		{UserId: 9, TenantId: proto.Uint32(7), Roles: claims.Roles, Permissions: claims.Permissions},
		{UserId: 9, Roles: []string{"tenant:manager"}, Permissions: claims.Permissions},
	} {
		original := claims
		claims = denied
		before := owner.calls
		fire("GET", collection, "", 403)
		require.Equal(t, before, owner.calls)
		claims = original
	}
	owner.failUpdate = true
	response = fire("PATCH", detail, `{"data":{"name":"new"},"update_mask":"name"}`, 503)
	require.NotContains(t, response.Body.String(), "private downstream")
}

func TestComputeHTTPRequiresUsageAndDeletesOldRoute(t *testing.T) {
	server := khttp.NewServer()
	RegisterComputeHTTPServer(server, service.NewComputePresetService(nil, service.ComputeCatalogConfig{}))
	RegisterInstanceHTTPServer(server, service.NewInstanceService(nil, nil, nil, nil, nil))
	for _, query := range []string{"", "usage=", "usage=unknown", "usage=TRAINING&usage=INFERENCE", "usage=0", "usage=TRAINING&tenant_id=1"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/compute-presets?"+query, nil))
		require.Equal(t, 400, response.Code, query+response.Body.String())
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/instance-presets", nil))
	require.Equal(t, 404, response.Code)
	for _, usage := range []string{"CONTAINER_INSTANCE", "TRAINING", "INFERENCE", "VIRTUAL_MACHINE"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/compute-presets?usage="+usage, nil))
		require.NotEqual(t, 200, response.Code, "anonymous must not reach owner")
	}
}
