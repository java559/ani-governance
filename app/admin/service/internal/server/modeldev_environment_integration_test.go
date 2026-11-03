//go:build modeldev_pg && modeldev_registration_pg

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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	ownerpb "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Only the external owner transport (and the existing Resource selection
// boundary fixture) is substituted. JWT/session, Casbin, current DB grants,
// tenant mapping, BFF service/client and persistence are production code.
func TestModelDevEnvironmentHTTPCurrentAuthorizationAndNoAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	sys := appViewer.NewSystemViewerContext(ctx)
	runtime, writer := prepareRegistrationHTTPDatabase(t, ctx)
	cleanup := func(remove func(context.Context) error) {
		t.Cleanup(func() {
			c, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
			defer stop()
			require.NoError(t, remove(c))
		})
	}
	id := uuid.NewString()
	plan, err := writer.Client().Plan.Create().SetName("environment-http-" + id).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Plan.DeleteOneID(plan.ID).Exec(c) })
	module, err := writer.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleModel).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().PlanModule.DeleteOneID(module.ID).Exec(c) })
	owner, err := writer.Client().Tenant.Create().SetName("environment " + id).SetCode("env-http-" + id).SetResourceTenantID(uuid.NewString()).SetStatus(tenant.StatusOn).SetPlanID(plan.ID).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Tenant.DeleteOneID(owner.ID).Exec(c) })
	operator, err := writer.Client().User.Create().SetTenantID(owner.ID).SetUsername("env-http-" + id).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().User.DeleteOneID(operator.ID).Exec(c) })
	currentRole, err := writer.Client().Role.Create().SetTenantID(owner.ID).SetName("environment " + id).SetCode("tenant:env-http:" + id).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Role.DeleteOneID(currentRole.ID).Exec(c) })
	membership, err := writer.Client().UserRole.Create().SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).SetStatus(userrole.StatusActive).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().UserRole.DeleteOneID(membership.ID).Exec(c) })
	var manageGrant uint32
	for _, route := range []struct{ method, path, code string }{
		{"POST", data.ModelDevEnsureEnvironmentPath, data.ModelDevManageEnvironmentPermissionCode},
		{"GET", data.ModelDevGetEnvironmentPath, data.ModelDevViewEnvironmentPermissionCode},
		{"POST", "/api/v1/modeldev/executions", "environment.http.create." + id},
	} {
		p, e := writer.Client().Permission.Create().SetName(route.code).SetCode(route.code).SetStatus(permission.StatusOn).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().Permission.DeleteOneID(p.ID).Exec(c) })
		a, e := writer.Client().Api.Create().SetModule("ModelDevEnvironment").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleModel).SetMethod(route.method).SetPath(route.path).SetStatus(api.StatusOn).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().Api.DeleteOneID(a.ID).Exec(c) })
		link, e := writer.Client().PermissionApi.Create().SetPermissionID(p.ID).SetAPIID(a.ID).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().PermissionApi.DeleteOneID(link.ID).Exec(c) })
		grant, e := writer.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(p.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().RolePermission.DeleteOneID(grant.ID).Exec(c) })
		if route.code == data.ModelDevManageEnvironmentPermissionCode {
			manageGrant = grant.ID
		}
	}
	keyBytes := make([]byte, 32)
	_, err = rand.Read(keyBytes)
	require.NoError(t, err)
	for _, name := range []string{"GWA_AUTH_JWT_PRIVATE_KEY", "GWA_AUTH_JWT_PUBLIC_KEY", "GWA_AUTH_JWT_KEY"} {
		t.Setenv(name, "")
	}
	bctx := testutil.NewBootstrapContext(&conf.Bootstrap{Authz: &conf.Authorization{Type: "casbin"}, Authn: &conf.Authentication{Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(keyBytes)}}})
	address := os.Getenv("ANI_MODELDEV_REDIS_ADDR")
	host, port, addressErr := net.SplitHostPort(address)
	n, portErr := strconv.ParseUint(port, 10, 16)
	require.True(t, addressErr == nil && host == "127.0.0.1" && portErr == nil && n > 0, "exclusive task Redis required")
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
	payload := &authv1.UserTokenPayload{UserId: operator.ID, TenantId: trans.Ptr(owner.ID), Roles: []string{*currentRole.Code}, DataScopes: []identityv1.DataScope{identityv1.DataScope_ALL}, Application: authv1.ApplicationType_console.Enum(), Sid: trans.Ptr(uuid.NewString())}
	token, _, err := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, payload)
	require.NoError(t, err)
	cleanup(func(c context.Context) error {
		return cache.RevokeTokenByJti(c, authv1.ClientType_admin, operator.ID, payload.GetJti())
	})

	key := uuid.NewString()
	peer := &environmentHTTPPeer{tenant: owner.ResourceTenantID, actor: "governance:user:" + strconv.FormatUint(uint64(operator.ID), 10), operation: key}
	peer.view = &ownerpb.TenantExecutionEnvironmentView{EnvironmentId: uuid.NewString(), ClusterId: uuid.NewString(), NamespaceName: "tenant-" + owner.ResourceTenantID, State: ownerpb.TenantExecutionEnvironmentState(1), Phase: ownerpb.TenantExecutionEnvironmentPhase(2), Retryable: true, RetryAfterSeconds: 2, UpdatedAt: timestamppb.Now()}
	config, security := registrationHTTPTLS(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	rpc := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	ownerpb.RegisterModelDevTenantEnvironmentServiceServer(rpc, peer)
	ownerpb.RegisterModelDevAdmissionServiceServer(rpc, peer)
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(rpc.Stop)
	config.Address = listener.Addr().String()
	resolver, closeResolver, err := data.NewModelDevClient(config)
	require.NoError(t, err)
	t.Cleanup(closeResolver)
	intent := conformance.IntentV1()
	bff := service.NewModelDevService(data.NewModelDevAuthorizationRepo(runtime), data.NewTenantRepo(bctx, runtime), data.NewModelDevAcceptanceRepo(runtime), resolver, &readinessHTTPCompute{t: t, tenant: owner.ResourceTenantID, actor: peer.actor})
	server := khttp.NewServer(khttp.Filter(instanceHTTPTimeoutFilter(time.Second)), khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(tenantChecker), auth.WithInjectMetadata(false), auth.WithInjectEnt(true)), authzMiddleware.Server(policy.Engine())))
	registerModelDevHTTP(server, bff)
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	assertNoAcceptance := func() {
		t.Helper()
		acceptances, e := runtime.Client().ModelDevAcceptance.Query().Count(sys)
		require.NoError(t, e)
		charges, e := runtime.Client().QuotaCharge.Query().Count(sys)
		require.NoError(t, e)
		require.Zero(t, acceptances, "no accepted execution or reliable CREATE row")
		require.Zero(t, charges, "no GPU accounting")
	}
	request := func(method, path, body string) (int, http.Header, []byte) {
		t.Helper()
		r, e := http.NewRequestWithContext(ctx, method, web.URL+path, strings.NewReader(body))
		require.NoError(t, e)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("X-Ani-Tenant-ID", uuid.NewString())
		r.Header.Set("X-Ani-Actor", "forged")
		r.Header.Set("X-Ani-Data-Scope", "platform-all")
		r.Header.Set("X-Ani-Authorized-Method", "forged")
		response, e := web.Client().Do(r)
		require.NoError(t, e)
		defer response.Body.Close()
		bodyBytes, e := io.ReadAll(io.LimitReader(response.Body, 16385))
		require.NoError(t, e)
		require.LessOrEqual(t, len(bodyBytes), 16384)
		assertNoAcceptance()
		return response.StatusCode, response.Header, bodyBytes
	}
	code, headers, body := request("POST", data.ModelDevEnsureEnvironmentPath, "{}")
	require.Equal(t, http.StatusAccepted, code, string(body))
	require.Equal(t, "2", headers.Get("Retry-After"))
	var wire struct {
		Environment struct {
			State         string `json:"state"`
			NamespaceName string `json:"namespace_name"`
		} `json:"environment"`
	}
	require.NoError(t, json.Unmarshal(body, &wire))
	require.Equal(t, "PROVISIONING", wire.Environment.State)
	peer.mu.Lock()
	uid, digest, experiment := uuid.NewString(), strings.Repeat("a", 64), uuid.NewString()
	peer.view.State, peer.view.Phase = ownerpb.TenantExecutionEnvironmentState(2), ownerpb.TenantExecutionEnvironmentPhase(6)
	peer.view.NamespaceUid, peer.view.BindingDigest, peer.view.ExperimentId = &uid, &digest, &experiment
	peer.view.Retryable, peer.view.RetryAfterSeconds, peer.view.LastVerifiedAt = false, 0, timestamppb.Now()
	peer.mu.Unlock()
	code, _, body = request("POST", data.ModelDevEnsureEnvironmentPath, "{}")
	require.Equal(t, http.StatusOK, code, string(body))
	require.NoError(t, json.Unmarshal(body, &wire))
	require.Equal(t, "READY", wire.Environment.State)
	code, _, body = request("GET", data.ModelDevGetEnvironmentPath, "")
	require.Equal(t, http.StatusOK, code, string(body))
	require.NoError(t, json.Unmarshal(body, &wire))
	require.Equal(t, "tenant-"+owner.ResourceTenantID, wire.Environment.NamespaceName)
	peer.missing.Store(true)
	code, _, body = request("GET", data.ModelDevGetEnvironmentPath, "")
	require.Equal(t, http.StatusNotFound, code, string(body))
	require.Contains(t, string(body), "ENVIRONMENT_NOT_REGISTERED")
	require.NotContains(t, string(body), "private-environment-sentinel")

	raw, err := json.Marshal(intent)
	require.NoError(t, err)
	createBody := append(bytes.Clone(raw[:len(raw)-1]), []byte(`,"idempotency_key":"`+uuid.NewString()+`"}`)...)
	code, _, body = request("POST", "/api/v1/modeldev/executions", string(createBody))
	require.Equal(t, http.StatusServiceUnavailable, code, string(body))
	require.Equal(t, int32(1), peer.prepareCalls.Load(), "real Prepare RPC reached")
	var failure struct {
		Reason   string            `json:"reason"`
		Metadata map[string]string `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(body, &failure))
	require.Equal(t, "ENVIRONMENT_NOT_READY", failure.Reason)
	require.Equal(t, map[string]string{"environment_reason": "TENANT_NAMESPACE_NOT_FOUND"}, failure.Metadata)
	require.NotContains(t, string(body), "private-environment-sentinel")
	require.Equal(t, int32(1), peer.prepareCalls.Load())
	require.Zero(t, peer.finalizeCalls.Load())
	calls := func() [4]int32 {
		return [4]int32{peer.ensureCalls.Load(), peer.getCalls.Load(), peer.prepareCalls.Load(), peer.finalizeCalls.Load()}
	}
	before := calls()
	require.Equal(t, int32(2), before[0])
	require.NoError(t, writer.Client().RolePermission.UpdateOneID(manageGrant).SetStatus(rolepermission.StatusOff).Exec(sys))
	// Keep the same JWT, key and already-loaded Casbin policies. The current
	// authoritative service check must independently observe this revocation.
	code, _, body = request("POST", data.ModelDevEnsureEnvironmentPath, "{}")
	require.Equal(t, http.StatusForbidden, code, string(body))
	require.Equal(t, before, calls(), "revoked retry cannot call any ModelDev RPC")
	code, _, body = request("GET", data.ModelDevGetEnvironmentPath, "")
	require.Equal(t, http.StatusNotFound, code, "independent view grant remains valid: %s", body)
	assertNoAcceptance()
	t.Log("ENVIRONMENT_HTTP PASS: actual JWT/Redis/Casbin/current grants/tenant mapping/mTLS; Ensure202->200/Get200/404; finite Prepare503; same-key revoked403 without upstream call; acceptance=0 charge=0")
}

type environmentHTTPPeer struct {
	ownerpb.UnimplementedModelDevTenantEnvironmentServiceServer
	ownerpb.UnimplementedModelDevAdmissionServiceServer
	tenant, actor, operation                           string
	mu                                                 sync.Mutex
	view                                               *ownerpb.TenantExecutionEnvironmentView
	missing                                            atomic.Bool
	ensureCalls, getCalls, prepareCalls, finalizeCalls atomic.Int32
}

func (p *environmentHTTPPeer) delegation(ctx context.Context, method string) error {
	md, _ := metadata.FromIncomingContext(ctx)
	expected := map[string]string{"x-ani-tenant-id": p.tenant, "x-ani-actor": p.actor}
	if method != "" {
		expected["x-ani-data-scope"], expected["x-ani-authorized-method"] = "tenant-all", method
	} else if len(md.Get("x-ani-data-scope")) != 0 || len(md.Get("x-ani-authorized-method")) != 0 {
		return status.Error(codes.PermissionDenied, "unexpected admission delegation fields")
	}
	for k, v := range expected {
		if values := md.Get(k); len(values) != 1 || values[0] != v {
			return status.Error(codes.PermissionDenied, "delegation mismatch")
		}
	}
	ids := md.Get("x-ani-request-id")
	if len(ids) != 1 || uuid.Validate(ids[0]) != nil || len(md.Get("authorization")) != 0 {
		return status.Error(codes.PermissionDenied, "invalid request identity")
	}
	return nil
}
func (p *environmentHTTPPeer) EnsureTenantExecutionEnvironment(ctx context.Context, in *ownerpb.EnsureTenantExecutionEnvironmentRequest) (*ownerpb.EnsureTenantExecutionEnvironmentResponse, error) {
	p.ensureCalls.Add(1)
	if e := p.delegation(ctx, ownerpb.ModelDevTenantEnvironmentService_EnsureTenantExecutionEnvironment_FullMethodName); e != nil {
		return nil, e
	}
	if in.OperationId != p.operation {
		return nil, status.Error(codes.InvalidArgument, "operation differs")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return &ownerpb.EnsureTenantExecutionEnvironmentResponse{Environment: proto.Clone(p.view).(*ownerpb.TenantExecutionEnvironmentView)}, nil
}
func (p *environmentHTTPPeer) GetTenantExecutionEnvironment(ctx context.Context, in *ownerpb.GetTenantExecutionEnvironmentRequest) (*ownerpb.GetTenantExecutionEnvironmentResponse, error) {
	p.getCalls.Add(1)
	if e := p.delegation(ctx, ownerpb.ModelDevTenantEnvironmentService_GetTenantExecutionEnvironment_FullMethodName); e != nil {
		return nil, e
	}
	if p.missing.Load() {
		md, _ := metadata.FromIncomingContext(ctx)
		e, _ := status.New(codes.NotFound, "private-environment-sentinel").WithDetails(&ownerpb.ErrorDetail{Reason: ownerpb.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_REGISTERED, SafeMessage: "private-environment-sentinel", CorrelationId: md.Get("x-ani-request-id")[0]})
		return nil, e.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return &ownerpb.GetTenantExecutionEnvironmentResponse{Environment: proto.Clone(p.view).(*ownerpb.TenantExecutionEnvironmentView)}, nil
}
func (p *environmentHTTPPeer) PrepareAdmission(ctx context.Context, in *ownerpb.PrepareAdmissionRequest) (*ownerpb.PrepareAdmissionResponse, error) {
	p.prepareCalls.Add(1)
	if e := p.delegation(ctx, ""); e != nil {
		return nil, e
	}
	if in.IdentityRef == nil || in.IdentityRef.TenantId != p.tenant || in.IdentityRef.Actor != p.actor {
		return nil, status.Error(codes.PermissionDenied, "admission identity differs")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	e, _ := status.New(codes.Unavailable, "private-environment-sentinel").WithDetails(&ownerpb.ErrorDetail{Reason: ownerpb.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY, SafeMessage: "private-environment-sentinel", CorrelationId: md.Get("x-ani-request-id")[0], Kind: "tenant_environment", Violations: []*ownerpb.FieldViolation{{Field: "environment", Reason: "TENANT_NAMESPACE_NOT_FOUND"}}})
	return nil, e.Err()
}
func (p *environmentHTTPPeer) FinalizeAdmission(context.Context, *ownerpb.FinalizeAdmissionRequest) (*ownerpb.FinalizeAdmissionResponse, error) {
	p.finalizeCalls.Add(1)
	return nil, status.Error(codes.Internal, "Finalize must not follow failed Prepare")
}
