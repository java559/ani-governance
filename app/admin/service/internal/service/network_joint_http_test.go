//go:build networkintegration

package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/cmd/server/assets"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/accesskey"
	apiEntity "go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/permissionapi"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/pkg/authorizer"
	appcrypto "go-wind-admin/pkg/crypto"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	authzMiddleware "go-wind-admin/pkg/localdeps/kratos-authz/middleware"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/middleware/auth"
	dbbootstrap "go-wind-admin/sql/bootstrap"
)

// Run only through scripts/network-joint-integration: required inputs fail rather
// than skip. Auth/cache/policies/tenant resolution and HTTP/mTLS are real;
// Resource uses production code with controlled external providers, or the
// runner-owned actual Resource process for the explicitly selected live VPC case.
func TestNetworkJointHTTP(t *testing.T) {
	peer := newNetworkResourcePeer(t)
	ctx := context.Background()
	sys := appViewer.NewSystemViewerContext(ctx)
	openDB := func(env string) (*sql.DB, *entCrud.EntClient[*ent.Client], *ent.Client) {
		t.Helper()
		file := os.Getenv(env)
		require.NotEmpty(t, file, "isolated runner input required")
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		db, err := sql.Open("pgx", strings.TrimSpace(string(raw)))
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		driver := entsql.OpenDB(dialect.Postgres, db)
		client := ent.NewClient(ent.Driver(driver))
		return db, entCrud.NewEntClient[*ent.Client](client, driver), client
	}
	runtimeDB, ec, _ := openDB("NETWORK_GOV_RUNTIME_DSN_FILE")
	fixtureDB, fixtureEC, client := openDB("NETWORK_GOV_ADMIN_DSN_FILE")
	var privileged bool
	require.NoError(t, runtimeDB.QueryRowContext(ctx, "SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolbypassrls FROM pg_roles WHERE rolname=current_user").Scan(&privileged))
	require.False(t, privileged)
	var owners int
	require.NoError(t, runtimeDB.QueryRowContext(ctx, "SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace AND relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user)").Scan(&owners))
	require.Zero(t, owners)
	randomKey := make([]byte, 32)
	_, err := rand.Read(randomKey)
	require.NoError(t, err)
	cfg := &conf.Bootstrap{Authz: &conf.Authorization{Type: "casbin"}, Authn: &conf.Authentication{Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(randomKey)}}}
	bctx := bootstrap.NewContextWithParam(sys, nil, cfg, bLogger.NopLogger())
	raw, err := os.ReadFile(os.Getenv("NETWORK_GOV_REDIS_FILE"))
	require.NoError(t, err)
	var redisCfg struct{ Address, Password string }
	require.NoError(t, json.Unmarshal(raw, &redisCfg))
	require.NotEmpty(t, redisCfg.Password)
	rdb := redis.NewClient(&redis.Options{Addr: redisCfg.Address, Password: redisCfg.Password})
	t.Cleanup(func() { _ = rdb.Close() })
	require.NoError(t, rdb.Ping(ctx).Err())
	cache := data.NewUserTokenCache(bctx, rdb)
	authenticator := data.NewAuthenticator(bctx, cache)
	checker := data.NewTokenChecker(bctx, authenticator, authv1.ClientType_admin)
	apis := data.NewApiRepo(bctx, ec)
	policy := authorizer.NewAuthorizer(bctx, data.NewAuthorizerProvider(bctx, newRoleRepo(ec), apis))
	catalog, err := dbbootstrap.Catalog(assets.OpenApiData)
	require.NoError(t, err)
	tx, err := fixtureDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = dbbootstrap.SyncAPIs(ctx, tx, catalog, true)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	require.NoError(t, data.NewApiRepo(bctx, fixtureEC).SyncOpenAPI(sys, assets.OpenApiData))
	seed, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "sql", "data", "20261009_network_permissions.sql"))
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = fixtureDB.ExecContext(ctx, string(seed))
		require.NoError(t, err)
	}
	count, err := client.RolePermission.Query().Count(sys)
	require.NoError(t, err)
	require.Zero(t, count, "catalog import must not grant roles")
	plan, err := client.Plan.Create().SetName("Network isolated fixture").Save(sys)
	require.NoError(t, err)
	module, err := client.PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleNetwork).Save(sys)
	require.NoError(t, err)
	uuids := []string{peer.TenantID, "22222222-2222-4222-8222-222222222222"}
	tenants := make([]*ent.Tenant, 2)
	for i, id := range uuids {
		tenants[i], err = client.Tenant.Create().SetName(fmt.Sprintf("Network %d", i)).SetCode(fmt.Sprintf("network-%d", i)).SetResourceTenantID(id).SetStatus(tenant.StatusOn).SetPlanID(plan.ID).Save(sys)
		require.NoError(t, err)
	}
	createRole := func(tid uint32, code string) *ent.Role {
		r, err := client.Role.Create().SetTenantID(tid).SetCode(code).SetName(code).SetType(role.TypeTenant).SetStatus(role.StatusOn).Save(sys)
		require.NoError(t, err)
		return r
	}
	reader := createRole(tenants[0].ID, "network-fixture-reader")
	publisher := createRole(tenants[0].ID, "network-fixture-publisher")
	other := createRole(tenants[1].ID, "network-fixture-other")
	for _, op := range auth.NetworkOperations {
		a, err := client.Api.Query().Where(apiEntity.PathEQ(op.Path), apiEntity.MethodEQ(op.Method)).Only(sys)
		require.NoError(t, err)
		require.Equal(t, apiEntity.BusinessModuleNetwork, *a.BusinessModule)
		perm, err := client.Permission.Query().Where(permission.CodeEQ(op.Permission)).Only(sys)
		require.NoError(t, err)
		_, err = client.PermissionApi.Query().Where(permissionapi.APIIDEQ(a.ID), permissionapi.PermissionIDEQ(perm.ID)).Only(sys)
		require.NoError(t, err)
		grant := []*ent.Role{publisher, other}
		// Leave the new preset permission ungranted to this fixture role so its
		// JWT and API key exercise the actual permission denial path.
		if op.ReadOnly && op.Permission != "network:vpc:presets" {
			grant = append(grant, reader)
		}
		for _, r := range grant {
			exists, err := client.RolePermission.Query().Where(rolepermission.RoleIDEQ(r.ID), rolepermission.PermissionIDEQ(perm.ID)).Exist(sys)
			require.NoError(t, err)
			if exists {
				continue
			}
			_, err = client.RolePermission.Create().SetTenantID(*r.TenantID).SetRoleID(r.ID).SetPermissionID(perm.ID).SetEffect(rolepermission.EffectAllow).SetStatus(rolepermission.StatusOn).Save(sys)
			require.NoError(t, err)
		}
	}
	platformSeed, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "sql", "data", "20261012_platform_network_permissions.sql"))
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = fixtureDB.ExecContext(ctx, string(platformSeed))
		require.NoError(t, err)
	}
	platformRole, err := client.Role.Create().SetTenantID(0).SetCode("platform:network-fixture").SetName("platform-network-fixture").SetType(role.TypeSystem).SetStatus(role.StatusOn).Save(sys)
	require.NoError(t, err)
	deniedPlatformRole, err := client.Role.Create().SetTenantID(0).SetCode("platform:network-denied").SetName("platform-network-denied").SetType(role.TypeSystem).SetStatus(role.StatusOn).Save(sys)
	require.NoError(t, err)
	platformPermissions := []string{}
	for _, op := range auth.PlatformNetworkOperations {
		perm, err := client.Permission.Query().Where(permission.CodeEQ(op.Permission)).Only(sys)
		require.NoError(t, err)
		_, err = client.RolePermission.Create().SetTenantID(0).SetRoleID(platformRole.ID).SetPermissionID(perm.ID).SetEffect(rolepermission.EffectAllow).SetStatus(rolepermission.StatusOn).Save(sys)
		require.NoError(t, err)
		platformPermissions = append(platformPermissions, op.Permission)
	}
	require.NoError(t, policy.ResetPolicies(ctx))
	token := func(r *ent.Role, uid uint32) string {
		v, _, err := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, &authv1.UserTokenPayload{UserId: uid, TenantId: r.TenantID, Roles: []string{*r.Code}})
		require.NoError(t, err)
		return v
	}
	readerToken, publisherToken, otherToken := token(reader, 1), token(publisher, 2), token(other, 3)
	platformToken, _, err := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, &authv1.UserTokenPayload{UserId: 9, TenantId: platformRole.TenantID, Roles: []string{*platformRole.Code}, Permissions: platformPermissions})
	require.NoError(t, err)
	deniedPlatformToken, _, err := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, &authv1.UserTokenPayload{UserId: 10, TenantId: deniedPlatformRole.TenantID, Roles: []string{*deniedPlatformRole.Code}, Permissions: platformPermissions})
	require.NoError(t, err)
	cipher, err := appcrypto.NewAccessKeyCipher(hex.EncodeToString(randomKey))
	require.NoError(t, err)
	keys := data.NewAccessKeyRepo(bctx, ec, cipher)
	makeAK := func(r *ent.Role, suffix string) (*ent.AccessKey, string) {
		secret := "sk-" + hex.EncodeToString(randomKey) + suffix
		encrypted, err := cipher.Encrypt(secret)
		require.NoError(t, err)
		k, err := client.AccessKey.Create().SetTenantID(*r.TenantID).SetRoleID(r.ID).SetAccessKey("ak-network-" + suffix).SetSecretCiphertext(encrypted).SetStatus(accesskey.StatusOn).Save(sys)
		require.NoError(t, err)
		return k, secret
	}
	readerAK, readerSK := makeAK(reader, "reader")
	publisherAK, publisherSK := makeAK(publisher, "publisher")
	otherAK, otherSK := makeAK(other, "other")
	// Match configs/server.yaml: the framework's 1s default cancels real KC
	// reads at the fixture boundary before the Network client's own deadline.
	server := khttp.NewServer(khttp.Timeout(10*time.Second), khttp.Filter(auth.NetworkHTTPFilter), khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithSigningKeyStore(keys), auth.WithTenantAccessChecker(data.NewTenantAccessCheckerImpl(bctx, ec)), auth.WithInjectMetadata(false)), authzMiddleware.Server(policy.Engine())))
	admin.RegisterNetworkServiceHTTPServer(server, NewNetworkService(peer.Client, data.NewTenantRepo(bctx, ec)))
	admin.RegisterPlatformNetworkServiceHTTPServer(server, NewPlatformNetworkService(peer.Client))
	host := httptest.NewServer(server)
	t.Cleanup(host.Close)
	type identity struct {
		bearer string
		key    *ent.AccessKey
		secret string
	}
	// Track only resources admitted through this HTTP fixture. Compensation runs
	// before closing HTTP, tokens or the Resource subprocess and uses product
	// lifecycle APIs, retaining the original failure and reporting cleanup apart.
	type admittedResource struct{ path, bearer string }
	admitted := map[string]map[string]admittedResource{}
	track := func(id identity, method, path string, out map[string]any) {
		resource := out
		if combined, ok := out["load_balancer"].(map[string]any); ok {
			resource = combined
		}
		resourceID, _ := resource["id"].(string)
		if resourceID == "" {
			return
		}
		if resource["state"] == "deleted" {
			for _, entries := range admitted {
				delete(entries, resourceID)
			}
			return
		}
		if method != "POST" {
			return
		}
		kind, resourcePath := "", ""
		switch {
		case path == "/api/v1/networks/vpcs":
			kind = "vpc"
			resourcePath = path + "/" + resourceID
		case path == "/api/v1/networks/subnets":
			kind = "subnet"
			resourcePath = path + "/" + resourceID
		case path == "/api/v1/networks/eips":
			kind = "eip"
			resourcePath = path + "/" + resourceID
		case path == "/api/v1/networks/load-balancers":
			kind = "load-balancer"
			resourcePath = path + "/" + resourceID
		case strings.HasSuffix(path, "/snat/bindings"):
			kind = "snat"
			resourcePath = "/api/v1/networks/snat/bindings/" + resourceID
		}
		if kind == "" {
			return
		}
		if admitted[kind] == nil {
			admitted[kind] = map[string]admittedResource{}
		}
		bearer := publisherToken
		if id.bearer == otherToken || id.key == otherAK {
			bearer = otherToken
		}
		admitted[kind][resourceID] = admittedResource{resourcePath, bearer}
	}
	t.Cleanup(func() {
		remaining := 0
		for _, entries := range admitted {
			remaining += len(entries)
		}
		if remaining == 0 {
			t.Log("HTTP compensation: no active task resources")
			return
		}
		call := func(method string, resource admittedResource) (int, map[string]any, error) {
			requestCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(requestCtx, method, host.URL+resource.path, nil)
			if err != nil {
				return 0, nil, err
			}
			req.Header.Set("Authorization", "Bearer "+resource.bearer)
			response, err := host.Client().Do(req)
			if err != nil {
				return 0, nil, err
			}
			defer response.Body.Close()
			payload, err := io.ReadAll(response.Body)
			if err != nil {
				return response.StatusCode, nil, err
			}
			out := map[string]any{}
			err = json.Unmarshal(payload, &out)
			return response.StatusCode, out, err
		}
		deadline := time.Now().Add(20 * time.Second)
		for _, kind := range []string{"load-balancer", "snat", "subnet", "eip", "vpc"} {
			for resourceID, resource := range admitted[kind] {
				finished := false
				lastStatus := 0
				var lastErr error
				for time.Now().Before(deadline) {
					status, out, err := call("GET", resource)
					lastStatus, lastErr = status, err
					if err == nil && (status == 404 || (status == 200 && out["state"] == "deleted")) {
						finished = true
						break
					}
					status, _, err = call("DELETE", resource)
					lastStatus, lastErr = status, err
					if err != nil || status == 401 || status == 403 {
						break
					}
					time.Sleep(40 * time.Millisecond)
				}
				if finished {
					delete(admitted[kind], resourceID)
					t.Logf("HTTP compensation pass kind=%s id=%s", kind, resourceID)
				} else {
					t.Errorf("HTTP compensation fail kind=%s id=%s status=%d transport=%v", kind, resourceID, lastStatus, lastErr)
				}
			}
		}
	})
	jwtIdentity := identity{bearer: publisherToken}
	akIdentity := identity{key: publisherAK, secret: publisherSK}
	send := func(t *testing.T, id identity, method, path, body string, mutate func(*http.Request)) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(method, host.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.URL.RawQuery = req.URL.Query().Encode()
		req.Header.Set("Content-Type", "application/json")
		if id.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+id.bearer)
		}
		if id.key != nil {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			req.Header.Set("X-Access-Key", id.key.AccessKey)
			req.Header.Set("X-Timestamp", ts)
			req.Header.Set("X-Signature", auth.NetworkSignature(id.secret, method, req.URL.Path, req.URL.RawQuery, id.key.AccessKey, ts, []byte(body)))
		}
		if mutate != nil {
			mutate(req)
		}
		resp, err := host.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		payload, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var admittedOutput map[string]any
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && json.Unmarshal(payload, &admittedOutput) == nil {
			track(id, method, req.URL.Path, admittedOutput)
		}
		require.NotContains(t, string(payload), "tenant_id")
		require.NotContains(t, string(payload), "tenantId")
		out := map[string]any{}
		require.NoError(t, json.Unmarshal(payload, &out))
		t.Logf("HTTP %s %s -> %d", method, path, resp.StatusCode)
		return resp.StatusCode, out
	}
	request := func(t *testing.T, id identity, method, path, body string, mutate func(*http.Request), want int) map[string]any {
		t.Helper()
		status, out := send(t, id, method, path, body, mutate)
		require.Equal(t, want, status, "%s %s response=%v", method, path, out)
		return out
	}
	get := func(t *testing.T, id identity, path string) map[string]any {
		return request(t, id, "GET", path, "", nil, 200)
	}
	poll := func(t *testing.T, id identity, path string, ready func(map[string]any) bool) map[string]any {
		t.Helper()
		var last map[string]any
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
			out := get(t, id, path)
			last = out
			if ready(out) {
				return out
			}
			time.Sleep(40 * time.Millisecond)
		}
		t.Fatalf("asynchronous Resource worker must converge: %s state=%v reason=%v desired=%v applied=%v", path, last["state"], last["reason"], last["desired_version"], last["applied_version"])
		return nil
	}
	state := func(want string) func(map[string]any) bool {
		return func(v map[string]any) bool { return v["state"] == want }
	}
	assertPersisted := func(t *testing.T, table, column, id, tenant, state string) {
		t.Helper()
		var tid, current string
		require.NoError(t, peer.DB.QueryRowContext(ctx, "SELECT tenant_id::text,state FROM "+table+" WHERE "+column+"=$1", id).Scan(&tid, &current))
		require.Equal(t, tenant, tid)
		if state == "provisioning" {
			require.Contains(t, []string{"provisioning", "available"}, current)
		} else {
			require.Equal(t, state, current)
		}
	}
	beforeSideEffects := func(t *testing.T) int {
		var count int
		require.NoError(t, peer.DB.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM network_operations)+(SELECT count(*) FROM network_platform_operations)").Scan(&count))
		return count
	}
	const base = "/api/v1/networks"
	t.Run("deferred_load_balancer", func(t *testing.T) {
		jsonBody := func(v any) string {
			raw, err := json.Marshal(v)
			require.NoError(t, err)
			return string(raw)
		}
		mutations := func() []string {
			response, err := http.Get(peer.ProviderURL + "/network-joint/lb-requests")
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, 200, response.StatusCode)
			var requests []string
			require.NoError(t, json.NewDecoder(response.Body).Decode(&requests))
			out := []string{}
			for _, r := range requests {
				if !strings.HasPrefix(r, "GET/") && !strings.Contains(r, "/secrets/") {
					out = append(out, r)
				}
			}
			return out
		}
		listenerInput := func(name string, port int) map[string]any {
			return map[string]any{"name": name, "protocol": "LOAD_BALANCER_LISTENER_PROTOCOL_HTTP", "port": port}
		}
		createBody := func(key string) string {
			return jsonBody(map[string]any{"name": key, "vpc_id": peer.ParentVPC, "subnet_id": peer.EntrySubnet, "exposure": "LOAD_BALANCER_EXPOSURE_PRIVATE", "private_ip": "10.42.1.100", "idempotency_key": key, "listeners": map[string]any{"items": []any{listenerInput("first", 80)}}})
		}
		waiting := func(v map[string]any) bool {
			return v["configuration_state"] == "LOAD_BALANCER_CONFIGURATION_STATE_WAITING_FOR_BACKENDS" && v["provider_target_version"] == v["applied_version"]
		}
		configured := func(v map[string]any) bool {
			return v["state"] == "available" && v["configuration_state"] == "LOAD_BALANCER_CONFIGURATION_STATE_CONFIGURED" && v["provider_target_version"] == v["applied_version"]
		}
		binding := func(lbID string) (string, string, string) {
			var id, name, uid string
			require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT binding_id::text,provider_name,provider_uid FROM network_provider_bindings WHERE tenant_id=$1 AND lb_id=$2 AND resource_kind='load_balancer'`, peer.TenantID, lbID).Scan(&id, &name, &uid))
			return id, name, uid
		}
		gateway := func(name string, status int) map[string]any {
			response, err := http.Get(peer.ProviderURL + "/apis/gateway.networking.k8s.io/v1/namespaces/" + peer.Namespace + "/gateways/" + name)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, status, response.StatusCode)
			var obj map[string]any
			require.NoError(t, json.NewDecoder(response.Body).Decode(&obj))
			return obj
		}
		t.Run("never_activated_delete", func(t *testing.T) {
			before := mutations()
			body := createBody("deferred-never-activated")
			accepted := request(t, jwtIdentity, "POST", base+"/load-balancers", body, nil, 200)
			lbID := accepted["load_balancer"].(map[string]any)["id"].(string)
			path := base + "/load-balancers/" + lbID
			lb := get(t, akIdentity, path)
			require.True(t, waiting(lb))
			require.Equal(t, "0", lb["provider_target_version"])
			require.Equal(t, "0", lb["applied_version"])
			require.Nil(t, lb["observed_at"], "database-only acceptance must not manufacture an observation")
			poll(t, jwtIdentity, base+"/load-balancers/operations/"+accepted["operation"].(map[string]any)["id"].(string), state("succeeded"))
			request(t, akIdentity, "DELETE", path, "", nil, 200)
			poll(t, jwtIdentity, path, state("deleted"))
			require.Equal(t, before, mutations(), "never-activated save/delete must not mutate Provider")
			require.Equal(t, accepted, request(t, akIdentity, "POST", base+"/load-balancers", body, nil, 200), "accepted receipt remains unchanged after deletion")
			var retained, occupied int
			require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM network_lb_configurations WHERE tenant_id=$1 AND lb_id=$2),(SELECT count(*) FROM network_lb_vip_intents WHERE tenant_id=$1 AND lb_id=$2 AND released_at IS NULL)+(SELECT count(*) FROM network_lb_subnet_refs WHERE tenant_id=$1 AND lb_id=$2 AND released_at IS NULL)`, peer.TenantID, lbID).Scan(&retained, &occupied))
			require.Equal(t, 1, retained)
			require.Zero(t, occupied)
		})
		t.Run("lifecycle", func(t *testing.T) {
			before := mutations()
			body := createBody("deferred-lifecycle")
			accepted := request(t, jwtIdentity, "POST", base+"/load-balancers", body, nil, 200)
			lbID := accepted["load_balancer"].(map[string]any)["id"].(string)
			path := base + "/load-balancers/" + lbID
			require.Equal(t, accepted, request(t, akIdentity, "POST", base+"/load-balancers", body, nil, 200))
			request(t, akIdentity, "POST", base+"/load-balancers", strings.Replace(body, `"name":"deferred-lifecycle"`, `"name":"changed-intent"`, 1), nil, 409)
			lb := get(t, jwtIdentity, path)
			require.True(t, waiting(lb))
			require.Equal(t, "1", lb["desired_version"])
			require.Equal(t, "0", lb["provider_target_version"])
			require.Equal(t, "0", lb["applied_version"])
			require.Equal(t, "waiting_for_backends", lb["listeners"].([]any)[0].(map[string]any)["deployment_state"])
			require.Nil(t, lb["observed_at"], "database-only acceptance must not manufacture an observation")
			poll(t, akIdentity, base+"/load-balancers/operations/"+accepted["operation"].(map[string]any)["id"].(string), state("succeeded"))
			oldBinding, oldName, uid := binding(lbID)
			require.Empty(t, uid)
			gateway(oldName, 404)
			require.Equal(t, before, mutations())
			// Build writable DTOs explicitly. GET deployment facts are never echoed
			// back into PATCH or included in its persistent idempotency fingerprint.
			first := listenerInput("first", 80)
			first["id"] = lb["listeners"].([]any)[0].(map[string]any)["id"]
			backend := func() map[string]any {
				return map[string]any{"subnet_id": peer.BackendSubnet, "address": peer.BackendAddress, "port": 8080}
			}
			update := func(key string, items []any, ready func(map[string]any) bool) map[string]any {
				var receipt map[string]any
				var body string
				for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
					lb = get(t, jwtIdentity, path)
					var beforeOperations int
					require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT count(*) FROM network_operations WHERE tenant_id=$1 AND lb_id=$2`, peer.TenantID, lbID).Scan(&beforeOperations))
					body = jsonBody(map[string]any{"expected_version": lb["version"], "idempotency_key": key, "data": map[string]any{"listeners": map[string]any{"items": items}}, "update_mask": "listeners"})
					status, out := send(t, akIdentity, "PATCH", path, body, nil)
					if status == 200 {
						receipt = out
						break
					}
					require.Equal(t, 412, status, "PATCH %s response=%v", path, out)
					require.Equal(t, "VERSION_CONFLICT", out["reason"], "only an observation version race may refresh expected_version")
					var operations, savedKey int
					var desiredVersion string
					require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM network_operations WHERE tenant_id=$1 AND lb_id=$2),(SELECT count(*) FROM network_idempotency WHERE tenant_id=$1 AND operation_kind='update_load_balancer' AND idempotency_key=$3),desired_version::text FROM network_load_balancers WHERE tenant_id=$1 AND lb_id=$2`, peer.TenantID, lbID, key).Scan(&operations, &savedKey, &desiredVersion))
					require.Equal(t, beforeOperations, operations, "version conflict must not accept an operation")
					require.Zero(t, savedKey, "version conflict must not persist an idempotency receipt")
					require.Equal(t, lb["desired_version"], desiredVersion, "version conflict must not save configuration")
					// Worker observations may advance version after GET, even when the
					// prior operation and running configuration have already converged.
					t.Logf("refresh expected_version after observation race: key=%s version=%v", key, lb["version"])
					time.Sleep(40 * time.Millisecond)
				}
				require.NotNil(t, receipt, "PATCH must be accepted within the existing 15 second worker deadline")
				require.Equal(t, receipt, request(t, jwtIdentity, "PATCH", path, body, nil, 200))
				poll(t, jwtIdentity, base+"/load-balancers/operations/"+receipt["operation"].(map[string]any)["id"].(string), state("succeeded"))
				lb = poll(t, akIdentity, path, ready)
				return receipt
			}
			first["backends"] = []any{backend()}
			update("deferred-activate", []any{first}, configured)
			require.Equal(t, "2", lb["provider_target_version"])
			_, _, oldUID := binding(lbID)
			require.NotEmpty(t, oldUID)
			originalGateway := gateway(oldName, 200)
			first["backends"].([]any)[0].(map[string]any)["id"] = lb["listeners"].([]any)[0].(map[string]any)["backends"].([]any)[0].(map[string]any)["id"]
			second := listenerInput("second", 8081)
			beforeMixed := mutations()
			update("deferred-add-empty", []any{first, second}, configured)
			require.Equal(t, "3", lb["desired_version"])
			require.Equal(t, "2", lb["provider_target_version"])
			require.Equal(t, "2", lb["applied_version"])
			require.Equal(t, beforeMixed, mutations(), "adding an empty listener must preserve running projection")
			require.Equal(t, originalGateway["spec"], gateway(oldName, 200)["spec"])
			second["id"] = lb["listeners"].([]any)[1].(map[string]any)["id"]
			require.Equal(t, "waiting_for_backends", lb["listeners"].([]any)[1].(map[string]any)["deployment_state"])
			second["backends"] = []any{backend()}
			update("deferred-activate-second", []any{first, second}, configured)
			require.Len(t, gateway(oldName, 200)["spec"].(map[string]any)["listeners"], 2)
			second["backends"].([]any)[0].(map[string]any)["id"] = lb["listeners"].([]any)[1].(map[string]any)["backends"].([]any)[0].(map[string]any)["id"]
			first["backends"] = []any{}
			update("deferred-clear-first", []any{first, second}, configured)
			_, _, partialUID := binding(lbID)
			require.Equal(t, oldUID, partialUID)
			require.Len(t, gateway(oldName, 200)["spec"].(map[string]any)["listeners"], 1)
			var activeBackend int
			require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT count(*) FROM network_lb_components WHERE tenant_id=$1 AND lb_id=$2 AND kind='backend' AND deleted_at IS NULL`, peer.TenantID, lbID).Scan(&activeBackend))
			require.Positive(t, activeBackend, "second listener retains shared backend")
			second["backends"] = []any{}
			update("deferred-clear-all", []any{first, second}, waiting)
			require.Equal(t, "6", lb["provider_target_version"])
			gateway(oldName, 404)
			var retired, generated, backendRefs, vipIntent int
			require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM network_lb_retired_bindings WHERE tenant_id=$1 AND lb_id=$2 AND binding_id=$3 AND provider_uid=$4 AND cleanup_target_version=6),(SELECT count(*) FROM network_lb_generated_resources WHERE tenant_id=$1 AND lb_id=$2 AND gateway_uid=$4 AND released_at IS NULL),(SELECT count(*) FROM network_lb_subnet_refs WHERE tenant_id=$1 AND lb_id=$2 AND subnet_id=$5 AND released_at IS NULL),(SELECT count(*) FROM network_lb_vip_intents WHERE tenant_id=$1 AND lb_id=$2 AND released_at IS NULL)`, peer.TenantID, lbID, oldBinding, oldUID, peer.BackendSubnet).Scan(&retired, &generated, &backendRefs, &vipIntent))
			require.Equal(t, 1, retired)
			require.Zero(t, generated)
			require.Zero(t, backendRefs)
			require.Equal(t, 1, vipIntent, "configuration retains selected VIP reservation")
			first["backends"] = []any{backend()}
			update("deferred-reactivate", []any{first, second}, configured)
			newBinding, newName, newUID := binding(lbID)
			require.NotEqual(t, oldBinding, newBinding)
			require.NotEqual(t, oldName, newName)
			require.NotEqual(t, oldUID, newUID)
			require.Equal(t, "7", lb["provider_target_version"])
			require.Equal(t, first["id"], lb["listeners"].([]any)[0].(map[string]any)["id"])
			gateway(newName, 200)
			for _, foreign := range []identity{{bearer: otherToken}, {key: otherAK, secret: otherSK}} {
				request(t, foreign, "GET", path, "", nil, 404)
				request(t, foreign, "PATCH", path, jsonBody(map[string]any{"expected_version": lb["version"], "idempotency_key": "deferred-foreign", "data": map[string]any{"listeners": map[string]any{"items": []any{first, second}}}, "update_mask": "listeners"}), nil, 404)
			}
			request(t, jwtIdentity, "PATCH", path, `{"expected_version":"1","idempotency_key":"deferred-stale","data":{"listeners":{"items":[{"name":"first","port":80}]}},"update_mask":"listeners"}`, nil, 412)
			request(t, akIdentity, "PATCH", path, jsonBody(map[string]any{"expected_version": lb["version"], "idempotency_key": "deferred-readonly", "data": map[string]any{"listeners": map[string]any{"items": []any{lb["listeners"].([]any)[0]}}}, "update_mask": "listeners"}), nil, 400)
			request(t, akIdentity, "DELETE", path, "", nil, 200)
			poll(t, jwtIdentity, path, state("deleted"))
			gateway(newName, 404)
			var occupied int
			require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM network_lb_vip_intents WHERE tenant_id=$1 AND lb_id=$2 AND released_at IS NULL)+(SELECT count(*) FROM network_lb_subnet_refs WHERE tenant_id=$1 AND lb_id=$2 AND released_at IS NULL)+(SELECT count(*) FROM network_lb_components WHERE tenant_id=$1 AND lb_id=$2 AND deleted_at IS NULL)`, peer.TenantID, lbID).Scan(&occupied))
			require.Zero(t, occupied)
			require.Equal(t, "available", get(t, jwtIdentity, base+"/vpcs/"+peer.ParentVPC)["state"])
			require.Equal(t, "available", get(t, akIdentity, base+"/subnets/"+peer.BackendSubnet)["state"])
			require.Equal(t, accepted, request(t, akIdentity, "POST", base+"/load-balancers", body, nil, 200), "original receipt must remain stable through runtime incarnations")
		})
	})
	t.Run("platform_network", func(t *testing.T) {
		const platformBase = "/api/v1/admin/networks"
		caller := identity{bearer: platformToken}
		t.Run("denials", func(t *testing.T) {
			before := beforeSideEffects(t)
			for _, id := range []identity{jwtIdentity, akIdentity, {bearer: readerToken}, {bearer: otherToken}, {bearer: deniedPlatformToken}} {
				request(t, id, "GET", platformBase+"/public-address-pools", "", nil, 403)
			}
			request(t, identity{}, "GET", platformBase+"/capabilities", "", nil, 401)
			for _, query := range []string{"limit=0", "limit=101", "limit=01", "page=1", "cursor=x&offset=1", "limit=1&limit=2", "state=unknown", "tenant_id=0"} {
				request(t, caller, "GET", platformBase+"/public-address-pools?"+query, "", nil, 400)
			}
			request(t, caller, "GET", platformBase+"/capabilities", "", func(r *http.Request) { r.Header.Set("X-Ani-Scope", "platform") }, 400)
			request(t, caller, "POST", platformBase+"/egress-gateways", `{"name":"flat","idempotency_key":"flat"}`, nil, 400)
			request(t, caller, "POST", platformBase+"/egress-gateways", `{"data":{"name":"forged","tenant_id":0}}`, nil, 400)
			require.Equal(t, before, beforeSideEffects(t))
		})
		t.Run("gateway_vertical_flow", func(t *testing.T) {
			body := `{"data":{"name":"gov-platform-gateway","description":"platform HTTP fixture","idempotency_key":"gov-platform-gateway"}}`
			created := request(t, caller, "POST", platformBase+"/egress-gateways", body, nil, 200)
			require.NotContains(t, created, "resource")
			gid := created["id"].(string)
			require.Regexp(t, `^egw_[0-9a-f]{32}$`, gid)
			defer func() {
				request(t, caller, "DELETE", platformBase+"/egress-gateways/"+gid, "", nil, 200)
				poll(t, caller, platformBase+"/egress-gateways/"+gid, state("deleted"))
			}()
			replay := request(t, caller, "POST", platformBase+"/egress-gateways", body, nil, 200)
			require.Equal(t, gid, replay["id"])
			var savedActor, savedState string
			require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT r.state,h.actor_ref FROM network_platform_resources r JOIN network_platform_history h ON h.operation_id=r.last_operation_id WHERE r.resource_id=$1 AND h.event='create_egress_gateway_accepted'`, gid).Scan(&savedState, &savedActor))
			require.Equal(t, "governance:user:9", savedActor)
			require.Contains(t, []string{"provisioning", "available"}, savedState)
			poll(t, caller, platformBase+"/egress-gateways/"+gid, state("available"))
			operationID := created["last_operation_id"].(string)
			op := get(t, caller, platformBase+"/operations/"+operationID)
			require.Equal(t, operationID, op["id"])
			require.NotContains(t, op, "operation")
			page := get(t, caller, platformBase+"/egress-gateways?limit=1")
			total := page["total"].(string)
			require.NotEqual(t, "1", total)
			cursor := page["next_cursor"].(string)
			require.NotEmpty(t, cursor)
			next := get(t, caller, platformBase+"/egress-gateways?limit=1&cursor="+url.QueryEscape(cursor))
			require.Equal(t, total, next["total"])
			request(t, caller, "GET", platformBase+"/egress-gateways?limit=1&name=changed&cursor="+url.QueryEscape(cursor), "", nil, 400)
			request(t, caller, "GET", platformBase+"/vlan-networks?limit=1&cursor="+url.QueryEscape(cursor), "", nil, 400)
			request(t, caller, "GET", platformBase+"/egress-gateways?limit=1&cursor="+url.QueryEscape(cursor+"x"), "", nil, 400)
			empty := get(t, caller, platformBase+"/egress-gateways?name=not-present")
			require.Equal(t, "0", empty["total"])
			require.Empty(t, empty["items"])
		})

		t.Run("layer2_lifecycle", func(t *testing.T) {
			inventory := get(t, caller, platformBase+"/node-interfaces")
			var usable map[string]any
			for _, raw := range inventory["items"].([]any) {
				v := raw.(map[string]any)
				if v["name"] == "phys0" {
					usable = v
				}
			}
			require.NotNil(t, usable)
			require.Equal(t, true, usable["selectable"])
			data := map[string]any{"name": "gov-physical-device", "device_name": "phys0", "inventory_fingerprint": inventory["inventory_fingerprint"], "idempotency_key": "gov-device"}
			encoded, err := json.Marshal(map[string]any{"data": data})
			require.NoError(t, err)
			device := request(t, caller, "POST", platformBase+"/devices", string(encoded), nil, 200)
			did := device["id"].(string)
			require.Equal(t, did, request(t, caller, "POST", platformBase+"/devices", string(encoded), nil, 200)["id"])
			poll(t, caller, platformBase+"/devices/"+did, state("available"))
			data = map[string]any{"name": "gov-layer2", "device_id": did, "vlan_id": 100, "idempotency_key": "gov-layer2"}
			encoded, err = json.Marshal(map[string]any{"data": data})
			require.NoError(t, err)
			vlan := request(t, caller, "POST", platformBase+"/vlan-networks", string(encoded), nil, 200)
			vid := vlan["id"].(string)
			defer func() {
				request(t, caller, "DELETE", platformBase+"/vlan-networks/"+vid, "", nil, 200)
				poll(t, caller, platformBase+"/vlan-networks/"+vid, state("deleted"))
			}()
			current := poll(t, caller, platformBase+"/vlan-networks/"+vid, state("available"))
			require.Equal(t, did, current["vlan"].(map[string]any)["device_id"])
			require.Equal(t, float64(100), current["vlan"].(map[string]any)["vlan_id"])
			page := get(t, caller, platformBase+"/vlan-networks?limit=1")
			require.Equal(t, "1", page["total"])
			require.Empty(t, page["next_cursor"])
			var state, actor string
			require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT r.state,h.actor_ref FROM network_platform_resources r JOIN network_platform_history h ON h.operation_id=r.last_operation_id WHERE r.resource_id=$1 AND h.event='create_vlan_accepted'`, vid).Scan(&state, &actor))
			require.Equal(t, "available", state)
			require.Equal(t, "governance:user:9", actor)
		})
		t.Run("address_pool_lifecycles", func(t *testing.T) {
			for _, scope := range []string{"public", "intranet"} {
				t.Run(scope, func(t *testing.T) {
					path := platformBase + "/" + scope + "-address-pools"
					existing := get(t, caller, path)["items"].([]any)[0].(map[string]any)
					oldID := existing["id"].(string)
					configKey := "pool"
					if scope == "intranet" {
						configKey = "intranet_pool"
					}
					config := existing[configKey].(map[string]any)
					data := map[string]any{"name": "gov-" + scope + "-pool", "idempotency_key": "gov-" + scope + "-pool"}
					if scope == "public" {
						data["mode"] = "overlay"
						data["gateway_id"] = config["gateway_id"]
						data["cidr"] = "198.51.100.0/24"
						data["ovn_gateway_ip"] = "198.51.100.1"
					} else {
						data["cidr"] = "10.232.253.0/24"
						data["ovn_gateway_ip"] = "10.232.253.1"
						data["default_vpc_name"] = config["default_vpc_name"]
						data["default_vpc_uid"] = config["default_vpc_uid"]
						data["intranet_networks"] = config["intranet_networks"]
					}
					encoded, err := json.Marshal(map[string]any{"data": data})
					require.NoError(t, err)
					created := request(t, caller, "POST", path, string(encoded), nil, 200)
					pid := created["id"].(string)
					require.NotContains(t, created, "resource")
					defer func() {
						request(t, caller, "DELETE", path+"/"+pid, "", nil, 200)
						poll(t, caller, path+"/"+pid, state("deleted"))
					}()
					require.Equal(t, pid, request(t, caller, "POST", path, string(encoded), nil, 200)["id"])
					poll(t, caller, path+"/"+pid, state("available"))
					var stored, storedScope string
					require.NoError(t, peer.DB.QueryRowContext(ctx, "SELECT r.kind,p.scope FROM network_platform_resources r JOIN network_public_pools p ON p.resource_id=r.resource_id WHERE r.resource_id=$1", pid).Scan(&stored, &storedScope))
					require.Equal(t, "public_pool", stored)
					require.Equal(t, scope, storedScope)
					first := get(t, caller, path+"?limit=1")
					require.Equal(t, "2", first["total"])
					next := get(t, caller, path+"?limit=1&cursor="+url.QueryEscape(first["next_cursor"].(string)))
					require.Equal(t, "2", next["total"])
					require.Empty(t, next["next_cursor"])
					// Exercise the existing default pool's real evidence and commands, preserving
					// the fixture's original default and allocation state for tenant regressions.
					current := get(t, caller, path+"/"+oldID)
					pool := current[configKey].(map[string]any)
					verification := pool["verification"]
					require.NotNil(t, verification)
					command := func(action, verb string, extras map[string]any) map[string]any {
						current := get(t, caller, path+"/"+oldID)
						data := map[string]any{"expected_version": current["version"], "idempotency_key": "gov-" + scope + "-" + action}
						for k, v := range extras {
							data[k] = v
						}
						body, err := json.Marshal(map[string]any{"data": data})
						require.NoError(t, err)
						suffix := action
						if strings.HasPrefix(action, "allocation-") {
							suffix = "allocation"
						}
						out := request(t, caller, verb, path+"/"+oldID+"/"+suffix, string(body), nil, 200)
						require.Equal(t, oldID, out["id"])
						require.NotContains(t, out, "resource")
						return out
					}
					command("verification", "POST", map[string]any{"verification": verification})
					command("allocation-off", "PATCH", map[string]any{"enabled": false})
					command("allocation-on", "PATCH", map[string]any{"enabled": true})
					command("default", "POST", nil)
				})
			}
		})
		for _, path := range []string{"public-address-pools", "intranet-address-pools", "vlan-networks", "node-interfaces", "capabilities"} {
			t.Run(path, func(t *testing.T) { get(t, caller, platformBase+"/"+path) })
		}
	})

	t.Run("fixed_flavors", func(t *testing.T) {
		for _, scenario := range []struct {
			flavor string
			caller identity
		}{{"small", jwtIdentity}, {"medium", akIdentity}, {"large", jwtIdentity}} {
			t.Run(scenario.flavor, func(t *testing.T) {
				id := scenario.caller
				body := fmt.Sprintf(`{"name":%q,"vpc_id":%q,"subnet_id":%q,"exposure":"LOAD_BALANCER_EXPOSURE_PRIVATE","flavor":%q,"private_ip":"10.42.1.100","idempotency_key":%q,"listener":{"protocol":"LOAD_BALANCER_LISTENER_PROTOCOL_HTTP","port":80},"backends":[{"subnet_id":%q,"address":%q,"port":8080}],"health_check":{"port":8080}}`, "fixed-"+scenario.flavor, peer.ParentVPC, peer.EntrySubnet, scenario.flavor, "fixed-"+scenario.flavor, peer.BackendSubnet, peer.BackendAddress)
				accepted := request(t, id, "POST", base+"/load-balancers", body, nil, 200)
				lbID := accepted["load_balancer"].(map[string]any)["id"].(string)
				require.Equal(t, accepted, request(t, id, "POST", base+"/load-balancers", body, nil, 200))
				lb := poll(t, id, base+"/load-balancers/"+lbID, func(v map[string]any) bool {
					return v["state"] == "available" && v["desired_version"] == v["applied_version"]
				})
				require.Equal(t, scenario.flavor, lb["flavor"])
				var stored string
				require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT flavor FROM network_load_balancers WHERE tenant_id=$1 AND lb_id=$2`, peer.TenantID, lbID).Scan(&stored))
				require.Equal(t, scenario.flavor, stored)
				request(t, id, "DELETE", base+"/load-balancers/"+lbID, "", nil, 200)
				poll(t, id, base+"/load-balancers/"+lbID, state("deleted"))
				var occupied int
				require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM network_lb_vip_intents WHERE tenant_id=$1 AND lb_id=$2 AND released_at IS NULL)+(SELECT count(*) FROM network_lb_subnet_refs WHERE tenant_id=$1 AND lb_id=$2 AND released_at IS NULL)`, peer.TenantID, lbID).Scan(&occupied))
				require.Zero(t, occupied)
			})
		}
	})
	t.Run("vpc_presets", func(t *testing.T) {
		vpcCIDR, subnetCIDR := "10.61.0.0/16", "10.61.1.0/24"
		if peer.LiveClusterUID != "" {
			vpcCIDR, subnetCIDR = peer.VPCCIDR, peer.SubnetCIDR
		}
		choices := get(t, jwtIdentity, base+"/vpc-cidr-presets")
		require.Equal(t, choices, get(t, akIdentity, base+"/vpc-cidr-presets"))
		values := choices["cidrs"].([]any)
		require.Contains(t, values, vpcCIDR)
		require.NotContains(t, values, "10.96.0.0/16")
		require.NotContains(t, values, "172.16.101.0/24")
		t.Run("lifecycle", func(t *testing.T) {
			for _, scenario := range []struct {
				name   string
				caller identity
			}{{"jwt", jwtIdentity}, {"ak", akIdentity}} {
				body := fmt.Sprintf(`{"name":%q,"cidr":%q,"idempotency_key":%q}`, "preset-"+scenario.name, vpcCIDR, "preset-"+scenario.name)
				accepted := request(t, scenario.caller, "POST", base+"/vpcs", body, nil, 200)
				require.Equal(t, accepted, request(t, scenario.caller, "POST", base+"/vpcs", body, nil, 200))
				id := accepted["id"].(string)
				poll(t, scenario.caller, base+"/vpcs/"+id, state("available"))
				sub := request(t, scenario.caller, "POST", base+"/subnets", fmt.Sprintf(`{"vpc_id":%q,"name":"preset-child","cidr":%q,"idempotency_key":%q}`, id, subnetCIDR, "preset-child-"+scenario.name), nil, 200)
				subID := sub["id"].(string)
				poll(t, scenario.caller, base+"/subnets/"+subID, state("available"))
				request(t, scenario.caller, "DELETE", base+"/subnets/"+subID, "", nil, 200)
				poll(t, scenario.caller, base+"/subnets/"+subID, state("deleted"))
				request(t, scenario.caller, "DELETE", base+"/vpcs/"+id, "", nil, 200)
				poll(t, scenario.caller, base+"/vpcs/"+id, state("deleted"))
			}
		})
		t.Run("rejections", func(t *testing.T) {
			before := beforeSideEffects(t)
			request(t, akIdentity, "POST", base+"/vpcs", `{"name":"custom","cidr":"10.63.0.0/16","idempotency_key":"custom"}`, nil, 400)
			request(t, jwtIdentity, "POST", base+"/vpcs", `{"name":"conflict","cidr":"10.96.0.0/16","idempotency_key":"conflict"}`, nil, 412)
			require.Equal(t, before, beforeSideEffects(t))
		})
	})
	t.Run("multiple_listeners", func(t *testing.T) {
		jsonBody := func(v any) string { raw, err := json.Marshal(v); require.NoError(t, err); return string(raw) }
		listener := func(name, protocol string, port int, address string) map[string]any {
			return map[string]any{"name": name, "protocol": "LOAD_BALANCER_LISTENER_PROTOCOL_" + protocol, "port": port, "backends": []any{map[string]any{"subnet_id": peer.BackendSubnet, "address": address, "port": 8080}}, "health_check": map[string]any{"port": 0}}
		}
		first := listener("first", "HTTP", 80, peer.BackendAddress)
		first["algorithm"] = "LOAD_BALANCER_ALGORITHM_LEAST_REQUEST"
		first["gzip_enabled"] = true
		first["max_connections"] = 100
		first["session_persistence"] = map[string]any{"mode": "LOAD_BALANCER_SESSION_MODE_COOKIE_TIMEOUT", "cookie_expiry_seconds": 600}
		first["health_check"] = map[string]any{"protocol": "LOAD_BALANCER_HEALTH_CHECK_PROTOCOL_HTTP", "port": 9090, "path": "/ready", "hostname": "health.example.com", "interval_seconds": 2, "timeout_seconds": 5}
		second := listener("second", "HTTPS", 443, peer.SecondBackendAddress)
		second["certificate_ref"] = map[string]any{"name": peer.CertificateName}
		second["algorithm"] = "LOAD_BALANCER_ALGORITHM_SOURCE_IP"
		second["session_persistence"] = map[string]any{"mode": "LOAD_BALANCER_SESSION_MODE_COOKIE_SESSION"}
		tcp := listener("tcp", "TCP", 3001, peer.BackendAddress)
		tcp["max_connections"] = 200
		udp := listener("udp", "UDP", 3002, peer.BackendAddress)
		udp["health_check"] = map[string]any{"port": 9091}
		ssl := listener("ssl", "SSL", 3003, peer.BackendAddress)
		ssl["certificate_ref"] = map[string]any{"name": peer.CertificateName, "namespace": peer.Namespace}
		body := jsonBody(map[string]any{"name": "multi", "vpc_id": peer.ParentVPC, "subnet_id": peer.EntrySubnet, "exposure": "LOAD_BALANCER_EXPOSURE_PRIVATE", "private_ip": "10.42.1.100", "idempotency_key": "multi-create", "listeners": map[string]any{"items": []any{first, second, tcp, udp, ssl}}})
		accepted := request(t, jwtIdentity, "POST", base+"/load-balancers", body, nil, 200)
		lbID := accepted["load_balancer"].(map[string]any)["id"].(string)
		require.Equal(t, accepted, request(t, akIdentity, "POST", base+"/load-balancers", body, nil, 200))
		path := base + "/load-balancers/" + lbID
		configured := func(v map[string]any) bool {
			return v["state"] == "available" && v["configuration_state"] == "LOAD_BALANCER_CONFIGURATION_STATE_CONFIGURED" && v["desired_version"] == v["applied_version"]
		}
		lb := poll(t, jwtIdentity, path, configured)
		items := lb["listeners"].([]any)
		require.Len(t, items, 5)
		saved := map[string]map[string]any{}
		for _, item := range items {
			v := item.(map[string]any)
			saved[v["name"].(string)] = v
		}
		for _, input := range []map[string]any{first, second, tcp, udp, ssl} {
			input["id"] = saved[input["name"].(string)]["id"]
			require.Equal(t, input["protocol"], saved[input["name"].(string)]["protocol"])
		}
		require.Equal(t, "LOAD_BALANCER_ALGORITHM_LEAST_REQUEST", saved["first"]["algorithm"])
		require.Equal(t, float64(100), saved["first"]["max_connections"])
		require.Equal(t, "/ready", saved["first"]["health_check"].(map[string]any)["path"])
		require.Equal(t, float64(600), saved["first"]["session_persistence"].(map[string]any)["cookie_expiry_seconds"])
		require.Equal(t, peer.Namespace, saved["second"]["certificate_ref"].(map[string]any)["namespace"])
		external := func(resource, kind string, id any) map[string]any {
			var name string
			require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT provider_name FROM network_lb_components WHERE tenant_id=$1 AND lb_id=$2 AND listener_id=$3 AND kind=$4 AND deleted_at IS NULL`, peer.TenantID, lbID, id, kind).Scan(&name))
			group := "gateway.envoyproxy.io/v1alpha1"
			if resource == "httproutes" {
				group = "gateway.networking.k8s.io/v1"
			}
			if resource == "tcproutes" || resource == "udproutes" {
				group = "gateway.networking.k8s.io/v1alpha2"
			}
			response, err := http.Get(peer.ProviderURL + "/apis/" + group + "/namespaces/" + peer.Namespace + "/" + resource + "/" + name)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, 200, response.StatusCode)
			var obj map[string]any
			require.NoError(t, json.NewDecoder(response.Body).Decode(&obj))
			return obj["spec"].(map[string]any)
		}
		rule := external("httproutes", "route", first["id"])["rules"].([]any)[0].(map[string]any)
		require.Equal(t, "600s", rule["sessionPersistence"].(map[string]any)["absoluteTimeout"])
		policy := external("backendtrafficpolicies", "policy", first["id"])
		require.Equal(t, "default-rule", policy["targetRefs"].([]any)[0].(map[string]any)["sectionName"])
		require.Equal(t, float64(9090), policy["healthCheck"].(map[string]any)["active"].(map[string]any)["overrides"].(map[string]any)["port"])
		require.Equal(t, "Gzip", policy["compressor"].([]any)[0].(map[string]any)["type"])
		require.Len(t, external("tcproutes", "tcp_route", tcp["id"])["rules"], 1)
		require.Len(t, external("tcproutes", "tcp_route", ssl["id"])["rules"], 1)
		require.Len(t, external("udproutes", "udp_route", udp["id"])["rules"], 1)
		require.Len(t, external("envoypatchpolicies", "patch_policy", udp["id"])["jsonPatches"], 3)
		update := func(key string, set []any) {
			body := jsonBody(map[string]any{"expected_version": lb["version"], "idempotency_key": key, "data": map[string]any{"listeners": map[string]any{"items": set}}, "update_mask": "listeners"})
			receipt := request(t, akIdentity, "PATCH", path, body, nil, 200)
			require.Equal(t, receipt, request(t, jwtIdentity, "PATCH", path, body, nil, 200))
			lb = poll(t, jwtIdentity, path, configured)
		}
		delete(first, "session_persistence")
		delete(first, "gzip_enabled")
		delete(first, "max_connections")
		delete(first, "algorithm")
		first["health_check"] = map[string]any{"port": 0}
		second["port"] = 444
		third := listener("third", "HTTP", 8083, peer.BackendAddress)
		update("multi-add-change", []any{first, second, third})
		require.Len(t, lb["listeners"].([]any), 3)
		require.Nil(t, external("httproutes", "route", first["id"])["rules"].([]any)[0].(map[string]any)["sessionPersistence"])
		require.Nil(t, external("backendtrafficpolicies", "policy", first["id"])["compressor"])
		require.Equal(t, "LOAD_BALANCER_ALGORITHM_ROUND_ROBIN", lb["listeners"].([]any)[0].(map[string]any)["algorithm"])
		first["https_redirect_status_code"] = 302
		delete(first, "backends")
		update("multi-redirect-remove", []any{first, second})
		rule = external("httproutes", "route", first["id"])["rules"].([]any)[0].(map[string]any)
		require.Nil(t, rule["backendRefs"])
		require.Equal(t, float64(302), rule["filters"].([]any)[0].(map[string]any)["requestRedirect"].(map[string]any)["statusCode"])
		require.Equal(t, float64(444), rule["filters"].([]any)[0].(map[string]any)["requestRedirect"].(map[string]any)["port"])
		request(t, akIdentity, "DELETE", path, "", nil, 200)
		poll(t, jwtIdentity, path, state("deleted"))
		var occupied int
		require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM network_lb_vip_intents WHERE tenant_id=$1 AND lb_id=$2 AND released_at IS NULL)+(SELECT count(*) FROM network_lb_subnet_refs WHERE tenant_id=$1 AND lb_id=$2 AND released_at IS NULL)+(SELECT count(*) FROM network_lb_components WHERE tenant_id=$1 AND lb_id=$2 AND deleted_at IS NULL)`, peer.TenantID, lbID).Scan(&occupied))
		require.Zero(t, occupied)
	})
	t.Run("subnet_ip_ranges", func(t *testing.T) {
		path := base + "/subnets/" + peer.EntrySubnet
		var ns, name string
		require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT namespace,provider_name FROM network_provider_bindings WHERE tenant_id=$1 AND subnet_id=$2 AND resource_kind='subnet'`, peer.TenantID, peer.EntrySubnet).Scan(&ns, &name))
		externalPath := peer.ProviderURL + "/apis/networking.kubercloud.com/v1/namespaces/" + ns + "/subnets/" + name
		change := func(available, used any) {
			response, err := http.Get(externalPath)
			require.NoError(t, err)
			var obj map[string]any
			require.NoError(t, json.NewDecoder(response.Body).Decode(&obj))
			response.Body.Close()
			status := obj["status"].(map[string]any)
			status["v4availableIPs"] = 999
			delete(status, "v4availableIPrange")
			delete(status, "v4usingIPrange")
			if available != nil {
				status["v4availableIPrange"] = available
			}
			if used != nil {
				status["v4usingIPrange"] = used
			}
			raw, err := json.Marshal(obj)
			require.NoError(t, err)
			r, err := http.NewRequest(http.MethodPut, externalPath+"/status", bytes.NewReader(raw))
			require.NoError(t, err)
			r.Header.Set("Content-Type", "application/json")
			response, err = http.DefaultClient.Do(r)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, 200, response.StatusCode)
		}
		// Earlier LB scenarios can populate the external controller's ranges.
		// Establish this scenario's unknown facts through its existing status API;
		// the production observer must persist that transition before assertions.
		change(nil, nil)
		unknown := poll(t, jwtIdentity, path, func(v map[string]any) bool {
			_, available := v["available_ip_range"]
			_, used := v["used_ip_range"]
			return !available && !used
		})
		_, present := unknown["available_ip_range"]
		require.False(t, present)

		change("10.42.1.20-10.42.1.254", "10.42.1.2,10.42.1.4-10.42.1.19")
		v := poll(t, jwtIdentity, path, func(v map[string]any) bool {
			return v["available_ip_range"] == "10.42.1.20-10.42.1.254" && v["used_ip_range"] == "10.42.1.2,10.42.1.4-10.42.1.19"
		})
		require.NotContains(t, v, "available_ip_count")
		require.NotContains(t, v, "used_ip_count")
		observed := v["observed_at"]
		change("10.42.1.21-10.42.1.254", "10.42.1.2-10.42.1.20")
		v = poll(t, akIdentity, path, func(v map[string]any) bool {
			return v["available_ip_range"] == "10.42.1.21-10.42.1.254" && v["used_ip_range"] == "10.42.1.2-10.42.1.20" && v["observed_at"] != observed
		})
		change("", "")
		poll(t, jwtIdentity, path, func(v map[string]any) bool { return v["available_ip_range"] == "" && v["used_ip_range"] == "" })
		change(nil, 123)
		poll(t, akIdentity, path, func(v map[string]any) bool {
			_, a := v["available_ip_range"]
			_, u := v["used_ip_range"]
			return !a && !u
		})
		request(t, identity{bearer: otherToken}, "GET", path, "", nil, 404)
	})
	for _, mode := range []struct {
		name string
		id   identity
	}{{"jwt", jwtIdentity}, {"ak", akIdentity}} {
		t.Run(mode.name, func(t *testing.T) {
			id := mode.id
			vpcBody := fmt.Sprintf(`{"name":%q,"cidr":"10.61.0.0/16","description":"joint HTTP durable vertical","idempotency_key":%q}`, "joint-"+mode.name, "vpc-"+mode.name)
			vpc := request(t, id, "POST", base+"/vpcs", vpcBody, nil, 200)
			require.NotContains(t, vpc, "vpc")
			require.Equal(t, "provisioning", vpc["state"])
			vpcID := vpc["id"].(string)
			assertPersisted(t, "network_vpcs", "vpc_id", vpcID, peer.TenantID, "provisioning")
			replay := request(t, id, "POST", base+"/vpcs", vpcBody, nil, 200)
			require.Equal(t, vpc, replay)
			request(t, id, "POST", base+"/vpcs", strings.Replace(vpcBody, "10.61.0.0/16", "10.62.0.0/16", 1), nil, 409)
			poll(t, id, base+"/vpcs/"+vpcID, state("available"))
			opID := vpc["last_operation_id"].(string)
			poll(t, id, base+"/operations/"+opID, state("succeeded"))
			listed := get(t, id, base+"/vpcs?name=joint-"+mode.name+"&limit=1")
			require.Equal(t, "1", listed["total"])
			ids := make([]string, 2)
			for i := range ids {
				body := fmt.Sprintf(`{"name":"child-%d","vpc_id":%q,"cidr":"10.61.%d.0/24","description":"subnet durable child","idempotency_key":%q}`, i, vpcID, i+1, "subnet-"+mode.name+strconv.Itoa(i))
				sub := request(t, id, "POST", base+"/subnets", body, nil, 200)
				require.NotContains(t, sub, "subnet")
				require.Equal(t, vpcID, sub["vpc_id"])
				require.Equal(t, "provisioning", sub["state"])
				ids[i] = sub["id"].(string)
				require.Equal(t, sub, request(t, id, "POST", base+"/subnets", body, nil, 200))
				assertPersisted(t, "network_subnets", "subnet_id", ids[i], peer.TenantID, "provisioning")
				poll(t, id, base+"/subnets/"+ids[i], state("available"))
			}
			first := get(t, id, base+"/subnets?vpc_id="+vpcID+"&limit=1")
			require.Equal(t, "2", first["total"])
			require.Len(t, first["items"], 1)
			require.NotEmpty(t, first["next_cursor"])
			second := get(t, id, base+"/subnets?vpc_id="+vpcID+"&limit=1&cursor="+url.QueryEscape(first["next_cursor"].(string)))
			require.Equal(t, "2", second["total"])
			require.Len(t, second["items"], 1)
			filtered := get(t, id, base+"/subnets?vpc_id="+vpcID+"&name=child-0&state=available")
			require.Equal(t, "1", filtered["total"])
			request(t, id, "DELETE", base+"/vpcs/"+vpcID, "", nil, 412)
			foreignBody := fmt.Sprintf(`{"name":"foreign-child","vpc_id":%q,"cidr":"10.61.9.0/24","idempotency_key":%q}`, vpcID, "foreign-parent-"+mode.name)
			request(t, identity{bearer: otherToken}, "POST", base+"/subnets", foreignBody, nil, 404)
			request(t, identity{key: otherAK, secret: otherSK}, "POST", base+"/subnets", foreignBody, nil, 404)
			// The existing supply/platform fixture has a verified enabled Public pool.
			eipBody := fmt.Sprintf(`{"name":%q,"description":"independent address","idempotency_key":%q}`, "joint-eip-"+mode.name, "eip-"+mode.name)
			eip := request(t, id, "POST", base+"/eips", eipBody, nil, 200)
			require.NotContains(t, eip, "eip")
			eipID := eip["id"].(string)
			require.Equal(t, eip, request(t, id, "POST", base+"/eips", eipBody, nil, 200))
			poll(t, id, base+"/eips/"+eipID, state("available"))
			require.Equal(t, "1", get(t, id, base+"/eips?name=joint-eip-"+mode.name)["total"])
			snatBody := fmt.Sprintf(`{"eip_id":%q,"idempotency_key":%q}`, eipID, "snat-"+mode.name)
			snat := request(t, id, "POST", base+"/vpcs/"+vpcID+"/snat/bindings", snatBody, nil, 200)
			require.NotContains(t, snat, "snat")
			snatID := snat["id"].(string)
			require.Equal(t, snat, request(t, id, "POST", base+"/vpcs/"+vpcID+"/snat/bindings", snatBody, nil, 200))
			snat = poll(t, id, base+"/snat/bindings/"+snatID, func(v map[string]any) bool { return v["state"] == "available" && v["applied_enabled"] == true })
			require.Equal(t, snatID, get(t, id, base+"/vpcs/"+vpcID+"/snat")["id"])
			poll(t, id, base+"/operations/"+snat["last_operation_id"].(string), state("succeeded"))
			snat = get(t, id, base+"/snat/bindings/"+snatID)
			for _, enabled := range []bool{false, true} {
				body := fmt.Sprintf(`{"enabled":%t,"expected_version":%q,"idempotency_key":%q}`, enabled, snat["version"], fmt.Sprintf("toggle-%s-%t", mode.name, enabled))
				accepted := request(t, id, "PATCH", base+"/snat/bindings/"+snatID, body, nil, 200)
				require.Equal(t, enabled, accepted["desired_enabled"])
				require.NotEqual(t, accepted["desired_enabled"], accepted["applied_enabled"], "accepted intent must retain independently observed applied value")
				require.Equal(t, accepted, request(t, id, "PATCH", base+"/snat/bindings/"+snatID, body, nil, 200))
				snat = poll(t, id, base+"/snat/bindings/"+snatID, func(v map[string]any) bool { return v["state"] == "available" && v["applied_enabled"] == enabled })
				poll(t, id, base+"/operations/"+accepted["last_operation_id"].(string), state("succeeded"))
				// SNAT operation completion and its EIP/VPC observations converge
				// independently. Continuous observations also advance the resource
				// version, so fetch it after these waits before the next mutation.
				poll(t, id, base+"/eips/"+eipID, state("available"))
				poll(t, id, base+"/vpcs/"+vpcID, state("available"))
				snat = get(t, id, base+"/snat/bindings/"+snatID)
			}
			request(t, id, "PATCH", base+"/snat/bindings/"+snatID, `{"enabled":false,"expected_version":"1","idempotency_key":"stale-`+mode.name+`"}`, nil, 412)
			deleted := request(t, id, "DELETE", base+"/snat/bindings/"+snatID, "", nil, 200)
			require.NotContains(t, deleted, "snat")
			poll(t, id, base+"/snat/bindings/"+snatID, state("deleted"))
			require.Equal(t, "available", get(t, id, base+"/eips/"+eipID)["state"])
			// Backend attachment was prepared and confirmed using real owner protocol;
			// its Kubernetes/instance owner facts are the controlled external boundary.
			lbBody := fmt.Sprintf(`{"name":%q,"vpc_id":%q,"subnet_id":%q,"exposure":"LOAD_BALANCER_EXPOSURE_PUBLIC_PRIVATE","flavor":"small","private_ip":"10.42.1.100","public_eip_id":%q,"idempotency_key":%q,"listener":{"protocol":"LOAD_BALANCER_LISTENER_PROTOCOL_HTTP","port":80},"backends":[{"subnet_id":%q,"address":%q,"port":8080}],"health_check":{"port":8080}}`, "joint-lb-"+mode.name, peer.ParentVPC, peer.EntrySubnet, eipID, "lb-"+mode.name, peer.BackendSubnet, peer.BackendAddress)
			result := request(t, id, "POST", base+"/load-balancers", lbBody, nil, 200)
			require.Contains(t, result, "operation")
			lb := result["load_balancer"].(map[string]any)
			lbID := lb["id"].(string)
			require.Equal(t, result, request(t, id, "POST", base+"/load-balancers", lbBody, nil, 200))
			lb = poll(t, id, base+"/load-balancers/"+lbID, state("available"))
			require.Equal(t, "LOAD_BALANCER_DATA_PLANE_STATE_UNKNOWN", lb["data_plane_state"])
			lbOp := result["operation"].(map[string]any)["id"].(string)
			poll(t, id, base+"/load-balancers/operations/"+lbOp, state("succeeded"))
			require.Equal(t, "1", get(t, id, base+"/load-balancers?vpc_id="+peer.ParentVPC+"&name=joint-lb-"+mode.name+"&limit=1")["total"])
			request(t, id, "DELETE", base+"/subnets/"+peer.EntrySubnet, "", nil, 412)
			updateBody := fmt.Sprintf(`{"name":%q,"description":"updated over authenticated HTTP","expected_version":%q,"idempotency_key":%q,"backends":[{"id":%q,"subnet_id":%q,"address":%q,"port":8080,"weight":2}],"health_check":{"port":8080}}`, "updated-lb-"+mode.name, lb["version"], "lb-update-"+mode.name, lb["backends"].([]any)[0].(map[string]any)["id"], peer.BackendSubnet, peer.BackendAddress)
			updated := request(t, id, "PATCH", base+"/load-balancers/"+lbID, updateBody, nil, 200)
			require.Contains(t, updated, "operation")
			require.Contains(t, updated, "load_balancer")
			require.Equal(t, updated, request(t, id, "PATCH", base+"/load-balancers/"+lbID, updateBody, nil, 200))
			updateOp := updated["operation"].(map[string]any)["id"].(string)
			poll(t, id, base+"/load-balancers/operations/"+updateOp, state("succeeded"))
			poll(t, id, base+"/load-balancers/"+lbID, func(v map[string]any) bool {
				return v["state"] == "available" && v["desired_version"] == v["applied_version"]
			})
			request(t, id, "PATCH", base+"/load-balancers/"+lbID, strings.Replace(strings.Replace(updateBody, `"expected_version":"`+lb["version"].(string)+`"`, `"expected_version":"1"`, 1), "lb-update-"+mode.name, "stale-lb-update-"+mode.name, 1), nil, 412)
			removed := request(t, id, "DELETE", base+"/load-balancers/"+lbID, "", nil, 200)
			require.Contains(t, removed, "operation")
			require.Contains(t, removed, "load_balancer")
			poll(t, id, base+"/load-balancers/"+lbID, state("deleted"))
			var claims, refs, vips int
			require.NoError(t, peer.DB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM network_eip_claims WHERE tenant_id=$1 AND eip_id=$2 AND released_at IS NULL),(SELECT count(*) FROM network_lb_subnet_refs WHERE tenant_id=$1 AND lb_id=$3 AND released_at IS NULL),(SELECT count(*) FROM network_lb_vip_intents WHERE tenant_id=$1 AND lb_id=$3 AND released_at IS NULL)`, peer.TenantID, eipID, lbID).Scan(&claims, &refs, &vips))
			require.Zero(t, claims)
			require.Zero(t, refs)
			require.Zero(t, vips)
			require.Equal(t, "available", get(t, id, base+"/eips/"+eipID)["state"])
			require.Equal(t, "available", get(t, id, base+"/vpcs/"+peer.ParentVPC)["state"])
			require.Equal(t, "available", get(t, id, base+"/subnets/"+peer.BackendSubnet)["state"])
			for _, path := range []string{base + "/vpcs/" + vpcID, base + "/subnets/" + ids[0], base + "/eips/" + eipID, base + "/snat/bindings/" + snatID, base + "/load-balancers/" + lbID, base + "/load-balancers/operations/" + lbOp} {
				request(t, identity{bearer: otherToken}, "GET", path, "", nil, 404)
				request(t, identity{key: otherAK, secret: otherSK}, "GET", path, "", nil, 404)
			}
			request(t, id, "DELETE", base+"/eips/"+eipID, "", nil, 200)
			poll(t, id, base+"/eips/"+eipID, state("deleted"))
			for _, subnetID := range ids {
				removed := request(t, id, "DELETE", base+"/subnets/"+subnetID, "", nil, 200)
				require.NotContains(t, removed, "subnet")
				poll(t, id, base+"/subnets/"+subnetID, state("deleted"))
			}
			removed = request(t, id, "DELETE", base+"/vpcs/"+vpcID, "", nil, 200)
			require.NotContains(t, removed, "vpc")
			poll(t, id, base+"/vpcs/"+vpcID, state("deleted"))
		})
	}
	// Failed main-flow assertions return while fixture authorization is still
	// usable so compensation can release any accepted resources.
	if t.Failed() {
		return
	}
	t.Run("preset_access", func(t *testing.T) {
		before := beforeSideEffects(t)
		path := base + "/vpc-cidr-presets"
		request(t, identity{}, "GET", path, "", nil, 401)
		request(t, identity{bearer: readerToken}, "GET", path, "", nil, 403)
		request(t, identity{key: readerAK, secret: readerSK}, "GET", path, "", nil, 403)
		request(t, akIdentity, "GET", path+"?tenant_id=forged", "", nil, 400)
		for _, foreign := range []identity{{bearer: otherToken}, {key: otherAK, secret: otherSK}} {
			request(t, foreign, "GET", base+"/vpcs/"+peer.ParentVPC, "", nil, 404)
			body := fmt.Sprintf(`{"name":"foreign-listeners","vpc_id":%q,"subnet_id":%q,"exposure":"LOAD_BALANCER_EXPOSURE_PRIVATE","private_ip":"10.42.1.100","idempotency_key":"foreign-listeners","listeners":{"items":[{"name":"first","port":80,"backends":[{"subnet_id":%q,"address":%q,"port":8080}],"health_check":{"port":8080}}]}}`, peer.ParentVPC, peer.EntrySubnet, peer.BackendSubnet, peer.BackendAddress)
			request(t, foreign, "POST", base+"/load-balancers", body, nil, 404)
		}
		require.Equal(t, before, beforeSideEffects(t))
	})
	t.Run("authentication_and_plan", func(t *testing.T) {
		// Every rejection below is verified before a new Resource operation is saved.
		before := beforeSideEffects(t)
		validBody := fmt.Sprintf(`{"name":"negative","vpc_id":%q,"cidr":"10.42.8.0/24","idempotency_key":"negative"}`, peer.ParentVPC)
		path := base + "/subnets"
		request(t, identity{}, "POST", path, validBody, nil, 401)
		request(t, identity{bearer: readerToken}, "POST", path, validBody, nil, 403)
		request(t, identity{key: readerAK, secret: readerSK}, "POST", path, validBody, nil, 403)
		request(t, akIdentity, "POST", path, validBody, func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(validBody, "negative", "tampered", 1)))
			r.ContentLength = int64(len(strings.Replace(validBody, "negative", "tampered", 1)))
		}, 401)
		request(t, akIdentity, "GET", base+"/subnets?vpc_id="+peer.ParentVPC+"&limit=1", "", func(r *http.Request) { r.URL.RawQuery = "vpc_id=" + peer.ParentVPC + "&limit=2" }, 401)
		request(t, akIdentity, "GET", base+"/vpcs/"+peer.ParentVPC, "", func(r *http.Request) { r.URL.Path = base + "/vpcs/vpc_" + strings.Repeat("a", 32) }, 401)
		request(t, akIdentity, "DELETE", base+"/vpcs/"+peer.ParentVPC, "", func(r *http.Request) { r.Method = "GET" }, 401)
		for _, header := range []string{"X-Access-Key", "X-Timestamp", "X-Signature"} {
			denied := request(t, akIdentity, "POST", path, validBody, func(r *http.Request) { r.Header.Add(header, "duplicate") }, 401)
			require.Equal(t, "INVALID_SIGNATURE", denied["reason"], "duplicate credentials preserve the shared authentication contract")
		}
		request(t, jwtIdentity, "POST", path, validBody, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+publisherToken) }, 401)
		request(t, akIdentity, "POST", path, validBody, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+publisherToken) }, 400)
		request(t, akIdentity, "POST", path, validBody, func(r *http.Request) {
			ts := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
			r.Header.Set("X-Timestamp", ts)
			r.Header.Set("X-Signature", auth.NetworkSignature(publisherSK, "POST", path, "", publisherAK.AccessKey, ts, []byte(validBody)))
		}, 401)
		request(t, jwtIdentity, "POST", path, validBody, func(r *http.Request) { r.Header.Add("X-Ani-Tenant-Id", "forged") }, 400)
		request(t, jwtIdentity, "POST", path, strings.Replace(validBody, `"name":"negative"`, `"tenant_id":"forged","name":"negative"`, 1), nil, 400)
		request(t, akIdentity, "GET", base+"/vpcs?limit=1&limit=2", "", nil, 400)
		request(t, akIdentity, "POST", base+"/unknown", validBody, nil, 400)
		require.Equal(t, before, beforeSideEffects(t))
		_, err = client.Tenant.UpdateOneID(tenants[0].ID).SetStatus(tenant.StatusOff).Save(sys)
		require.NoError(t, err)
		request(t, jwtIdentity, "POST", path, validBody, nil, 403)
		request(t, akIdentity, "POST", path, validBody, nil, 403)
		_, err = client.Tenant.UpdateOneID(tenants[0].ID).SetStatus(tenant.StatusOn).Save(sys)
		require.NoError(t, err)
		_, err = client.AccessKey.UpdateOneID(publisherAK.ID).SetStatus(accesskey.StatusOff).Save(sys)
		require.NoError(t, err)
		request(t, akIdentity, "POST", path, validBody, nil, 401)
		_, err = client.AccessKey.UpdateOneID(publisherAK.ID).SetStatus(accesskey.StatusOn).Save(sys)
		require.NoError(t, err)
		expiry := time.Now().Add(-time.Minute)
		_, err = client.AccessKey.UpdateOneID(publisherAK.ID).SetExpiresAt(expiry).Save(sys)
		require.NoError(t, err)
		request(t, akIdentity, "POST", path, validBody, nil, 401)
		_, err = client.AccessKey.UpdateOneID(publisherAK.ID).ClearExpiresAt().Save(sys)
		require.NoError(t, err)
		revokedAK, revokedSK := makeAK(publisher, "revoked")
		require.NoError(t, client.AccessKey.DeleteOneID(revokedAK.ID).Exec(sys))
		request(t, identity{key: revokedAK, secret: revokedSK}, "POST", path, validBody, nil, 401)
		_, err = client.RolePermission.Update().Where(rolepermission.RoleIDEQ(publisher.ID)).SetStatus(rolepermission.StatusOff).Save(sys)
		require.NoError(t, err)
		require.NoError(t, policy.ResetPolicies(ctx))
		request(t, akIdentity, "POST", path, validBody, nil, 403)
		request(t, jwtIdentity, "POST", path, validBody, nil, 403)
		_, err = client.RolePermission.Update().Where(rolepermission.RoleIDEQ(publisher.ID)).SetStatus(rolepermission.StatusOn).Save(sys)
		require.NoError(t, err)
		require.NoError(t, policy.ResetPolicies(ctx))
		require.NoError(t, client.PlanModule.DeleteOneID(module.ID).Exec(sys))
		request(t, akIdentity, "GET", base+"/vpc-cidr-presets", "", nil, 403)
		request(t, jwtIdentity, "GET", base+"/vpc-cidr-presets", "", nil, 403)
		request(t, akIdentity, "POST", path, validBody, nil, 403)
		request(t, jwtIdentity, "POST", path, validBody, nil, 403)
		require.Equal(t, before, beforeSideEffects(t))
	})
}
