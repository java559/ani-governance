//go:build modeldev_pg && modeldev_contract

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
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
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	authzMiddleware "go-wind-admin/pkg/localdeps/kratos-authz/middleware"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/encoding/protojson"
)

type modelDevFixtureTLS struct {
	CAFile   string `json:"ca_file"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

type modelDevMainFlowStartup struct {
	Schema           string             `json:"schema"`
	Address          string             `json:"address"`
	TLS              modelDevFixtureTLS `json:"tls"`
	ResourceTenantID string             `json:"resource_tenant_id"`
	Intent           cpup01.Intent      `json:"intent"`
	Release          struct {
		ID     string `json:"release_id"`
		Digest string `json:"release_digest"`
	} `json:"release"`
}

type modelDevQueryFixture struct {
	Schema           string             `json:"schema"`
	Address          string             `json:"address"`
	TLS              modelDevFixtureTLS `json:"tls"`
	ResourceTenantID string             `json:"resource_tenant_id"`
	ExecutionID      string             `json:"execution_id"`
	Artifact         struct {
		ID       string `json:"artifact_id"`
		Filename string `json:"filename"`
		Size     int64  `json:"size_bytes"`
		SHA256   string `json:"sha256"`
	} `json:"artifact"`
	StorageCAFile string `json:"storage_ca_file"`
}

func readModelDevQueryFixture(t *testing.T) modelDevQueryFixture {
	t.Helper()
	file := os.Getenv("ANI_MODELDEV_QUERY_HANDSHAKE")
	if !filepath.IsAbs(file) || filepath.Base(file) != "handshake.json" {
		t.Fatal("CPU09_QUERY_PREFLIGHT: explicit private handshake required")
	}
	info, err := os.Lstat(filepath.Dir(file))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("CPU09_QUERY_PREFLIGHT: private provider directory required")
	}
	if os.Getenv("ANI_MODELDEV_MAINFLOW_STARTUP") == "" {
		stopModelDevFixtureAfter(t, file)
	}
	info, err = os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		t.Fatal("CPU09_QUERY_PREFLIGHT: bounded private handshake required")
	}
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var fixture modelDevQueryFixture
	require.NoError(t, decoder.Decode(&fixture))
	require.Equal(t, io.EOF, decoder.Decode(new(any)))
	host, port, err := net.SplitHostPort(fixture.Address)
	n, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host != "127.0.0.1" || portErr != nil || n == 0 || fixture.Schema != "ani.cpu-p01.governance-query-fixture.v1" {
		t.Fatal("CPU09_QUERY_PREFLIGHT: invalid provider connection")
	}
	for _, path := range []string{fixture.TLS.CAFile, fixture.TLS.CertFile, fixture.TLS.KeyFile, fixture.StorageCAFile} {
		info, err := os.Lstat(path)
		if !filepath.IsAbs(path) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
			t.Fatal("CPU09_QUERY_PREFLIGHT: private TLS file references required")
		}
	}
	for _, id := range []string{fixture.ResourceTenantID, fixture.ExecutionID, fixture.Artifact.ID} {
		parsed, e := uuid.Parse(id)
		if e != nil || parsed == uuid.Nil || parsed.String() != id {
			t.Fatal("CPU09_QUERY_PREFLIGHT: invalid immutable identity")
		}
	}
	return fixture
}

func stopModelDevFixtureAfter(t *testing.T, file string) {
	t.Helper()
	t.Cleanup(func() {
		stop, err := os.OpenFile(filepath.Join(filepath.Dir(file), "stop"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		require.NoError(t, err)
		if err == nil {
			require.NoError(t, stop.Close())
		}
	})
}

func readModelDevMainFlowStartup(t *testing.T) *modelDevMainFlowStartup {
	t.Helper()
	file := os.Getenv("ANI_MODELDEV_MAINFLOW_STARTUP")
	if file == "" {
		return nil
	}
	if !filepath.IsAbs(file) || filepath.Base(file) != "startup.json" || filepath.Dir(file) != filepath.Dir(os.Getenv("ANI_MODELDEV_QUERY_HANDSHAKE")) {
		t.Fatal("CPU09_BFF_PREFLIGHT: explicit private startup required")
	}
	info, err := os.Lstat(filepath.Dir(file))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("CPU09_BFF_PREFLIGHT: private startup directory required")
	}
	stopModelDevFixtureAfter(t, file)
	info, err = os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		t.Fatal("CPU09_BFF_PREFLIGHT: bounded private startup required")
	}
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var startup modelDevMainFlowStartup
	require.NoError(t, decoder.Decode(&startup))
	require.Equal(t, io.EOF, decoder.Decode(new(any)))
	host, port, err := net.SplitHostPort(startup.Address)
	n, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host != "127.0.0.1" || portErr != nil || n == 0 || startup.Schema != "ani.cpu-p01.governance-main-flow-fixture.v1" {
		t.Fatal("CPU09_BFF_PREFLIGHT: invalid startup connection")
	}
	for _, name := range []string{startup.TLS.CAFile, startup.TLS.CertFile, startup.TLS.KeyFile} {
		info, err := os.Lstat(name)
		if !filepath.IsAbs(name) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
			t.Fatal("CPU09_BFF_PREFLIGHT: private TLS file references required")
		}
	}
	for _, value := range []string{startup.ResourceTenantID, startup.Release.ID} {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil || id.String() != value {
			t.Fatal("CPU09_BFF_PREFLIGHT: invalid startup identity")
		}
	}
	digest, err := hex.DecodeString(startup.Release.Digest)
	if err != nil || len(digest) != 32 || startup.Release.Digest != strings.ToLower(startup.Release.Digest) {
		t.Fatal("CPU09_BFF_PREFLIGHT: invalid Release digest")
	}
	_, _, err = cpup01.CanonicalIntent(startup.Intent)
	require.NoError(t, err, "CPU09_BFF_PREFLIGHT: registered intent required")
	return &startup
}

// This test calls the real ModelDev query service on its real PG publication.
// Only the KFP/Kubernetes/S3 boundaries in the provider remain explicit fixtures.
func TestModelDevQueryCurrentAuthorizationDownloadsPublishedArtifacts(t *testing.T) {
	testModelDevBFFMainFlow(t, false, false, false)
}

func TestModelDevStopDuringActualTrainingAndObserveClosed(t *testing.T) {
	testModelDevBFFMainFlow(t, true, false, false)
}

func TestModelDevListAndRealTrainingLogsThroughBFF(t *testing.T) {
	testModelDevBFFMainFlow(t, false, true, false)
}

func TestModelDevStopBeforeDeliveryClosesWithoutTraining(t *testing.T) {
	testModelDevBFFMainFlow(t, true, false, true)
}

func testModelDevBFFMainFlow(t *testing.T, stopping, listLogs, stopBefore bool) {
	startup := readModelDevMainFlowStartup(t)
	if stopping {
		require.NotNil(t, startup, "CPU10_STOP_PREFLIGHT: actual create provider required")
	}
	var provider modelDevQueryFixture
	if startup == nil {
		provider = readModelDevQueryFixture(t)
	} else {
		provider.Address, provider.TLS, provider.ResourceTenantID = startup.Address, startup.TLS, startup.ResourceTenantID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
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
	suffix := uuid.NewString()
	plan, err := writer.Client().Plan.Create().SetName("cpu-query-" + suffix).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Plan.DeleteOneID(plan.ID).Exec(c) })
	module, err := writer.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleModel).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().PlanModule.DeleteOneID(module.ID).Exec(c) })
	type apiRoute struct{ path, method string }
	routes := []apiRoute{{data.ModelDevGetExecutionPath, "GET"}, {data.ModelDevListArtifactsPath, "GET"}, {data.ModelDevDownloadArtifactPath, "GET"}}
	if startup != nil {
		routes = append(routes, apiRoute{"/api/v1/modeldev/executions", "POST"})
	}
	if stopping {
		routes = append(routes, apiRoute{data.ModelDevStopExecutionPath, "POST"})
	}
	if listLogs {
		routes = append(routes, apiRoute{data.ModelDevListExecutionsPath, "GET"}, apiRoute{data.ModelDevGetExecutionLogsPath, "GET"})
	}
	var apiIDs []uint32
	queryAPI := map[string]uint32{}
	for _, route := range routes {
		row, e := writer.Client().Api.Create().SetModule("ModelDevService").SetScope(api.ScopeAdmin).SetPath(route.path).SetMethod(route.method).SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(sys)
		require.NoError(t, e)
		apiIDs = append(apiIDs, row.ID)
		if route.method == "GET" {
			queryAPI[route.path] = row.ID
		}
		cleanup(func(c context.Context) error { return writer.Client().Api.DeleteOneID(row.ID).Exec(c) })
	}
	type identity struct {
		tenant, user, role, permission uint32
		roleCode                       string
	}
	makeIdentity := func(resourceTenant string) identity {
		id := uuid.NewString()
		owner, e := writer.Client().Tenant.Create().SetName("query fixture " + id).SetCode("cpu-query-" + id).SetResourceTenantID(resourceTenant).SetStatus(tenant.StatusOn).SetPlanID(plan.ID).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().Tenant.DeleteOneID(owner.ID).Exec(c) })
		operator, e := writer.Client().User.Create().SetTenantID(owner.ID).SetUsername("cpu-query-" + id).SetStatus(user.StatusNormal).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().User.DeleteOneID(operator.ID).Exec(c) })
		currentRole, e := writer.Client().Role.Create().SetTenantID(owner.ID).SetName("query fixture " + id).SetCode("tenant:cpu-query:" + id).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().Role.DeleteOneID(currentRole.ID).Exec(c) })
		membership, e := writer.Client().UserRole.Create().SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).SetStatus(userrole.StatusActive).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error {
			_, e := writer.Client().UserRole.Delete().Where(userrole.IDEQ(membership.ID)).Exec(c)
			return e
		})
		grantPermission, e := writer.Client().Permission.Create().SetName("query fixture " + id).SetCode("cpu.query." + id).SetStatus(permission.StatusOn).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error {
			return writer.Client().Permission.DeleteOneID(grantPermission.ID).Exec(c)
		})
		for _, apiID := range apiIDs {
			link, e := writer.Client().PermissionApi.Create().SetPermissionID(grantPermission.ID).SetAPIID(apiID).Save(sys)
			require.NoError(t, e)
			cleanup(func(c context.Context) error { return writer.Client().PermissionApi.DeleteOneID(link.ID).Exec(c) })
		}
		grant, e := writer.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(grantPermission.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().RolePermission.DeleteOneID(grant.ID).Exec(c) })
		return identity{owner.ID, operator.ID, currentRole.ID, grantPermission.ID, *currentRole.Code}
	}
	owner, other := makeIdentity(provider.ResourceTenantID), makeIdentity(uuid.NewString())
	unauthorized, err := writer.Client().User.Create().SetTenantID(owner.tenant).SetUsername("cpu-query-denied-" + suffix).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().User.DeleteOneID(unauthorized.ID).Exec(c) })
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	for _, name := range []string{"GWA_AUTH_JWT_PRIVATE_KEY", "GWA_AUTH_JWT_PUBLIC_KEY", "GWA_AUTH_JWT_KEY"} {
		t.Setenv(name, "")
	}
	bctx := testutil.NewBootstrapContext(&conf.Bootstrap{Authz: &conf.Authorization{Type: "casbin"}, Authn: &conf.Authentication{Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(key)}}})
	address := os.Getenv("ANI_MODELDEV_REDIS_ADDR")
	host, port, err := net.SplitHostPort(address)
	n, e := strconv.ParseUint(port, 10, 16)
	if err != nil || host != "127.0.0.1" || e != nil || n == 0 {
		t.Fatal("CPU09_QUERY_PREFLIGHT: task loopback Redis required")
	}
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
	issue := func(who identity) string {
		payload := &authv1.UserTokenPayload{UserId: who.user, TenantId: trans.Ptr(who.tenant), Roles: []string{who.roleCode}, DataScopes: []identityv1.DataScope{identityv1.DataScope_ALL}}
		token, _, e := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, payload)
		require.NoError(t, e)
		cleanup(func(c context.Context) error {
			return cache.RevokeTokenByJti(c, authv1.ClientType_admin, who.user, payload.GetJti())
		})
		return token
	}
	token, otherToken := issue(owner), issue(other)
	denied := owner
	denied.user = unauthorized.ID
	deniedToken := issue(denied) // stale/forged role claim cannot replace current DB membership.
	resolver, closeResolver, err := data.NewModelDevClient(data.ModelDevClientConfig{Address: provider.Address, CAFile: provider.TLS.CAFile, CertFile: provider.TLS.CertFile, KeyFile: provider.TLS.KeyFile, Timeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(closeResolver)
	acceptances, bindings := data.NewModelDevAcceptanceRepo(runtime), data.NewModelDevReleaseBindingRepo(runtime)
	bff := service.NewModelDevService(data.NewModelDevAuthorizationRepo(runtime), data.NewTenantRepo(bctx, runtime), acceptances, bindings, resolver)
	server := khttp.NewServer(khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(tenantChecker), auth.WithInjectMetadata(false), auth.WithInjectEnt(true)), authzMiddleware.Server(policy.Engine())))
	registerModelDevHTTP(server, bff)
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	if startup != nil {
		valid, verified := checker.IsValidAccessToken(ctx, token, false)
		require.True(t, valid)
		requestCtx := viewer.WithContext(auth.NewContext(ctx, verified), appViewer.NewUserViewer(uint64(owner.user), uint64(owner.tenant), 0, "", appViewer.BuildDataScopes(verified.GetDataScopes(), verified.GetDataScopeUnitIds(), verified.GetDataScope())))
		principal, e := auth.PrincipalFromContext(requestCtx)
		require.NoError(t, e)
		actor, e := principal.Actor()
		require.NoError(t, e)
		cleanup(func(c context.Context) error {
			_, e := writer.Client().ModelDevReleaseBinding.Delete().Where(modeldevreleasebinding.TenantIDEQ(owner.tenant)).Exec(c)
			return e
		})
		cleanup(func(c context.Context) error {
			_, e := writer.Client().ModelDevAcceptance.Delete().Where(modeldevacceptance.TenantIDEQ(owner.tenant)).Exec(c)
			return e
		})
		binding, e := bindings.CompareAndSwap(requestCtx, data.ModelDevReleaseBindingScope{TenantID: owner.tenant, ResourceTenantID: provider.ResourceTenantID, PresetID: startup.Intent.PresetID}, data.ModelDevReleaseBindingUpdate{
			Target: data.ModelDevReleaseBindingTarget{ReleaseID: startup.Release.ID, ReleaseDigest: startup.Release.Digest, NewSubmissionsEnabled: true},
			Actor:  actor, RequestedAt: time.Now().UTC().Truncate(time.Microsecond), Reason: "owned BFF main-flow fixture", EvidenceReference: "contract:cpu-p01:bff-main-flow",
		})
		require.NoError(t, e)
		require.Equal(t, uint64(1), binding.After.Generation)
		key := uuid.NewString()
		body, e := json.Marshal(struct {
			cpup01.Intent
			IdempotencyKey string `json:"idempotency_key"`
		}{startup.Intent, key})
		require.NoError(t, e)
		request, e := http.NewRequestWithContext(ctx, "POST", web.URL+"/api/v1/modeldev/executions", bytes.NewReader(body))
		require.NoError(t, e)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response, e := web.Client().Do(request)
		require.NoError(t, e)
		replyBody, e := io.ReadAll(io.LimitReader(response.Body, 16385))
		require.NoError(t, response.Body.Close())
		require.NoError(t, e)
		require.Equal(t, http.StatusAccepted, response.StatusCode, "CPU09_BFF_CREATE_NOT_IMPLEMENTED: real authorized POST did not durably accept; safe response=%s", replyBody)
		var accepted modeldevv1.CreateExecutionResponse
		require.NoError(t, protojson.Unmarshal(replyBody, &accepted))
		stored, e := acceptances.FindAccepted(requestCtx, data.ModelDevAdmissionScope{TenantID: owner.tenant, ResourceTenantID: provider.ResourceTenantID, Actor: actor, Action: data.ModelDevCreateAction, IdempotencyKey: key}, startup.Intent)
		require.NoError(t, e)
		require.Equal(t, stored.ExecutionID, accepted.ExecutionId)
		require.Equal(t, stored.OperationID, accepted.OperationId)
		worker := service.NewModelDevDispatchWorker(bctx, acceptances, resolver)
		startWorker := func() {
			require.NoError(t, worker.Start(ctx))
			cleanup(worker.Stop)
		}
		if !stopBefore {
			startWorker()
		}
		created, e := json.Marshal(map[string]string{"execution_id": accepted.ExecutionId, "operation_id": accepted.OperationId})
		require.NoError(t, e)
		createdPath := filepath.Join(filepath.Dir(os.Getenv("ANI_MODELDEV_MAINFLOW_STARTUP")), "created.json")
		require.NoFileExists(t, createdPath)
		file, e := os.OpenFile(createdPath+".pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		require.NoError(t, e)
		_, e = file.Write(created)
		require.NoError(t, e)
		require.NoError(t, file.Close())
		require.NoError(t, os.Rename(createdPath+".pending", createdPath))
		t.Log("BFF_MAIN_FLOW_CREATED: real authenticated HTTP202 committed before actual delivery worker")
		if stopping {
			var startAfterStop func()
			if stopBefore {
				startAfterStop = startWorker
			}
			verifyModelDevBFFStopMainFlow(t, ctx, web, token, otherToken, deniedToken, accepted.OperationId, accepted.ExecutionId, owner.tenant, startAfterStop)
			return
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, e = os.Stat(os.Getenv("ANI_MODELDEV_QUERY_HANDSHAKE")); e == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("BFF main flow did not publish before timeout")
			case <-ticker.C:
			}
		}
		provider = readModelDevQueryFixture(t)
		require.Equal(t, startup.ResourceTenantID, provider.ResourceTenantID)
		require.Equal(t, accepted.ExecutionId, provider.ExecutionID)
		require.Equal(t, startup.Address, provider.Address)
		stored, e = acceptances.FindAccepted(requestCtx, data.ModelDevAdmissionScope{TenantID: owner.tenant, ResourceTenantID: provider.ResourceTenantID, Actor: actor, Action: data.ModelDevCreateAction, IdempotencyKey: key}, startup.Intent)
		require.NoError(t, e)
		require.Equal(t, "ACKED", stored.DispatchState)
		require.NotNil(t, stored.OwnerReceipt)
	}
	get := func(path, accessToken string) (int, []byte) {
		t.Helper()
		request, e := http.NewRequestWithContext(ctx, "GET", web.URL+path, nil)
		require.NoError(t, e)
		if accessToken != "" {
			request.Header.Set("Authorization", "Bearer "+accessToken)
		}
		request.Header.Set("x-ani-tenant-id", provider.ResourceTenantID)
		request.Header.Set("x-ani-actor", "governance:user:42")
		request.Header.Set("x-ani-authorized-method", "forged")
		request.Header.Set("x-ani-data-scope", "tenant-all")
		response, e := web.Client().Do(request)
		require.NoError(t, e)
		defer response.Body.Close()
		body, e := io.ReadAll(io.LimitReader(response.Body, 65537))
		require.NoError(t, e)
		require.LessOrEqual(t, len(body), 65536)
		require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		return response.StatusCode, body
	}
	detailPath := "/api/v1/modeldev/executions/" + provider.ExecutionID
	artifactsPath := detailPath + "/artifacts"
	downloadPath := "/api/v1/modeldev/artifacts/" + provider.Artifact.ID + "/content"
	logsPath := detailPath + "/logs"
	if listLogs {
		for _, path := range []string{data.ModelDevListExecutionsPath, logsPath} {
			code, _ := get(path, "")
			require.Equal(t, http.StatusUnauthorized, code)
			code, _ = get(path, deniedToken)
			require.Equal(t, http.StatusForbidden, code)
		}
		code, body := get(data.ModelDevListExecutionsPath+"?pageSize=1", token)
		require.Equal(t, http.StatusOK, code, "CPU09_BFF_LIST_NOT_IMPLEMENTED")
		var executions modeldevv1.ListExecutionsResponse
		require.NoError(t, protojson.Unmarshal(body, &executions))
		require.Len(t, executions.Items, 1)
		require.Equal(t, provider.ExecutionID, executions.Items[0].ExecutionId)
		require.Equal(t, "PUBLISHED", executions.Items[0].DeliveryState)
		// page/pageSize is the only supported window; legacy limit/cursor is rejected.
		code, _ = get(data.ModelDevListExecutionsPath+"?page=0", token)
		require.Equal(t, http.StatusBadRequest, code)
		code, _ = get(data.ModelDevListExecutionsPath+"?limit=1", token)
		require.Equal(t, http.StatusBadRequest, code)
		code, body = get(data.ModelDevListExecutionsPath, otherToken)
		require.Equal(t, http.StatusOK, code)
		executions.Reset()
		require.NoError(t, protojson.Unmarshal(body, &executions))
		require.Empty(t, executions.Items)
		code, _ = get(logsPath, otherToken)
		require.Equal(t, http.StatusNotFound, code)
		code, body = get(logsPath, token)
		require.Equal(t, http.StatusOK, code, "CPU09_BFF_LOGS_NOT_IMPLEMENTED")
		var logs modeldevv1.GetExecutionLogsResponse
		require.NoError(t, protojson.Unmarshal(body, &logs))
		_, err = uuid.Parse(logs.LogId)
		require.NoError(t, err)
		require.NotEmpty(t, logs.Lines)
		require.LessOrEqual(t, len(logs.Lines), 200)
		_, err = time.Parse(time.RFC3339Nano, logs.ObservedAt)
		require.NoError(t, err)
		foundLoss, bytes := false, 0
		for _, line := range logs.Lines {
			_, err = time.Parse(time.RFC3339Nano, line.Timestamp)
			require.NoError(t, err)
			bytes += len(line.Text)
			if strings.Contains(line.Text, `"schema": "ani.metric.v1"`) && strings.Contains(line.Text, `"name": "train.loss"`) && strings.Contains(line.Text, `"step": 48`) {
				foundLoss = true
			}
		}
		require.True(t, foundLoss, "CPU09_REAL_TRAINING_OUTPUT_NOT_OBSERVED")
		require.LessOrEqual(t, bytes, 16384)
		require.NotContains(t, string(body), "resource_uid")
		require.NotContains(t, string(body), "container_name")
		// Save the actual BFF response for user-visible evidence, not private selectors.
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(os.Getenv("ANI_MODELDEV_QUERY_OUTPUT_DIR")), "bff-training-logs.json"), body, 0600))
		code, body = get(logsPath+"?tail_lines=1&max_bytes=512", token)
		require.Equal(t, http.StatusOK, code)
		logs.Reset()
		require.NoError(t, protojson.Unmarshal(body, &logs))
		require.LessOrEqual(t, len(logs.Lines), 1)
		for _, line := range logs.Lines {
			require.LessOrEqual(t, len(line.Text), 512)
		}
		for path, route := range map[string]string{data.ModelDevListExecutionsPath: data.ModelDevListExecutionsPath, logsPath: data.ModelDevGetExecutionLogsPath} {
			require.NoError(t, writer.Client().Api.UpdateOneID(queryAPI[route]).SetStatus(api.StatusOff).Exec(sys))
			code, _ = get(path, token)
			require.Equal(t, http.StatusForbidden, code)
			code, _ = get(detailPath, token)
			require.Equal(t, http.StatusOK, code)
			require.NoError(t, writer.Client().Api.UpdateOneID(queryAPI[route]).SetStatus(api.StatusOn).Exec(sys))
		}
		t.Log("same BFF-created execution listed under trusted tenant; actual train.loss step 48 retrieved through current-authorized BFF logs")
	}
	for _, path := range []string{detailPath, artifactsPath, downloadPath} {
		code, body := get(path, "")
		require.Equal(t, http.StatusUnauthorized, code)
		require.NotContains(t, string(body), provider.Artifact.SHA256)
		code, body = get(path, deniedToken)
		require.Equal(t, http.StatusForbidden, code)
		require.NotContains(t, string(body), provider.Artifact.SHA256)
		code, body = get(path, otherToken)
		require.Equal(t, http.StatusNotFound, code)
		require.NotContains(t, string(body), provider.Artifact.SHA256)
	}
	code, body := get(detailPath, token)
	require.Equal(t, http.StatusOK, code)
	var detail modeldevv1.ExecutionView
	require.NoError(t, protojson.Unmarshal(body, &detail))
	require.Equal(t, provider.ExecutionID, detail.GetExecutionId())
	require.Equal(t, "PUBLISHED", detail.GetDeliveryState())
	require.Equal(t, "CLOSED", detail.GetCloseState())
	require.NotContains(t, string(body), "download_url")
	require.NotContains(t, string(body), "bucket")
	require.NotContains(t, string(body), "token")
	code, body = get(artifactsPath, token)
	require.Equal(t, http.StatusOK, code)
	var listed modeldevv1.ListExecutionArtifactsResponse
	require.NoError(t, protojson.Unmarshal(body, &listed))
	require.GreaterOrEqual(t, len(listed.Items), 2)
	roots := x509.NewCertPool()
	ca, err := os.ReadFile(provider.StorageCAFile)
	require.NoError(t, err)
	require.True(t, roots.AppendCertsFromPEM(ca))
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	t.Cleanup(transport.CloseIdleConnections)
	storage := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	output := os.Getenv("ANI_MODELDEV_QUERY_OUTPUT_DIR")
	if !filepath.IsAbs(output) {
		t.Fatal("CPU09_QUERY_PREFLIGHT: independent download directory required")
	}
	require.NoError(t, os.Mkdir(output, 0700))
	found := map[string]bool{}
	for _, artifact := range listed.Items {
		code, body = get("/api/v1/modeldev/artifacts/"+artifact.ArtifactId+"/content", token)
		require.Equal(t, http.StatusOK, code)
		var grant modeldevv1.AuthorizeArtifactDownloadResponse
		err = protojson.Unmarshal(body, &grant)
		if err != nil {
			t.Fatal("invalid download grant response")
		}
		require.Equal(t, artifact.ArtifactId, grant.GetArtifact().GetArtifactId())
		response, e := storage.Get(grant.DownloadUrl)
		if e != nil {
			t.Fatal("authorized HTTPS object retrieval failed")
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			t.Fatal("authorized object retrieval did not succeed")
		}
		raw, e := io.ReadAll(io.LimitReader(response.Body, artifact.SizeBytes+1))
		require.NoError(t, response.Body.Close())
		if e != nil {
			t.Fatal("authorized object body read failed")
		}
		require.Equal(t, artifact.SizeBytes, int64(len(raw)))
		sum := sha256.Sum256(raw)
		require.Equal(t, artifact.Sha256, hex.EncodeToString(sum[:]))
		require.NoError(t, os.WriteFile(filepath.Join(output, artifact.Filename), raw, 0600))
		found[artifact.Filename] = true
		if artifact.ArtifactId == provider.Artifact.ID {
			require.Equal(t, provider.Artifact.SHA256, artifact.Sha256)
			require.Equal(t, provider.Artifact.Size, artifact.SizeBytes)
			require.Equal(t, provider.Artifact.Filename, artifact.Filename)
		}
	}
	require.True(t, found["model.pt"])
	require.True(t, found["model_config.json"])
	// Removing only the content API grant cannot be replaced by a read grant.
	require.NoError(t, writer.Client().Api.UpdateOneID(apiIDs[2]).SetStatus(api.StatusOff).Exec(sys))
	code, body = get(downloadPath, token)
	require.Equal(t, http.StatusForbidden, code)
	require.NotContains(t, string(body), "download_url")
	code, _ = get(detailPath, token)
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, writer.Client().Api.UpdateOneID(apiIDs[2]).SetStatus(api.StatusOn).Exec(sys))
	// Current grant revocation wins over the still-valid JWT and cached Casbin policy.
	require.NoError(t, writer.Client().Permission.UpdateOneID(owner.permission).SetStatus(permission.StatusOff).Exec(sys))
	revokedPaths := []string{detailPath, artifactsPath, downloadPath}
	if listLogs {
		revokedPaths = append(revokedPaths, data.ModelDevListExecutionsPath, logsPath)
	}
	for _, path := range revokedPaths {
		code, body = get(path, token)
		require.Equal(t, http.StatusForbidden, code)
		require.NotContains(t, string(body), "download_url")
	}
	require.NoError(t, writer.Client().Permission.UpdateOneID(owner.permission).SetStatus(permission.StatusOn).Exec(sys))
	require.NoError(t, writer.Client().Role.UpdateOneID(owner.role).SetDataScope(role.DataScopeSelf).Exec(sys))
	code, body = get(downloadPath, token)
	require.Equal(t, http.StatusForbidden, code)
	require.NotContains(t, string(body), "download_url")
	if listLogs {
		for _, path := range []string{data.ModelDevListExecutionsPath, logsPath} {
			code, _ = get(path, token)
			require.Equal(t, http.StatusForbidden, code)
		}
	}
	t.Log("real JWT/Redis, Casbin, current PG grants, tenant-isolated ModelDev mTLS query and authorized HTTPS downloads passed; output ready for independent CPU forward")
}
