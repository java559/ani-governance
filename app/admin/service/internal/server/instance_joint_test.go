//go:build quota_pg

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/coder/websocket"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	authpb "go-wind-admin/api/gen/go/authentication/service/v1"
	computeview "go-wind-admin/api/gen/go/compute/service/v1"
	identity "go-wind-admin/api/gen/go/identity/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/cmd/server/assets"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	api "go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/permissionapi"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/app/admin/service/tests/testutil"
	"go-wind-admin/pkg/authorizer"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	authz "go-wind-admin/pkg/localdeps/kratos-authz/middleware"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/middleware/auth"
	dbbootstrap "go-wind-admin/sql/bootstrap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type instancePlanEndpoint struct {
	plan        *acc.ResolvedGpuRequest
	request     *acc.GpuRequest
	client      acc.AcceleratorCatalogServiceClient
	unavailable bool
	calls       int
}

func (p *instancePlanEndpoint) ResolveGpuRequest(ctx context.Context, r *acc.ResolveGpuRequestRequest, _ ...grpc.CallOption) (*acc.ResolvedGpuRequest, error) {
	p.calls++
	if p.unavailable {
		return nil, io.EOF
	}
	if !proto.Equal(p.request, r.Gpu) {
		return nil, io.ErrUnexpectedEOF
	}
	if p.client != nil {
		return p.client.ResolveGpuRequest(ctx, r)
	}
	return proto.Clone(p.plan).(*acc.ResolvedGpuRequest), nil
}

// Real JWT/token-cache, tenant module gate, Casbin/current role snapshot, public
// DTO decoding, quota ledger/dispatch, mTLS Resource service and Resource PG.
// The Acc catalog/plan service uses real isolated PG; hardware candidates and
// external image/environment facts are controlled. No running
// Notebook, terminal, compute cleanup or GPU acceptance is claimed here.
func TestInstancePublicHTTPPersistentIntakeAndCommands(t *testing.T) {
	ctx := context.Background()
	sys := appViewer.NewSystemViewerContext(ctx)
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	fixture := uuid.NewString()
	key := make([]byte, 32)
	_, e := rand.Read(key)
	require.NoError(t, e)
	cfg := &conf.Bootstrap{Authz: &conf.Authorization{Type: "casbin"}, Authn: &conf.Authentication{Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(key)}}}
	bctx := testutil.NewBootstrapContext(cfg)
	plan, e := c.Client().Plan.Create().SetName("instance-http-" + fixture).Save(sys)
	require.NoError(t, e)
	tenant, e := c.Client().Tenant.Create().SetName("instance-http-" + fixture).SetCode("instance-http-" + fixture).SetPlanID(plan.ID).Save(sys)
	require.NoError(t, e)
	require.NoError(t, c.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleInstance).Exec(sys))
	require.NoError(t, c.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleAccelerator).Exec(sys))
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(plan.ID).SetQuotaCode(service.GpuSharedQuotaCode).SetQuotaValue(20000).Exec(sys))
	operator, e := c.Client().User.Create().SetTenantID(tenant.ID).SetUsername("instance-http-" + fixture).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, e)
	currentRole, e := c.Client().Role.Create().SetTenantID(tenant.ID).SetName("instance-http-" + fixture).SetCode("tenant:instance:http:" + fixture).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, e)
	membership, e := c.Client().UserRole.Create().SetTenantID(tenant.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).SetStatus(userrole.StatusActive).Save(sys)
	require.NoError(t, e)
	grant, e := c.Client().Permission.Create().SetName("instance-http-" + fixture).SetCode("instance:http:" + fixture).SetStatus(permission.StatusOn).Save(sys)
	require.NoError(t, e)
	require.NoError(t, c.Client().RolePermission.Create().SetTenantID(tenant.ID).SetRoleID(currentRole.ID).SetPermissionID(grant.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Exec(sys))
	catalog, e := dbbootstrap.Catalog(assets.OpenApiData)
	require.NoError(t, e)
	requiredRoutes := map[string]bool{
		"POST " + data.InstanceCreatePath:   true,
		"GET " + data.InstanceCreatePath:    true,
		"DELETE " + data.InstanceDeletePath: true,
		"POST " + data.InstanceStartPath:    true,
		"POST " + data.InstanceStopPath:     true,
		"GET " + data.InstanceGetPath:       true,
		"GET " + data.InstanceOperationPath: true,
		"POST " + data.InstanceExecPath:     true,
	}
	routes := 0
	for _, entry := range catalog {
		if entry.Module != "InstanceService" {
			continue
		}
		if strings.HasPrefix(entry.Path, "/api/v1/admin/") {
			continue
		}
		if requiredRoutes[entry.Method+" "+entry.Path] {
			routes++
		}
		require.Equal(t, "INSTANCE", entry.BusinessModule)
		route, e := c.Client().Api.Query().Where(api.PathEQ(entry.Path), api.MethodEQ(entry.Method)).Only(sys)
		if ent.IsNotFound(e) {
			route, e = c.Client().Api.Create().SetModule(entry.Module).SetOperation(entry.Operation).SetPath(entry.Path).SetMethod(entry.Method).SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleInstance).SetStatus(api.StatusOn).Save(sys)
		}
		require.NoError(t, e)
		require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(grant.ID).SetAPIID(route.ID).Exec(sys))
	}
	require.Equal(t, 8, routes, "instance lifecycle routes remain registered; compute has its own owner")
	// Shared API and permission catalogs survive task tenant cleanup. Reuse
	// their exact records so a preceding fixture or retry cannot duplicate them.
	computeRoute, e := c.Client().Api.Query().Where(api.ModuleEQ("ComputePresetService"), api.PathEQ(data.ComputeCatalogPath), api.MethodEQ("GET"), api.ScopeEQ(api.ScopeAdmin)).Only(sys)
	if ent.IsNotFound(e) {
		computeRoute, e = c.Client().Api.Create().SetModule("ComputePresetService").SetOperation("ComputePresetService_ListPresets").SetPath(data.ComputeCatalogPath).SetMethod("GET").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleSystem).SetStatus(api.StatusOn).Save(sys)
	}
	require.NoError(t, e)
	require.NotNil(t, computeRoute.BusinessModule)
	require.Equal(t, api.BusinessModuleSystem, *computeRoute.BusinessModule)
	require.NotNil(t, computeRoute.Status)
	require.Equal(t, api.StatusOn, *computeRoute.Status)
	computeGrant, e := c.Client().Permission.Query().Where(permission.CodeEQ("instance:preset:read")).Only(sys)
	if ent.IsNotFound(e) {
		computeGrant, e = c.Client().Permission.Create().SetName("instance:preset:read").SetCode("instance:preset:read").SetStatus(permission.StatusOn).Save(sys)
	}
	require.NoError(t, e)
	require.NotNil(t, computeGrant.Status)
	require.Equal(t, permission.StatusOn, *computeGrant.Status)
	linked, e := c.Client().PermissionApi.Query().Where(permissionapi.PermissionIDEQ(computeGrant.ID), permissionapi.APIIDEQ(computeRoute.ID)).Exist(sys)
	require.NoError(t, e)
	if !linked {
		require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(computeGrant.ID).SetAPIID(computeRoute.ID).Exec(sys))
	}
	require.NoError(t, c.Client().RolePermission.Create().SetTenantID(tenant.ID).SetRoleID(currentRole.ID).SetPermissionID(computeGrant.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Exec(sys))
	catalogRoute, e := c.Client().Api.Query().Where(api.PathEQ("/api/v1/accelerator/profiles"), api.MethodEQ("GET"), api.ScopeEQ(api.ScopeAdmin)).Only(sys)
	if ent.IsNotFound(e) {
		catalogRoute, e = c.Client().Api.Create().SetModule("AcceleratorService").SetPath("/api/v1/accelerator/profiles").SetMethod("GET").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleAccelerator).SetStatus(api.StatusOn).Save(sys)
	}
	require.NoError(t, e)
	require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(grant.ID).SetAPIID(catalogRoute.ID).Exec(sys))
	var gpu *acc.ResolvedGpuRequest
	var gpuRequest *acc.GpuRequest
	var gpuProfile *acc.GpuProfile
	lifecycle := os.Getenv("INSTANCE_JOINT_LIFECYCLE") == "1"
	ledger := data.NewQuotaLedgerRepo(bctx, c)
	var accelerator *data.AcceleratorClient
	actorID := strconv.FormatUint(uint64(operator.ID), 10)
	{
		peer := testutil.StartInstanceAcceleratorPeer(t, tenant.ResourceTenantID, actorID)
		var closeAcc func()
		accelerator, closeAcc, e = data.NewAcceleratorClient(data.AcceleratorClientConfig{Address: peer.Address, ServerName: peer.ServerName, CAFile: peer.CAFile, CertFile: peer.CertFile, KeyFile: peer.KeyFile, Timeout: 3 * time.Second})
		require.NoError(t, e)
		t.Cleanup(closeAcc)
		gpuRequest = peer.Request
		gpu, e = accelerator.Catalog.ResolveGpuRequest(ctx, &acc.ResolveGpuRequestRequest{Context: &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant.ResourceTenantID, Actor: &acc.Actor{Type: "user", Id: actorID}}, Gpu: peer.Request})
		require.NoError(t, e)
	}
	gpuProfile, e = accelerator.Catalog.GetProfile(ctx, &acc.GetProfileRequest{Context: &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant.ResourceTenantID, Actor: &acc.Actor{Type: "user", Id: actorID}}, ProfileId: gpuRequest.ProfileId, ProfileVersion: gpuRequest.ProfileVersion})
	require.NoError(t, e)

	if lifecycle {
		dir, _ := quotaTLSFixture(t)
		quotaServer, e := NewQuotaInternalServer(QuotaInternalServerConfig{Enabled: true, Address: "127.0.0.1:0", CAFile: filepath.Join(dir, "ca.pem"), CertFile: filepath.Join(dir, "server.pem"), KeyFile: filepath.Join(dir, "server.key"), CertOwnerMap: map[string]string{"ani-resource-service": service.InstanceOwner}}, ledger)
		require.NoError(t, e)
		done := make(chan error, 1)
		go func() { done <- quotaServer.Start(ctx) }()
		t.Cleanup(func() { require.NoError(t, quotaServer.Stop(ctx)); require.NoError(t, <-done) })
		config, e := json.Marshal(map[string]string{"Address": quotaServer.listener.Addr().String(), "CAFile": filepath.Join(dir, "ca.pem"), "CertFile": filepath.Join(dir, "resource.pem"), "KeyFile": filepath.Join(dir, "resource.key"), "ServerName": "ani-governance"})
		require.NoError(t, e)
		path := filepath.Join(t.TempDir(), "quota.json")
		require.NoError(t, os.WriteFile(path, config, 0600))
		t.Setenv("INSTANCE_JOINT_QUOTA_FILE", path)
	}
	resourcePeer := testutil.StartInstanceResourcePeer(t, gpuRequest, gpuProfile)
	owner, computeOwner, closeOwner, e := data.NewResourceClientsFromEnv()
	require.NoError(t, e)
	t.Cleanup(closeOwner)
	// This suite covers the current frozen instance admission contract.
	t.Setenv("ANI_INSTANCE_CONFIG_V2_WRITES", "true")
	binding := service.NewInstanceGpuBinding(owner, data.NewInstanceAuthorizationRepo(c), resourcePeer.ClusterID)
	registry := service.NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(binding))
	worker := service.NewQuotaDispatchWorker(bctx, ledger, registry)
	require.NoError(t, worker.Start(ctx))
	t.Cleanup(func() { require.NoError(t, worker.Stop(ctx)) })
	plans := &instancePlanEndpoint{plan: gpu, request: gpuRequest}
	if accelerator != nil {
		plans.client = accelerator.Catalog
	}
	tenants := data.NewTenantRepo(bctx, c)
	acceptance, e := service.NewGpuAcceptance(ledger, registry, tenants, plans, binding, worker)
	require.NoError(t, e)
	var sessions service.InstanceSessionClient
	terminalEnabled := lifecycle && os.Getenv("SG_INSTANCE_TEST_BINARY") != ""
	if terminalEnabled {
		testutil.StartInstanceSessionPeer(t, resourcePeer, gpuRequest.ClusterId)
		client, close, e := data.NewInstanceSessionClientFromEnv()
		require.NoError(t, e)
		t.Cleanup(close)
		sessions = client
	}
	handler := service.NewInstanceService(acceptance, binding, tenants, ledger, sessions, service.InstanceCatalogConfig{ClusterID: resourcePeer.ClusterID})
	if lifecycle {
		sync := service.NewGpuUsageSyncWorker(ledger, accelerator, func(e error) { t.Log("Acc sync attempt failed", e) })
		require.NoError(t, sync.Start(ctx))
		t.Cleanup(func() { require.NoError(t, sync.Stop(ctx)) })
	}
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	tokens := data.NewAuthenticator(bctx, data.NewUserTokenCache(bctx, rdb))
	checker := data.NewTokenChecker(bctx, tokens, authpb.ClientType_admin)
	roles := data.NewRoleRepo(bctx, c, data.NewRolePermissionRepo(bctx, c), data.NewRoleOrgUnitRepo(bctx, c), data.NewPermissionRepo(bctx, c, data.NewPermissionApiRepo(bctx, c), data.NewPermissionMenuRepo(bctx, c)), data.NewRoleMetadataRepo(bctx, c), data.NewRoleFieldPermissionRepo(bctx, c))
	policy := authorizer.NewAuthorizer(bctx, data.NewAuthorizerProvider(bctx, roles, data.NewApiRepo(bctx, c)))
	require.NoError(t, policy.ResetPolicies(ctx))
	token, _, e := tokens.CreateUserToken(ctx, authpb.ClientType_admin, &authpb.UserTokenPayload{UserId: operator.ID, TenantId: trans.Ptr(tenant.ID), Roles: []string{*currentRole.Code}, DataScopes: []identity.DataScope{identity.DataScope_ALL}, Application: authpb.ApplicationType_console.Enum(), Sid: trans.Ptr(uuid.NewString())})
	require.NoError(t, e)
	server := khttp.NewServer(khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(data.NewTenantAccessCheckerImpl(bctx, c)), auth.WithInjectMetadata(false), auth.WithInjectEnt(true)), authz.Server(policy.Engine())))
	RegisterInstanceHTTPServer(server, handler)
	RegisterComputeHTTPServer(server, service.NewComputePresetService(computeOwner, service.ComputeCatalogConfig{ClusterID: resourcePeer.ClusterID, Compute: computeOwner, Catalog: accelerator.Catalog, Resolver: tenants, Authorization: data.NewComputeCatalogAuthorizationRepo(c)}))
	host := httptest.NewServer(server)
	t.Cleanup(host.Close)
	request := func(method, path string, in proto.Message, out proto.Message, status int, bearer string) {
		t.Helper()
		var body []byte
		if in != nil {
			body, e = protojson.Marshal(in)
			require.NoError(t, e)
		}
		r, e := http.NewRequest(method, host.URL+path, bytes.NewReader(body))
		require.NoError(t, e)
		r.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		response, e := host.Client().Do(r)
		require.NoError(t, e)
		defer response.Body.Close()
		raw, e := io.ReadAll(response.Body)
		require.NoError(t, e)
		require.Equalf(t, status, response.StatusCode, "%s %s: %s", method, path, raw)
		if out != nil && response.StatusCode == status {
			require.NoError(t, protojson.Unmarshal(raw, out))
		}
		require.NotContains(t, string(raw), "registry.invalid")
		t.Logf("HTTP %s %s -> %d", method, path, response.StatusCode)
	}
	public := &view.CreateInstanceRequest{Data: &view.CreateInstanceData{IdempotencyKey: uuid.NewString(), Name: "workspace", ImageId: "img_" + strings.ReplaceAll(uuid.NewString(), "-", ""), ImageScope: "platform", PresetId: resourcePeer.PresetID}}
	request("POST", "/api/v1/instances", public, nil, 401, "")
	accepted := new(view.Acceptance)
	request("POST", "/api/v1/instances", public, accepted, 202, token)
	require.NotEmpty(t, accepted.OperationId)
	require.NotEmpty(t, accepted.ResourceId)
	require.Eventually(t, func() bool {
		op, e := ledger.GetOperationForUser(sys, tenant.ID, accepted.OperationId)
		return e == nil && op.DispatchState == quotaoperation.DispatchStateAcked
	}, 5*time.Second, 20*time.Millisecond, "real Resource ACK via production dispatch required")
	plans.unavailable = true
	replay := new(view.Acceptance)
	request("POST", "/api/v1/instances", public, replay, 202, token)
	require.True(t, replay.Replayed)
	require.Equal(t, accepted.OperationId, replay.OperationId)
	require.Equal(t, 1, plans.calls)
	for _, field := range []string{"env", "labels"} {
		encoded, err := protojson.Marshal(public)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(encoded, &payload))
		object := payload["data"].(map[string]any)
		if field == "env" {
			object[field] = []any{}
		} else {
			object[field] = map[string]any{}
		}
		for _, configured := range []bool{false, true} {
			if configured {
				if field == "env" {
					object[field] = []any{map[string]any{"name": "BUSINESS_VALUE", "value": " <literal> "}}
				} else {
					object[field] = map[string]any{"team": "science"}
				}
			}
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			rawRequest, err := http.NewRequest("POST", host.URL+"/api/v1/instances", bytes.NewReader(body))
			require.NoError(t, err)
			rawRequest.Header.Set("Content-Type", "application/json")
			rawRequest.Header.Set("Authorization", "Bearer "+token)
			response, err := host.Client().Do(rawRequest)
			require.NoError(t, err)
			raw, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			if configured {
				require.Equal(t, http.StatusConflict, response.StatusCode, "%s changed current configuration: %s", field, raw)
			} else {
				require.Equal(t, http.StatusAccepted, response.StatusCode, "%s explicit empty current configuration: %s", field, raw)
				var emptyReplay view.Acceptance
				require.NoError(t, protojson.Unmarshal(raw, &emptyReplay))
				require.True(t, emptyReplay.Replayed)
				require.Equal(t, accepted.OperationId, emptyReplay.OperationId)
			}
		}
	}
	require.Equal(t, 1, plans.calls, "current replay and configuration conflicts cannot re-resolve or incur new quota")
	instance := new(view.Instance)
	request("GET", "/api/v1/instances/"+accepted.ResourceId, nil, instance, 200, token)
	require.EqualValues(t, 1, instance.Version)
	require.Equal(t, accepted.OperationId, instance.CreateOperationId)
	require.Equal(t, "container", instance.InstanceKind)
	require.Equal(t, "gpu", instance.ComputeType)
	require.Equal(t, resourcePeer.PresetID, instance.PresetId)
	require.NotEmpty(t, instance.Cpu)
	require.NotEmpty(t, instance.Memory)
	require.NotEmpty(t, instance.CreatedAt)
	require.NotNil(t, instance.GpuSummary)
	require.Equal(t, gpuRequest.ProfileId, instance.GpuSummary.ProfileId)
	require.NotNil(t, instance.Scheduling)
	require.Equal(t, view.SchedulingOptions_NONE, instance.Scheduling.GetPodPlacement())
	if lifecycle {
		require.Eventually(t, func() bool {
			i, e := owner.Get(ctx, tenant.ResourceTenantID, "governance:user:"+actorID, accepted.ResourceId)
			return e == nil && i.ObservedState == "running"
		}, 30*time.Second, 100*time.Millisecond, "real worker must observe controlled Kubernetes running")
	}
	listed := new(view.ListInstancesResponse)
	request("GET", "/api/v1/instances", nil, listed, 200, token)
	require.Len(t, listed.Items, 1)
	require.EqualValues(t, 1, listed.Total)
	thirdPage := new(view.ListInstancesResponse)
	request("GET", "/api/v1/instances?page=3&pageSize=1", nil, thirdPage, 200, token)
	require.Empty(t, thirdPage.Items)
	require.EqualValues(t, 1, thirdPage.Total, "empty page retains authorized matching total")
	operation := new(view.Operation)
	request("GET", "/api/v1/instance-operations/"+accepted.OperationId, nil, operation, 200, token)
	require.Equal(t, accepted.ResourceId, operation.InstanceId)
	presets := new(computeview.ListPresetsResponse)
	request("GET", "/api/v1/compute-presets?usage=CONTAINER_INSTANCE", nil, presets, 200, token)
	require.Len(t, presets.Items, 1)
	require.EqualValues(t, 1, presets.Total)
	require.Equal(t, resourcePeer.PresetID, presets.Items[0].Id)
	require.Equal(t, "gpu", presets.Items[0].ComputeType)
	require.NotNil(t, presets.Items[0].Gpu)
	require.Equal(t, "OK", presets.GpuCatalogStatus)
	t.Run("new Gov tenant and user need no Accelerator provisioning", func(t *testing.T) {
		secondPlan, err := c.Client().Plan.Create().SetName("new-gpu-user-" + fixture).Save(sys)
		require.NoError(t, err)
		secondTenant, err := c.Client().Tenant.Create().SetName("new-gpu-user-" + fixture).SetCode("new-gpu-user-" + fixture).SetPlanID(secondPlan.ID).Save(sys)
		require.NoError(t, err)
		require.NoError(t, c.Client().PlanModule.Create().SetPlanID(secondPlan.ID).SetModule(planmodule.ModuleInstance).Exec(sys))
		acceleratorModule, err := c.Client().PlanModule.Create().SetPlanID(secondPlan.ID).SetModule(planmodule.ModuleAccelerator).Save(sys)
		require.NoError(t, err)
		secondUser, err := c.Client().User.Create().SetTenantID(secondTenant.ID).SetUsername("new-gpu-user-" + fixture).SetStatus(user.StatusNormal).Save(sys)
		require.NoError(t, err)
		secondRole, err := c.Client().Role.Create().SetTenantID(secondTenant.ID).SetName("new-gpu-user-" + fixture).SetCode("tenant:gpu:new:" + fixture).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
		require.NoError(t, err)
		require.NoError(t, c.Client().UserRole.Create().SetTenantID(secondTenant.ID).SetUserID(secondUser.ID).SetRoleID(secondRole.ID).SetStatus(userrole.StatusActive).Exec(sys))
		secondGrant, err := c.Client().RolePermission.Create().SetTenantID(secondTenant.ID).SetRoleID(secondRole.ID).SetPermissionID(grant.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
		require.NoError(t, err)
		secondComputeGrant, err := c.Client().RolePermission.Create().SetTenantID(secondTenant.ID).SetRoleID(secondRole.ID).SetPermissionID(computeGrant.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
		require.NoError(t, err)
		require.NoError(t, policy.ResetPolicies(ctx))
		secondToken, _, err := tokens.CreateUserToken(ctx, authpb.ClientType_admin, &authpb.UserTokenPayload{UserId: secondUser.ID, TenantId: trans.Ptr(secondTenant.ID), Roles: []string{*secondRole.Code}, DataScopes: []identity.DataScope{identity.DataScope_ALL}, Application: authpb.ApplicationType_console.Enum(), Sid: trans.Ptr(uuid.NewString())})
		require.NoError(t, err)
		const catalogPath = "/api/v1/compute-presets?usage=CONTAINER_INSTANCE&page=1&pageSize=20&compute_type=gpu"
		query := func(wantCatalog string) {
			result := new(computeview.ListPresetsResponse)
			request("GET", catalogPath, nil, result, 200, secondToken)
			require.Equal(t, wantCatalog, result.GpuCatalogStatus)
			if wantCatalog == "OK" {
				require.Len(t, result.Items, 1)
				require.Equal(t, gpuRequest.ProfileId, result.Items[0].Gpu.ProfileId)
			} else {
				require.Empty(t, result.Items)
			}
		}
		// The existing Accelerator process/configuration remains untouched throughout.
		query("OK")
		require.NoError(t, c.Client().RolePermission.UpdateOneID(secondComputeGrant.ID).SetStatus(rolepermission.StatusOff).Exec(sys))
		require.NoError(t, policy.ResetPolicies(ctx))
		request("GET", catalogPath, nil, nil, 403, secondToken)
		require.NoError(t, c.Client().RolePermission.UpdateOneID(secondComputeGrant.ID).SetStatus(rolepermission.StatusOn).Exec(sys))
		require.NoError(t, c.Client().RolePermission.UpdateOneID(secondGrant.ID).SetStatus(rolepermission.StatusOff).Exec(sys))
		require.NoError(t, policy.ResetPolicies(ctx))
		query("NO_PERMISSION")
		require.NoError(t, c.Client().RolePermission.UpdateOneID(secondGrant.ID).SetStatus(rolepermission.StatusOn).Exec(sys))
		require.NoError(t, policy.ResetPolicies(ctx))
		require.NoError(t, c.Client().PlanModule.DeleteOneID(acceleratorModule.ID).Exec(sys))
		query("NO_PERMISSION")
		require.NoError(t, c.Client().PlanModule.Create().SetPlanID(secondPlan.ID).SetModule(planmodule.ModuleAccelerator).Exec(sys))
		query("OK")
	})
	change := func(action string, version int64) {
		r := &view.ChangeInstanceRequest{Data: &view.ChangeInstanceData{IdempotencyKey: uuid.NewString(), ExpectedVersion: version}}
		ack := new(view.Acceptance)
		request("POST", "/api/v1/instances/"+accepted.ResourceId+":"+action, r, ack, 202, token)
		require.Equal(t, accepted.ResourceId, ack.ResourceId)
	}
	execTicket := func(version int64) *view.ExecInstanceResponse {
		reply := new(view.ExecInstanceResponse)
		request("POST", "/api/v1/instances/"+accepted.ResourceId+"/exec", &view.ExecInstanceRequest{Data: &view.ExecInstanceData{IdempotencyKey: uuid.NewString(), ExpectedVersion: version, Rows: 24, Cols: 80}}, reply, 202, token)
		return reply
	}
	terminalCommand := func(ticket *view.ExecInstanceResponse, command string, want string) {
		t.Helper()
		deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		connection, response, e := websocket.Dial(deadline, ticket.WsUrl, &websocket.DialOptions{Subprotocols: []string{"ani.terminal.v1"}, HTTPHeader: http.Header{"Origin": []string{"http://instance-fixture"}}})
		if response != nil && e != nil {
			_ = response.Body.Close()
		}
		require.NoError(t, e)
		defer connection.CloseNow()
		raw, _ := json.Marshal(map[string]string{"type": "stdin", "data": command})
		require.NoError(t, connection.Write(deadline, websocket.MessageText, raw))
		output := ""
		for {
			_, raw, e := connection.Read(deadline)
			require.NoError(t, e)
			var frame struct {
				Type, Data string
				Code       json.RawMessage
			}
			require.NoError(t, json.Unmarshal(raw, &frame))
			if frame.Type == "stdout" {
				output += frame.Data
			}
			if frame.Type == "error" {
				t.Fatal("terminal error", frame.Code)
			}
			if frame.Type == "exit" {
				break
			}
		}
		require.Contains(t, output, want)
	}
	var staleTicket, replacedTicket *view.ExecInstanceResponse
	if terminalEnabled {
		terminalCommand(execTicket(1), "printf retained-workspace > instance-proof.txt; cat instance-proof.txt; exit\n", "retained-workspace")
		staleTicket = execTicket(1)
		replacedTicket = execTicket(1)
	}
	change("stop", 1)
	if lifecycle {
		require.Eventually(t, func() bool {
			i, e := owner.Get(ctx, tenant.ResourceTenantID, "governance:user:"+actorID, accepted.ResourceId)
			return e == nil && i.ObservedState == "stopped"
		}, 25*time.Second, 100*time.Millisecond, "Stop must finish before Start")
	}
	if terminalEnabled {
		deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
		connection, _, e := websocket.Dial(deadline, staleTicket.WsUrl, &websocket.DialOptions{Subprotocols: []string{"ani.terminal.v1"}, HTTPHeader: http.Header{"Origin": []string{"http://instance-fixture"}}})
		require.NoError(t, e)
		_, raw, e := connection.Read(deadline)
		require.NoError(t, e)
		var frame struct{ Type, Code string }
		require.NoError(t, json.Unmarshal(raw, &frame))
		require.Equal(t, "error", frame.Type, "stopped old ticket must not connect an exec stream")
		_ = connection.CloseNow()
		cancel()
	}
	change("start", 2)
	if lifecycle {
		require.Eventually(t, func() bool {
			i, e := owner.Get(ctx, tenant.ResourceTenantID, "governance:user:"+actorID, accepted.ResourceId)
			return e == nil && i.ObservedState == "running"
		}, 25*time.Second, 100*time.Millisecond, "Start must use original plan/PVC")
	}
	if terminalEnabled {
		terminalCommand(execTicket(3), "cat instance-proof.txt; exit\n", "retained-workspace")
	}
	if terminalEnabled {
		deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
		connection, _, e := websocket.Dial(deadline, replacedTicket.WsUrl, &websocket.DialOptions{Subprotocols: []string{"ani.terminal.v1"}, HTTPHeader: http.Header{"Origin": []string{"http://instance-fixture"}}})
		require.NoError(t, e)
		_, raw, e := connection.Read(deadline)
		require.NoError(t, e)
		var frame struct{ Type, Code string }
		require.NoError(t, json.Unmarshal(raw, &frame))
		require.Equal(t, "error", frame.Type, "old ticket must not target restarted Pod")
		_ = connection.CloseNow()
		cancel()
	}
	request("GET", "/api/v1/instances/"+accepted.ResourceId, nil, instance, 200, token)
	require.EqualValues(t, 3, instance.Version)
	require.Equal(t, accepted.OperationId, instance.CreateOperationId)
	deleted := new(view.Acceptance)
	request("DELETE", "/api/v1/instances/"+accepted.ResourceId, &view.ChangeInstanceRequest{Data: &view.ChangeInstanceData{IdempotencyKey: uuid.NewString(), ExpectedVersion: 3}}, deleted, 202, token)
	require.Eventually(t, func() bool {
		op, e := ledger.GetOperationForUser(sys, tenant.ID, deleted.OperationId)
		return e == nil && op.DispatchState == quotaoperation.DispatchStateAcked
	}, 5*time.Second, 20*time.Millisecond, "delete ACK required")
	if lifecycle {
		require.Eventually(t, func() bool {
			i, e := owner.Get(ctx, tenant.ResourceTenantID, "governance:user:"+actorID, accepted.ResourceId)
			return e == nil && i.ObservedState == "deleted" && i.ComputeCleaned && i.WorkspaceCleaned && i.QuotaReturned
		}, 50*time.Second, 100*time.Millisecond, "real worker/outbox must obtain actual Gov receipt and complete controlled cleanup")
	}
	balances, e := ledger.RecomputeInvariants(ctx, tenant.ID)
	require.NoError(t, e)
	for _, balance := range balances {
		require.True(t, balance.Balanced)
		if strings.HasPrefix(balance.QuotaCode, "gpu.") {
			if lifecycle {
				require.Zero(t, balance.OccupiedUnits)
			} else {
				expected := int64(0)
				for _, item := range gpu.QuotaItems {
					if item.QuotaCode == balance.QuotaCode {
						expected += item.Units
					}
				}
				require.EqualValues(t, expected, balance.OccupiedUnits, "intake-only test cannot manufacture cleanup/return")
			}
		}
	}
	if lifecycle {
		require.Eventually(t, func() bool {
			usage, e := accelerator.Usage.GetGpuUsage(ctx, &acc.GetGpuUsageRequest{Context: &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant.ResourceTenantID, Actor: &acc.Actor{Type: "user", Id: actorID}}, Ref: &acc.GpuUsageRef{TenantId: tenant.ResourceTenantID, OwnerService: service.InstanceOwner, ResourceId: accepted.ResourceId, CreateOperationId: accepted.OperationId}})
			return e == nil && usage.State == acc.UsageState_ENDED
		}, 15*time.Second, 100*time.Millisecond, "Acc ENDED must be read from its real PG service after original ledger release")
	}
	require.NoError(t, c.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusDisabled).Exec(sys))
	request("POST", "/api/v1/instances", public, nil, 403, token)
}
