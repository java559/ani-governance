//go:build modeldev_pg && modeldev_contract

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevreleasebinding"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/app/admin/service/tests/testutil"
	"go-wind-admin/pkg/authorizer"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	authzMiddleware "go-wind-admin/pkg/localdeps/kratos-authz/middleware"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/middleware/auth"
)

// The admitted input is a normative contract fixture, not a remote ModelDev
// resolver. Stop must remain durably available while that owner is unavailable.
func TestModelDevStopPersistsBefore202AndReplaysOriginal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	t.Cleanup(cancel)
	sys := appViewer.NewSystemViewerContext(ctx)
	runtime, writer := prepareModelDevHTTPDatabase(t, ctx)
	cleanup := func(remove func(context.Context) error) {
		t.Cleanup(func() {
			c, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
			defer stop()
			require.NoError(t, remove(c))
		})
	}
	id := uuid.NewString()
	plan, err := writer.Client().Plan.Create().SetName("stop " + id).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Plan.DeleteOneID(plan.ID).Exec(c) })
	module, err := writer.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleModel).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().PlanModule.DeleteOneID(module.ID).Exec(c) })
	owner, err := writer.Client().Tenant.Create().SetName("stop " + id).SetCode("stop-" + id).SetResourceTenantID(uuid.NewString()).SetStatus(tenant.StatusOn).SetPlanID(plan.ID).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Tenant.DeleteOneID(owner.ID).Exec(c) })
	operator, err := writer.Client().User.Create().SetTenantID(owner.ID).SetUsername("stop-" + id).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().User.DeleteOneID(operator.ID).Exec(c) })
	currentRole, err := writer.Client().Role.Create().SetTenantID(owner.ID).SetName("stop " + id).SetCode("tenant:stop:" + id).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Role.DeleteOneID(currentRole.ID).Exec(c) })
	membership, err := writer.Client().UserRole.Create().SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).SetStatus(userrole.StatusActive).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error {
		_, e := writer.Client().UserRole.Delete().Where(userrole.IDEQ(membership.ID)).Exec(c)
		return e
	})
	perm, err := writer.Client().Permission.Create().SetName("stop " + id).SetCode("stop." + id).SetStatus(permission.StatusOn).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Permission.DeleteOneID(perm.ID).Exec(c) })
	const route = "/api/v1/modeldev/executions/{execution_id}:stop"
	endpoint, err := writer.Client().Api.Create().SetModule("ModelDevService").SetScope(api.ScopeAdmin).SetPath(route).SetMethod("POST").SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Api.DeleteOneID(endpoint.ID).Exec(c) })
	link, err := writer.Client().PermissionApi.Create().SetPermissionID(perm.ID).SetAPIID(endpoint.ID).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().PermissionApi.DeleteOneID(link.ID).Exec(c) })
	grant, err := writer.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(perm.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().RolePermission.DeleteOneID(grant.ID).Exec(c) })
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	for _, name := range []string{"GWA_AUTH_JWT_PRIVATE_KEY", "GWA_AUTH_JWT_PUBLIC_KEY", "GWA_AUTH_JWT_KEY"} {
		t.Setenv(name, "")
	}
	bctx := testutil.NewBootstrapContext(&conf.Bootstrap{Authz: &conf.Authorization{Type: "casbin"}, Authn: &conf.Authentication{Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(key)}}})
	address := os.Getenv("ANI_MODELDEV_REDIS_ADDR")
	host, port, addressErr := net.SplitHostPort(address)
	n, portErr := strconv.ParseUint(port, 10, 16)
	require.True(t, addressErr == nil && host == "127.0.0.1" && portErr == nil && n > 0, "CPU10_STOP_PREFLIGHT: task Redis required")
	rdb := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	require.NoError(t, rdb.Ping(ctx).Err())
	cache := data.NewUserTokenCache(bctx, rdb)
	authenticator := data.NewAuthenticator(bctx, cache)
	checker := data.NewTokenChecker(bctx, authenticator, authv1.ClientType_admin)
	tenantChecker := data.NewTenantAccessCheckerImpl(bctx, runtime)
	permissions := data.NewPermissionRepo(bctx, runtime, data.NewPermissionApiRepo(bctx, runtime), data.NewPermissionMenuRepo(bctx, runtime))
	roles := data.NewRoleRepo(bctx, runtime, data.NewRolePermissionRepo(bctx, runtime), data.NewRoleOrgUnitRepo(bctx, runtime), permissions, data.NewRoleMetadataRepo(bctx, runtime), data.NewRoleFieldPermissionRepo(bctx, runtime))
	policy := authorizer.NewAuthorizer(bctx, data.NewAuthorizerProvider(bctx, roles, data.NewApiRepo(bctx, runtime)))
	require.Equal(t, "casbin", policy.Engine().Name())
	require.NoError(t, policy.ResetPolicies(ctx))
	payload := &authv1.UserTokenPayload{UserId: operator.ID, TenantId: trans.Ptr(owner.ID), Roles: []string{*currentRole.Code}, DataScopes: []identityv1.DataScope{identityv1.DataScope_ALL}}
	token, _, err := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, payload)
	require.NoError(t, err)
	cleanup(func(c context.Context) error {
		return cache.RevokeTokenByJti(c, authv1.ClientType_admin, operator.ID, payload.GetJti())
	})
	acceptances, bindings := data.NewModelDevAcceptanceRepo(runtime), data.NewModelDevReleaseBindingRepo(runtime)
	cleanup(func(c context.Context) error {
		_, e := writer.Client().ModelDevReleaseBinding.Delete().Where(modeldevreleasebinding.TenantIDEQ(owner.ID)).Exec(c)
		return e
	})
	cleanup(func(c context.Context) error {
		_, e := writer.Client().ModelDevAcceptance.Delete().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Exec(c)
		return e
	})
	snapshot, intent := conformance.SnapshotV1(), conformance.IntentV1()
	snapshot.Release.AcceptedBindingGeneration = 1
	intent.PresetID, intent.DatasetVersionID = snapshot.Release.PresetID, snapshot.Input.InputVersionID
	_, err = bindings.CompareAndSwap(sys, data.ModelDevReleaseBindingScope{TenantID: owner.ID, ResourceTenantID: owner.ResourceTenantID, PresetID: intent.PresetID}, data.ModelDevReleaseBindingUpdate{Target: data.ModelDevReleaseBindingTarget{ReleaseID: snapshot.Release.ReleaseID, ReleaseDigest: snapshot.Release.ReleaseDigest, NewSubmissionsEnabled: true}, Actor: "governance:user:7", RequestedAt: time.Now().UTC().Truncate(time.Microsecond), Reason: "owned Stop fixture", EvidenceReference: "contract:cpu-p01:stop"})
	require.NoError(t, err)
	accepted, replayed, err := acceptances.AcceptFrozen(sys, data.ModelDevAdmissionScope{TenantID: owner.ID, ResourceTenantID: owner.ResourceTenantID, Actor: "governance:user:7", Action: data.ModelDevCreateAction, IdempotencyKey: uuid.NewString()}, data.ModelDevFrozenCandidate{OperationID: uuid.NewString(), ExecutionID: uuid.NewString(), Intent: intent, Snapshot: snapshot, AcceptedAt: snapshot.DeadlineAt.Add(-time.Hour)})
	require.NoError(t, err)
	require.False(t, replayed)
	start := func() *httptest.Server {
		bff := service.NewModelDevService(data.NewModelDevAuthorizationRepo(runtime), data.NewTenantRepo(bctx, runtime), data.NewModelDevAcceptanceRepo(runtime), bindings, nil)
		server := khttp.NewServer(khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(tenantChecker), auth.WithInjectMetadata(false), auth.WithInjectEnt(true)), authzMiddleware.Server(policy.Engine())))
		registerModelDevHTTP(server, bff)
		web := httptest.NewServer(server)
		t.Cleanup(web.Close)
		return web
	}
	web := start()
	post := func(id, body, accessToken string) (int, map[string]any) {
		req, e := http.NewRequestWithContext(ctx, "POST", web.URL+"/api/v1/modeldev/executions/"+id+":stop", bytes.NewBufferString(body))
		require.NoError(t, e)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("x-ani-tenant-id", uuid.NewString())
		req.Header.Set("x-ani-actor", "governance:user:7")
		resp, e := web.Client().Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 16385))
		require.NoError(t, e)
		var out map[string]any
		if resp.StatusCode != http.StatusNotFound {
			require.NoError(t, json.Unmarshal(raw, &out))
		}
		return resp.StatusCode, out
	}
	before := time.Now().UTC()
	status, first := post(accepted.ExecutionID, "", token)
	require.Equal(t, http.StatusAccepted, status, "CPU10_BFF_STOP_NOT_IMPLEMENTED: actual JWT/Casbin/PG accepted execution must accept Stop")
	require.Equal(t, map[string]any{"operation_id": accepted.OperationID, "execution_id": accepted.ExecutionID, "stop_requested": true, "intent_generation": "1", "replayed": false}, first)
	observer := openModelDevHTTPPG(t, os.Getenv("ANI_TEST_DATABASE_DSN"))
	var actor, state string
	var generation int64
	var requestedAt time.Time
	require.NoError(t, observer.DB().QueryRowContext(sys, `SELECT stop_requested_actor,stop_requested_at,stop_intent_generation,close_dispatch_state FROM sys_modeldev_acceptances WHERE tenant_id=$1 AND execution_id=$2`, owner.ID, accepted.ExecutionID).Scan(&actor, &requestedAt, &generation, &state))
	require.Equal(t, "governance:user:"+strconv.FormatUint(uint64(operator.ID), 10), actor)
	require.False(t, requestedAt.Before(before))
	require.Equal(t, int64(1), generation)
	require.Equal(t, "QUEUED", state, "202 must not require a reachable ModelDev or pretend CLOSED")
	web.Close()
	web = start() // New BFF/repository instances must replay the committed intent.
	status, repeat := post(accepted.ExecutionID, "", token)
	require.Equal(t, http.StatusAccepted, status)
	first["replayed"] = true
	require.Equal(t, first, repeat)
	status, _ = post(accepted.ExecutionID, `{"generation":2,"actor":"injected"}`, token)
	require.Equal(t, http.StatusBadRequest, status)
	status, _ = post(uuid.NewString(), "", token)
	require.Equal(t, http.StatusNotFound, status)
	require.NoError(t, writer.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusDisabled).Exec(sys))
	status, _ = post(accepted.ExecutionID, "", token)
	require.Equal(t, http.StatusForbidden, status, "current authorization is checked even on Stop replay")
	t.Log("CPU10_BFF_STOP_ACCEPTED: current auth, durable queued intent, reconstructed BFF replay; no owner closure claim")
}
