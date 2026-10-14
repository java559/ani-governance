package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	authzMiddleware "go-wind-admin/pkg/localdeps/kratos-authz/middleware"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	annotations "google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"

	admin "go-wind-admin/api/gen/go/admin/service/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/cmd/server/assets"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	apiEntity "go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/permissionapi"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/pkg/authorizer"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/middleware/auth"
	dbbootstrap "go-wind-admin/sql/bootstrap"
)

//go:embed testdata/accelerator_ledger_snapshot.sql
var acceleratorLedgerSnapshotSQL string

// This explicit test assembly uses the production JWT/token-cache, tenant
// module checker, policy provider, Casbin middleware, generated HTTP handlers,
// BFF, ledger readers and Accelerator mTLS client. No login endpoint or owner
// business endpoint is invented. Session issuance is a test fixture.
func TestAcceleratorJointHTTP(t *testing.T) {
	dsnFile := os.Getenv("GOV_ACC_JOINT_DSN_FILE")
	if dsnFile == "" {
		t.Skip("requires isolated joint B PostgreSQL, Redis and Accelerator helper")
	}
	ctx := context.Background()
	sys := appViewer.NewSystemViewerContext(ctx)
	dsn, e := os.ReadFile(dsnFile)
	require.NoError(t, e)
	db, e := sql.Open("pgx", strings.TrimSpace(string(dsn)))
	require.NoError(t, e)
	t.Cleanup(func() { _ = db.Close() })
	driver := entsql.OpenDB(dialect.Postgres, db)
	runtimeClient := ent.NewClient(ent.Driver(driver))
	ec := entCrud.NewEntClient[*ent.Client](runtimeClient, driver)
	// Explicit fixture/catalog mutations use the migration identity; every
	// request, authorization read, ledger read and downstream call uses runtime.
	adminDSN, e := os.ReadFile(os.Getenv("GOV_ACC_JOINT_ADMIN_DSN_FILE"))
	require.NoError(t, e)
	fixtureDB, e := sql.Open("pgx", strings.TrimSpace(string(adminDSN)))
	require.NoError(t, e)
	t.Cleanup(func() { _ = fixtureDB.Close() })
	fixtureDriver := entsql.OpenDB(dialect.Postgres, fixtureDB)
	client := ent.NewClient(ent.Driver(fixtureDriver))
	fixtureEC := entCrud.NewEntClient[*ent.Client](client, fixtureDriver)
	randomKey := make([]byte, 32)
	_, e = rand.Read(randomKey)
	require.NoError(t, e)
	cfg := &conf.Bootstrap{Authz: &conf.Authorization{Type: "casbin"}, Authn: &conf.Authentication{Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(randomKey)}}}
	bctx := bootstrap.NewContextWithParam(sys, nil, cfg, bLogger.NopLogger())
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("GOV_ACC_JOINT_REDIS_ADDR")})
	t.Cleanup(func() { _ = rdb.Close() })
	require.NoError(t, rdb.Ping(ctx).Err())
	cache := data.NewUserTokenCache(bctx, rdb)
	authenticator := data.NewAuthenticator(bctx, cache)
	checker := data.NewTokenChecker(bctx, authenticator, authv1.ClientType_admin)
	tenantChecker := data.NewTenantAccessCheckerImpl(bctx, ec)
	roles := newRoleRepo(ec)
	apis := data.NewApiRepo(bctx, ec)
	policy := authorizer.NewAuthorizer(bctx, data.NewAuthorizerProvider(bctx, roles, apis))
	catalog, e := dbbootstrap.Catalog(assets.OpenApiData)
	require.NoError(t, e)
	tx, e := fixtureDB.BeginTx(ctx, nil)
	require.NoError(t, e)
	_, e = dbbootstrap.SyncAPIs(ctx, tx, catalog, true)
	require.NoError(t, e)
	require.NoError(t, tx.Rollback())
	require.NoError(t, data.NewApiRepo(bctx, fixtureEC).SyncOpenAPI(sys, assets.OpenApiData))
	permissionSQL, e := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "sql", "patches", "20260923_accelerator_permissions.sql"))
	require.NoError(t, e)
	_, e = fixtureDB.ExecContext(ctx, string(permissionSQL))
	require.NoError(t, e)
	tenantID := uint32(1)
	otherTenantID := uint32(9102)
	otherTenant, e := client.Tenant.Get(sys, otherTenantID)
	if ent.IsNotFound(e) {
		otherTenant, e = client.Tenant.Create().SetID(otherTenantID).SetName("BFF cross-tenant fixture").SetCode("gov-acc-bff-other").SetResourceTenantID("22222222-2222-4222-8222-222222222222").SetStatus(tenant.StatusOn).SetPlanID(1).Save(sys)
	}
	require.NoError(t, e)
	require.Equal(t, "22222222-2222-4222-8222-222222222222", otherTenant.ResourceTenantID)
	if n, e := client.PlanModule.Query().Where(planmodule.ModuleEQ(planmodule.ModuleAccelerator), planmodule.HasPlanWith(plan.IDEQ(1))).Count(sys); e != nil {
		t.Fatal(e)
	} else if n == 0 {
		_, e = client.PlanModule.Create().SetPlanID(1).SetModule(planmodule.ModuleAccelerator).Save(sys)
		require.NoError(t, e)
	}
	if n, e := client.PlanModule.Query().Where(planmodule.ModuleEQ(planmodule.ModuleTenant), planmodule.HasPlanWith(plan.IDEQ(1))).Count(sys); e != nil {
		t.Fatal(e)
	} else if n == 0 {
		_, e = client.PlanModule.Create().SetPlanID(1).SetModule(planmodule.ModuleTenant).Save(sys)
		require.NoError(t, e)
	}
	seedRole := func(tid uint32, code string, kind role.Type) *ent.Role {
		r, e := client.Role.Query().Where(role.TenantIDEQ(tid), role.CodeEQ(code)).Only(sys)
		if ent.IsNotFound(e) {
			r, e = client.Role.Create().SetTenantID(tid).SetCode(code).SetName(code).SetType(kind).SetStatus(role.StatusOn).Save(sys)
		}
		require.NoError(t, e)
		return r
	}
	platformRole := seedRole(0, "gov-acc-bff-platform", role.TypeSystem)
	tenantRole := seedRole(tenantID, "gov-acc-bff-user", role.TypeTenant)
	otherRole := seedRole(otherTenantID, "gov-acc-bff-user", role.TypeTenant)
	services := admin.File_admin_service_v1_i_accelerator_proto.Services()
	for i := 0; i < services.Len(); i++ {
		methods := services.Get(i).Methods()
		for j := 0; j < methods.Len(); j++ {
			rule := proto.GetExtension(methods.Get(j).Options(), annotations.E_Http).(*annotations.HttpRule)
			path, method := rule.GetGet(), "GET"
			if path == "" {
				path, method = rule.GetPost(), "POST"
			}
			a, e := client.Api.Query().Where(apiEntity.PathEQ(path), apiEntity.MethodEQ(method)).Only(sys)
			require.NoError(t, e)
			pa, e := client.PermissionApi.Query().Where(permissionapi.APIIDEQ(a.ID)).Only(sys)
			require.NoError(t, e)
			grantRoles := []*ent.Role{tenantRole, otherRole}
			if strings.HasPrefix(path, "/admin/") {
				grantRoles = []*ent.Role{platformRole}
			}
			for _, r := range grantRoles {
				exists, e := client.RolePermission.Query().Where(rolepermission.RoleIDEQ(r.ID), rolepermission.PermissionIDEQ(*pa.PermissionID), rolepermission.TenantIDEQ(*r.TenantID)).Exist(sys)
				require.NoError(t, e)
				if !exists {
					_, e = client.RolePermission.Create().SetTenantID(*r.TenantID).SetRoleID(r.ID).SetPermissionID(*pa.PermissionID).SetEffect(rolepermission.EffectAllow).SetStatus(rolepermission.StatusOn).Save(sys)
					require.NoError(t, e)
				} else {
					_, e = client.RolePermission.Update().Where(rolepermission.RoleIDEQ(r.ID), rolepermission.PermissionIDEQ(*pa.PermissionID), rolepermission.TenantIDEQ(*r.TenantID)).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
					require.NoError(t, e)
				}
			}
		}
	}
	// Existing administrator quota route remains a separately authorized API.
	quotaAPI, e := client.Api.Query().Where(apiEntity.PathEQ("/api/v1/admin/tenants/{id}/quota-accounts"), apiEntity.MethodEQ("GET")).Only(sys)
	require.NoError(t, e)
	quotaPermission, e := client.Permission.Query().Where(permission.CodeEQ("bff:existing-quota:read")).Only(sys)
	if ent.IsNotFound(e) {
		quotaPermission, e = client.Permission.Create().SetName("BFF existing quota fixture").SetCode("bff:existing-quota:read").SetStatus(permission.StatusOn).Save(sys)
	}
	require.NoError(t, e)
	if exists, err := client.PermissionApi.Query().Where(permissionapi.APIIDEQ(quotaAPI.ID), permissionapi.PermissionIDEQ(quotaPermission.ID)).Exist(sys); !exists {
		require.NoError(t, err)
		_, err = client.PermissionApi.Create().SetAPIID(quotaAPI.ID).SetPermissionID(quotaPermission.ID).Save(sys)
		require.NoError(t, err)
	}
	for _, r := range []*ent.Role{platformRole, tenantRole} {
		if exists, err := client.RolePermission.Query().Where(rolepermission.RoleIDEQ(r.ID), rolepermission.PermissionIDEQ(quotaPermission.ID)).Exist(sys); !exists {
			require.NoError(t, err)
			_, err = client.RolePermission.Create().SetTenantID(*r.TenantID).SetRoleID(r.ID).SetPermissionID(quotaPermission.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
			require.NoError(t, err)
		}
	}
	t.Cleanup(func() {
		_, _ = client.RolePermission.Delete().Where(rolepermission.RoleIDEQ(tenantRole.ID), rolepermission.PermissionIDEQ(quotaPermission.ID)).Exec(sys)
	})
	require.NoError(t, policy.ResetPolicies(ctx))
	createToken := func(tid uint32, roleCode string) string {
		p := &authv1.UserTokenPayload{UserId: 1, TenantId: trans.Ptr(tid), Roles: []string{roleCode}}
		token, _, e := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, p)
		require.NoError(t, e)
		return token
	}
	platformToken, tenantToken := createToken(0, *platformRole.Code), createToken(tenantID, *tenantRole.Code)
	otherToken := createToken(otherTenantID, *otherRole.Code)
	downstreamCfg, e := data.AcceleratorConfigFromEnv()
	require.NoError(t, e)
	downstream, closeDownstream, e := data.NewAcceleratorClient(downstreamCfg)
	require.NoError(t, e)
	defer closeDownstream()
	ledger := data.NewQuotaLedgerRepo(bctx, ec)
	quotaAccounts := data.NewQuotaAdminRepo(bctx, ec)
	bridge := NewGpuBffLedgerBridge(ledger, quotaAccounts)
	service := NewAcceleratorService(downstream, data.NewTenantRepo(bctx, ec), bridge, bridge, quotaAccounts, NewQuotaAdapterRegistry())
	server := khttp.NewServer(khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(tenantChecker), auth.WithInjectMetadata(false)), authzMiddleware.Server(policy.Engine())))
	admin.RegisterAcceleratorAdminServiceHTTPServer(server, service)
	admin.RegisterAcceleratorServiceHTTPServer(server, service)
	admin.RegisterQuotaSelfServiceHTTPServer(server, service)
	admin.RegisterQuotaAdminServiceHTTPServer(server, NewQuotaAdminService(bctx, quotaAccounts))
	host := httptest.NewServer(server)
	defer host.Close()
	request := func(method, path, token string, body any, want int) map[string]any {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, e = json.Marshal(body)
			require.NoError(t, e)
		}
		req, e := http.NewRequest(method, host.URL+path, bytes.NewReader(raw))
		require.NoError(t, e)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-ani-tenant-id", uuid.NewString())
		req.Header.Set("x-ani-actor-id", "forged")
		req.Header.Set("x-ani-action", "ObserveRelease")
		response, e := host.Client().Do(req)
		require.NoError(t, e)
		defer response.Body.Close()
		result, e := io.ReadAll(response.Body)
		require.NoError(t, e)
		require.Equalf(t, want, response.StatusCode, "%s %s: %s", method, path, result)
		loggedPath, e := url.Parse(path)
		require.NoError(t, e)
		loggedQuery := loggedPath.Query()
		if loggedQuery.Has("page_token") {
			loggedQuery.Set("page_token", "opaque-redacted")
			loggedPath.RawQuery = loggedQuery.Encode()
		}
		t.Logf("HTTP %s %s -> %d", method, loggedPath.String(), response.StatusCode)
		var out map[string]any
		require.NoError(t, json.Unmarshal(result, &out))
		return out
	}
	var seed struct {
		Cluster  acc.Cluster                `json:"cluster"`
		Pool     acc.Pool                   `json:"pool"`
		Group    acc.SupplyGroup            `json:"group"`
		Profile  acc.GpuProfile             `json:"profile"`
		Request  acc.GpuRequest             `json:"request"`
		Requests map[string]json.RawMessage `json:"requests"`
	}
	seedBytes, e := os.ReadFile(os.Getenv("GOV_ACC_JOINT_SEED_FILE"))
	require.NoError(t, e)
	require.NoError(t, json.Unmarshal(seedBytes, &seed))
	listItems := func(response map[string]any) []any {
		t.Helper()
		items, ok := response["items"].([]any)
		require.True(t, ok, "list must expose typed items")
		require.NotEmpty(t, items)
		require.NotContains(t, response, "total")
		return items
	}
	// Replays the exact management mutation committed by the seed's actor=1.
	adminWrites := []struct{ rpc, path string }{{"RegisterCluster", "/api/v1/admin/accelerator/clusters"}, {"CreatePool", "/api/v1/admin/accelerator/pools"}, {"AdoptSupplyGroup", "/api/v1/admin/accelerator/supply-groups:adopt"}, {"PublishProfile", "/api/v1/admin/accelerator/profiles"}, {"SetAdmission", "/api/v1/admin/accelerator/supply-groups/" + seed.Group.GroupId + ":set-admission"}}
	for _, call := range adminWrites {
		var payload map[string]any
		require.NoError(t, json.Unmarshal(seed.Requests[call.rpc], &payload))
		mutation := payload["mutation"].(map[string]any)
		payload["idempotency_key"] = mutation["idempotency_key"]
		delete(payload, "mutation")
		if baseline, ok := payload["baseline"].(map[string]any); ok {
			delete(baseline, "verification")
		}
		if call.rpc == "SetAdmission" {
			delete(payload, "group_id")
		}
		// encoding/json encodes proto enums numerically; HTTP enum DTOs are names.
		if mode, ok := payload["mode"].(float64); ok {
			payload["mode"] = acc.SupplyMode(int32(mode)).String()
		}
		if spec, ok := payload["spec"].(map[string]any); ok {
			if mode, ok := spec["mode"].(float64); ok {
				spec["mode"] = acc.SupplyMode(int32(mode)).String()
			}
		}
		if desired, ok := payload["desired"].(float64); ok {
			payload["desired"] = acc.AdmissionState(int32(desired)).String()
		}
		response := request("POST", call.path, platformToken, map[string]any{"data": payload}, 200)
		switch call.rpc {
		case "RegisterCluster":
			require.Equal(t, seed.Cluster.ClusterId, response["cluster_id"])
			payload["display_name"] = "conflicting retry"
			conflict := request("POST", call.path, platformToken, map[string]any{"data": payload}, 409)
			require.Equal(t, "IDEMPOTENCY_CONFLICT", conflict["reason"])
		case "CreatePool":
			require.Equal(t, seed.Pool.PoolId, response["pool_id"])
			require.Equal(t, seed.Cluster.ClusterId, response["cluster_id"])
		case "AdoptSupplyGroup", "SetAdmission":
			require.Equal(t, seed.Group.GroupId, response["group_id"])
			if call.rpc == "SetAdmission" {
				require.Equal(t, "ADMISSION_OPEN", response["admission"])
				payload["idempotency_key"] = uuid.NewString()
				payload["expected_version"] = "1"
				conflict := request("POST", call.path, platformToken, map[string]any{"data": payload}, 409)
				require.Equal(t, "VERSION_CONFLICT", conflict["reason"])
			}
		case "PublishProfile":
			require.Equal(t, seed.Profile.ProfileId, response["profile_id"])
			require.Equal(t, "1", response["profile_version"])
			require.Equal(t, true, response["published"])
		}
	}
	query := func(path string, kv ...string) string {
		q := url.Values{}
		for i := 0; i < len(kv); i += 2 {
			q.Set(kv[i], kv[i+1])
		}
		return path + "?" + q.Encode()
	}
	for _, path := range []string{
		"/api/v1/admin/accelerator/clusters?page_size=1",
		query("/api/v1/admin/accelerator/pools", "cluster_id", seed.Cluster.ClusterId),
		query("/api/v1/admin/accelerator/supply-groups", "pool_id", seed.Pool.PoolId),
		query("/api/v1/admin/accelerator/profiles", "cluster_id", seed.Cluster.ClusterId),
		query("/api/v1/admin/accelerator/devices", "cluster_id", seed.Cluster.ClusterId),
		query("/api/v1/admin/accelerator/bindings", "cluster_id", seed.Cluster.ClusterId),
		query("/api/v1/admin/accelerator/capacity", "profile_id", seed.Profile.ProfileId, "profile_version", "1", "include_devices", "true"),
	} {
		response := request("GET", path, platformToken, nil, 200)
		if strings.Contains(path, "/capacity?") {
			require.Equal(t, seed.Profile.ProfileId, response["profile_id"])
			require.NotEmpty(t, response["devices"])
			continue
		}
		items := listItems(response)
		first := items[0].(map[string]any)
		switch {
		case strings.Contains(path, "/clusters?"):
			require.NotEmpty(t, first["cluster_id"])
			require.NotEmpty(t, first["connection_ref"])
		case strings.Contains(path, "/pools?"):
			require.Equal(t, seed.Cluster.ClusterId, first["cluster_id"])
		case strings.Contains(path, "/supply-groups?"):
			require.Equal(t, seed.Group.GroupId, first["group_id"])
			require.NotEmpty(t, first["baseline"])
		case strings.Contains(path, "/profiles?"):
			require.Equal(t, seed.Profile.ProfileId, first["profile_id"])
		case strings.Contains(path, "/devices?"):
			require.NotEmpty(t, first["device_id"])
			require.NotEmpty(t, first["node"])
		case strings.Contains(path, "/bindings?"):
			require.NotEmpty(t, first["physical_device_id"])
			require.NotEmpty(t, first["pod_uid"])
			require.NotEmpty(t, first["evidence_ref"])
		}
	}
	tenantProfiles := query("/api/v1/accelerator/profiles", "cluster_id", seed.Cluster.ClusterId)
	require.Equal(t, seed.Profile.ProfileId, listItems(request("GET", tenantProfiles, tenantToken, nil, 200))[0].(map[string]any)["profile_id"])
	request("GET", tenantProfiles, otherToken, nil, 403) // downstream exact tenant/cluster grant
	profileView := request("GET", "/api/v1/accelerator/profiles/"+seed.Profile.ProfileId+"?profile_version=1", tenantToken, nil, 200)
	require.Equal(t, seed.Profile.ProfileId, profileView["profile_id"])
	require.Equal(t, "1", profileView["profile_version"])
	capacity := request("GET", query("/api/v1/accelerator/capacity", "profile_id", seed.Profile.ProfileId, "profile_version", "1"), tenantToken, nil, 200)
	if v, ok := capacity["devices"].([]any); ok {
		require.Empty(t, v)
	}
	ledgerSnapshot := func() []string {
		t.Helper()
		out := make([]string, 3)
		require.NoError(t, db.QueryRowContext(ctx, acceleratorLedgerSnapshotSQL, tenantID).Scan(&out[0], &out[1], &out[2]))
		return out
	}
	beforePreview := ledgerSnapshot()
	preview := request("POST", "/api/v1/accelerator/admission-preview", tenantToken, map[string]any{"data": &seed.Request}, 200)
	require.Equal(t, beforePreview, ledgerSnapshot(), "preview cannot change operations, charges or account rows")
	quotaItems := preview["requested_quota_items"].([]any)
	require.Len(t, quotaItems, 1)
	require.Equal(t, "12288", quotaItems[0].(map[string]any)["units"])
	require.NotEmpty(t, preview["quota_checks"])
	if expectedFit := os.Getenv("GOV_ACC_BFF_EXPECT_FIT"); expectedFit != "" {
		require.Equal(t, expectedFit, preview["gpu_fit"])
		for _, check := range preview["quota_checks"].([]any) {
			require.Equal(t, true, check.(map[string]any)["sufficient"], "capacity is independent from sufficient quota")
		}
		t.Logf("preview expected gpu_fit=%s with sufficient quota; ledger unchanged", expectedFit)
	}
	invalidRequest := proto.Clone(&seed.Request).(*acc.GpuRequest)
	invalidRequest.Replicas = 17
	request("POST", "/api/v1/accelerator/admission-preview", tenantToken, map[string]any{"data": invalidRequest}, 400)
	require.Equal(t, beforePreview, ledgerSnapshot(), "invalid preview cannot change ledger state")
	require.Equal(t, "NOT_ENABLED", preview["owner_execution_readiness"])
	require.Equal(t, "NOT_CHECKED", preview["cpu"])
	require.Equal(t, "NOT_CHECKED", preview["network"])
	require.Equal(t, "NOT_CHECKED", preview["storage"])
	require.NotContains(t, preview, "plan")
	require.NotContains(t, preview, "runtime")
	listItems(request("GET", "/api/v1/accelerator/usages", tenantToken, nil, 200))
	request("GET", "/api/v1/accelerator/usages?owner_service=unknown-owner", tenantToken, nil, 400)
	request("GET", "/api/v1/accelerator/usages/unknown-owner/"+uuid.NewString(), tenantToken, nil, 404)
	firstPage := request("GET", "/api/v1/accelerator/usages?page_size=1", tenantToken, nil, 200)
	firstUsage := listItems(firstPage)[0].(map[string]any)
	cursor, ok := firstPage["next_page_token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, cursor, "multi-page real usages required")
	require.Equal(t, firstPage, request("GET", "/api/v1/accelerator/usages?page_size=1", tenantToken, nil, 200))
	nextPath := query("/api/v1/accelerator/usages", "page_size", "1", "page_token", cursor)
	nextUsage := listItems(request("GET", nextPath, tenantToken, nil, 200))[0].(map[string]any)
	require.NotEqual(t, firstUsage["create_operation_id"], nextUsage["create_operation_id"])
	request("GET", nextPath, otherToken, nil, 403)
	otherActorToken, _, e := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, &authv1.UserTokenPayload{UserId: 2, TenantId: trans.Ptr(tenantID), Roles: []string{*tenantRole.Code}})
	require.NoError(t, e)
	request("GET", nextPath, otherActorToken, nil, 403)
	request("GET", nextPath+"&state=ENDED", tenantToken, nil, 400)
	request("GET", query("/api/v1/accelerator/usages", "page_token", "tampered-token"), tenantToken, nil, 400)
	request("GET", query("/api/v1/accelerator/profiles", "page_token", cursor), tenantToken, nil, 400)
	request("GET", "/api/v1/accelerator/usages?page_size=200", tenantToken, nil, 200)
	accounts := request("GET", "/api/v1/me/quota-accounts?tenant_id=9102", tenantToken, nil, 200)
	require.Equal(t, float64(1), accounts["tenantId"])
	require.NotEmpty(t, accounts["items"])
	adminAccounts := request("GET", "/api/v1/admin/tenants/1/quota-accounts", platformToken, nil, 200)
	require.Equal(t, accounts, adminAccounts)
	request("GET", "/api/v1/admin/tenants/1/quota-accounts", tenantToken, nil, 404)
	request("GET", "/api/v1/admin/tenants/1/quota-accounts", "", nil, 401)
	request("GET", tenantProfiles, "", nil, 401)
	request("GET", tenantProfiles, "invalid.jwt", nil, 401)
	request("GET", tenantProfiles, createToken(tenantID, "ungranted-bff-role"), nil, 403)
	request("GET", "/api/v1/admin/accelerator/clusters", tenantToken, nil, 403)
	request("GET", tenantProfiles, platformToken, nil, 403)
	request("GET", tenantProfiles+"&page_size=201", tenantToken, nil, 400)
	request("GET", "/api/v1/accelerator/profiles/"+uuid.NewString()+"?profile_version=1", tenantToken, nil, 404)
	// Removing the live role grant rejects an otherwise still-valid token.
	a, e := client.Api.Query().Where(apiEntity.PathEQ("/api/v1/accelerator/profiles"), apiEntity.MethodEQ("GET")).Only(sys)
	require.NoError(t, e)
	pa, e := client.PermissionApi.Query().Where(permissionapi.APIIDEQ(a.ID)).Only(sys)
	require.NoError(t, e)
	grant, e := client.RolePermission.Query().Where(rolepermission.RoleIDEQ(tenantRole.ID), rolepermission.PermissionIDEQ(*pa.PermissionID), rolepermission.TenantIDEQ(tenantID)).Only(sys)
	require.NoError(t, e)
	_, e = client.RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOff).Save(sys)
	require.NoError(t, e)
	t.Cleanup(func() { _ = client.RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOn).Exec(sys) })
	require.NoError(t, policy.ResetPolicies(ctx))
	request("GET", tenantProfiles, tenantToken, nil, 403)
	_, e = client.RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOn).Save(sys)
	require.NoError(t, e)
	require.NoError(t, policy.ResetPolicies(ctx))
	// Module revocation is read live from PG, independent of policy refresh.
	for _, check := range []struct {
		name    string
		disable func() error
		restore func() error
	}{
		{"deny grant", func() error {
			return client.RolePermission.UpdateOneID(grant.ID).SetEffect(rolepermission.EffectDeny).Exec(sys)
		}, func() error {
			return client.RolePermission.UpdateOneID(grant.ID).SetEffect(rolepermission.EffectAllow).Exec(sys)
		}},
		{"disabled role", func() error { return client.Role.UpdateOneID(tenantRole.ID).SetStatus(role.StatusOff).Exec(sys) }, func() error { return client.Role.UpdateOneID(tenantRole.ID).SetStatus(role.StatusOn).Exec(sys) }},
		{"disabled permission", func() error {
			return client.Permission.UpdateOneID(*pa.PermissionID).SetStatus(permission.StatusOff).Exec(sys)
		}, func() error {
			return client.Permission.UpdateOneID(*pa.PermissionID).SetStatus(permission.StatusOn).Exec(sys)
		}},
		{"disabled API", func() error { return client.Api.UpdateOneID(a.ID).SetStatus(apiEntity.StatusOff).Exec(sys) }, func() error { return client.Api.UpdateOneID(a.ID).SetStatus(apiEntity.StatusOn).Exec(sys) }},
	} {
		require.NoError(t, check.disable(), check.name)
		t.Cleanup(func() { _ = check.restore() })
		require.NoError(t, policy.ResetPolicies(ctx), check.name)
		request("GET", tenantProfiles, tenantToken, nil, 403)
		require.NoError(t, check.restore(), check.name)
		require.NoError(t, policy.ResetPolicies(ctx), check.name)
	}
	mod, e := client.PlanModule.Query().Where(planmodule.HasPlanWith(plan.IDEQ(1)), planmodule.ModuleEQ(planmodule.ModuleAccelerator)).Only(sys)
	require.NoError(t, e)
	require.NoError(t, client.PlanModule.DeleteOneID(mod.ID).Exec(sys))
	request("GET", tenantProfiles, tenantToken, nil, 403)
	request("GET", "/api/v1/me/quota-accounts", tenantToken, nil, 200)
	_, e = client.PlanModule.Create().SetPlanID(1).SetModule(planmodule.ModuleAccelerator).Save(sys)
	require.NoError(t, e)
	// Mandatory nonempty binding created by the independent owner/ledger chain.
	refPath := os.Getenv("GOV_ACC_JOINT_USAGE_REF_FILE")
	require.NotEmpty(t, refPath, "nonempty ledger/binding fixture is mandatory")
	refBytes, e := os.ReadFile(refPath)
	require.NoError(t, e)
	var fixture struct {
		Ref acc.GpuUsageRef `json:"ref"`
	}
	require.NoError(t, json.Unmarshal(refBytes, &fixture))
	ref := &fixture.Ref
	require.NotEmpty(t, ref.CreateOperationId)
	usagePath := fmt.Sprintf("/api/v1/accelerator/usages/%s/%s", ref.OwnerService, ref.ResourceId)
	usage := request("GET", usagePath, tenantToken, nil, 200)
	require.Equal(t, "ENDED", usage["state"])
	request("GET", usagePath, otherToken, nil, 404)
	request("GET", usagePath+"/bindings", otherToken, nil, 404)
	require.NotContains(t, usage, "source_fact_ref")
	require.NotContains(t, usage, "plan")
	bindings := request("GET", usagePath+"/bindings", tenantToken, nil, 200)
	items, ok := bindings["items"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, items, "empty list cannot prove redaction")
	body, e := json.Marshal(bindings)
	require.NoError(t, e)
	for _, sensitive := range []string{"physical_device_id", "pod_uid", "pod_namespace", "evidence_ref", "source_baseline_digest", "snapshot_ref", "tenant_id"} {
		require.NotContains(t, string(body), sensitive)
	}
	request("GET", "/api/v1/accelerator/usages/ani-inference/"+uuid.NewString(), tenantToken, nil, 404)
	if output := os.Getenv("GOV_ACC_BFF_SESSION_OUTPUT"); output != "" {
		// Explicit private test session handoff for the separate ordinary-binary
		// process gate. Never print or commit signing material or bearer tokens.
		material, err := json.Marshal(map[string]string{"jwt_key": hex.EncodeToString(randomKey), "platform_token": platformToken, "tenant_token": tenantToken})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(output, material, 0600))
		require.NoError(t, os.Chmod(output, 0600))
	}
	t.Log("PASS: twenty HTTP BFFs, production JWT/cache + tenant/module/Casbin gates, exact downstream mTLS delegation, nonempty binding redaction; isolated software fixture, hardware_observed=false")
}

// Refresh only the explicitly issued test sessions used by the ordinary-binary
// gate after a pause. It does not bypass runtime JWT checking or issue a login
// endpoint; the issuer key remains in the private task directory.
func TestAcceleratorFormalSessionRefresh(t *testing.T) {
	path := os.Getenv("GOV_ACC_FORMAL_SESSION_FILE")
	if path == "" {
		t.Skip("requires the explicit private formal process fixture")
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var material map[string]string
	require.NoError(t, json.Unmarshal(raw, &material))
	require.NotEmpty(t, material["jwt_key"])
	cfg := &conf.Bootstrap{Authn: &conf.Authentication{Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: material["jwt_key"]}}}
	bctx := bootstrap.NewContextWithParam(context.Background(), nil, cfg, bLogger.NopLogger())
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("GOV_ACC_JOINT_REDIS_ADDR")})
	defer rdb.Close()
	require.NoError(t, rdb.Ping(context.Background()).Err())
	authenticator := data.NewAuthenticator(bctx, data.NewUserTokenCache(bctx, rdb))
	for name, payload := range map[string]*authv1.UserTokenPayload{
		"platform_token": {UserId: 1, TenantId: trans.Ptr(uint32(0)), Roles: []string{"gov-acc-bff-platform"}},
		"tenant_token":   {UserId: 1, TenantId: trans.Ptr(uint32(1)), Roles: []string{"gov-acc-bff-user"}},
	} {
		token, _, err := authenticator.CreateUserToken(context.Background(), authv1.ClientType_admin, payload)
		require.NoError(t, err)
		material[name] = token
	}
	raw, err = json.Marshal(material)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0600))
	require.NoError(t, os.Chmod(path, 0600))
}
