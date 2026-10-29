//go:build modeldev_pg && modeldev_registration_pg

package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	ownerpb "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingpb "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
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
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
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

// Only the external ModelDev RPC is a boundary peer. JWT, current database
// grants, frozen acceptance lookup, HTTP routing and mTLS client are real.
func TestModelDevRegistrationHTTPUsesOriginalSourceAndCurrentGrants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	plan, err := writer.Client().Plan.Create().SetName("registration " + id).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Plan.DeleteOneID(plan.ID).Exec(c) })
	module, err := writer.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleModel).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().PlanModule.DeleteOneID(module.ID).Exec(c) })
	owner, err := writer.Client().Tenant.Create().SetName("registration " + id).SetCode("registration-" + id).SetResourceTenantID(uuid.NewString()).SetStatus(tenant.StatusOn).SetPlanID(plan.ID).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Tenant.DeleteOneID(owner.ID).Exec(c) })
	operator, err := writer.Client().User.Create().SetTenantID(owner.ID).SetUsername("registration-" + id).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().User.DeleteOneID(operator.ID).Exec(c) })
	currentRole, err := writer.Client().Role.Create().SetTenantID(owner.ID).SetName("registration " + id).SetCode("tenant:registration:" + id).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return writer.Client().Role.DeleteOneID(currentRole.ID).Exec(c) })
	membership, err := writer.Client().UserRole.Create().SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).SetStatus(userrole.StatusActive).Save(sys)
	require.NoError(t, err)
	cleanup(func(c context.Context) error {
		_, e := writer.Client().UserRole.Delete().Where(userrole.IDEQ(membership.ID)).Exec(c)
		return e
	})

	type routeGrant struct {
		path, method string
		id           uint32
	}
	routes := []routeGrant{
		{data.ModelDevResumeModelRegistrationPath, "POST", 0},
		{"/api/v1/model-versions/{model_version_id}", "GET", 0},
		{"/api/v1/model-versions/{model_version_id}/download-url", "GET", 0},
		{"/api/v1/model-versions", "POST", 0},
		{"/api/v1/models/{model_id}", "GET", 0},
		{"/api/v1/models", "POST", 0},
		{data.ModelDevGetExecutionPath, "GET", 0},
		{"/api/v1/modeldev/executions", "POST", 0},
		{data.ModelDevReadinessPath, "POST", 0},
		{"/api/v1/compute-presets", "GET", 0},
		{data.ModelDevListImplementationsPath, "GET", 0},
	}
	for i := range routes {
		code := "registration." + uuid.NewString()
		if i == 0 {
			code = data.ModelDevManageExecutionPermissionCode
		}
		businessModule := api.BusinessModuleModel
		if i == 9 {
			code, businessModule = "modeldev:compute-preset:read", api.BusinessModuleSystem
		}
		p, e := writer.Client().Permission.Create().SetName("registration " + id).SetCode(code).SetStatus(permission.StatusOn).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().Permission.DeleteOneID(p.ID).Exec(c) })
		a, e := writer.Client().Api.Create().SetModule("ModelDevRegistration" + id).SetOperation("registration" + uuid.NewString()).SetScope(api.ScopeAdmin).SetPath(routes[i].path).SetMethod(routes[i].method).SetBusinessModule(businessModule).SetStatus(api.StatusOn).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().Api.DeleteOneID(a.ID).Exec(c) })
		link, e := writer.Client().PermissionApi.Create().SetPermissionID(p.ID).SetAPIID(a.ID).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().PermissionApi.DeleteOneID(link.ID).Exec(c) })
		g, e := writer.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(p.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
		require.NoError(t, e)
		cleanup(func(c context.Context) error { return writer.Client().RolePermission.DeleteOneID(g.ID).Exec(c) })
		routes[i].id = g.ID
	}
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
	require.True(t, addressErr == nil && host == "127.0.0.1" && portErr == nil && n > 0, "MODELDEV_REGISTRATION_PREFLIGHT: task Redis required")
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

	acceptances := data.NewModelDevAcceptanceRepo(runtime)
	cleanup(func(c context.Context) error {
		_, e := writer.Client().ModelDevAcceptance.Delete().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Exec(c)
		return e
	})
	originalActor := "governance:user:" + strconv.FormatUint(uint64(operator.ID)+1000, 10)
	freeze := func(newModel bool, internalTenantID uint32, resourceTenantID string, artifactsOnly ...bool) (*data.ModelDevAcceptance, cpup01.Snapshot) {
		snapshot, intent := conformance.FinetuningSnapshotV1(), conformance.FinetuningIntentV1()
		snapshot.IdentityRef.ExecutionID, snapshot.IdentityRef.OperationID = uuid.NewString(), uuid.NewString()
		selection := &compute.ComputeSelection{SchemaVersion: compute.SelectionSchemaVersion, PresetId: intent.ComputePresetID, Code: "cpu-small", Name: "CPU small", ComputeType: "cpu", CpuMillis: 1000, MemoryMib: 1024}
		var e error
		selection.SelectionDigest, e = compute.SelectionDigest(selection)
		require.NoError(t, e)
		canonical, e := compute.CanonicalSelection(selection)
		require.NoError(t, e)
		snapshot.ComputeOrigin = cpup01.ComputeOrigin{ComputePresetID: selection.PresetId, SelectionDigest: selection.SelectionDigest, SelectionJSON: string(canonical), CPUMillis: selection.CpuMillis, MemoryMiB: selection.MemoryMib, DeviceMode: "cpu"}

		target := uuid.NewString()
		intent.TargetModelID = &target
		intent.NewModelName = nil
		snapshot.Finetuning.TargetModelID = &target
		snapshot.Finetuning.NewModelName = nil
		if newModel {
			name := "registration-fixture-" + uuid.NewString()
			intent.TargetModelID = nil
			intent.NewModelName = &name
			snapshot.Finetuning.TargetModelID = nil
			snapshot.Finetuning.NewModelName = &name
		}
		if len(artifactsOnly) == 1 && artifactsOnly[0] {
			intent.TargetModelID, intent.NewModelName = nil, nil
			snapshot.Finetuning.TargetModelID, snapshot.Finetuning.NewModelName = nil, nil
		}
		acceptedAt := time.Now().UTC().Truncate(time.Microsecond)
		_, hash, e := cpup01.CanonicalIntent(intent)
		require.NoError(t, e)
		snapshot.IdentityRef.TenantID, snapshot.IdentityRef.Actor, snapshot.IdentityRef.IntentHash = resourceTenantID, originalActor, hash
		if len(artifactsOnly) == 1 && artifactsOnly[0] {
			snapshot.IdentityRef.Actor = "governance:user:" + strconv.FormatUint(uint64(operator.ID), 10)
		}
		snapshot.Timing.AcceptedAt = acceptedAt
		snapshot.Timing.ExecutionDeadlineAt = acceptedAt.Add(time.Duration(snapshot.Timing.ExecutionTimeoutSeconds) * time.Second)
		snapshot.DeadlineAt = snapshot.Timing.ExecutionDeadlineAt
		snapshot.RuntimePlan.Name = cpup01.RuntimeName(snapshot.Environment.NamespaceUID, snapshot.IdentityRef.ExecutionID)
		a, replay, e := acceptances.AcceptFrozen(sys, data.ModelDevAdmissionScope{TenantID: internalTenantID, ResourceTenantID: resourceTenantID, Actor: snapshot.IdentityRef.Actor, Action: data.ModelDevCreateAction, IdempotencyKey: uuid.NewString()}, data.ModelDevFrozenCandidate{OperationID: snapshot.IdentityRef.OperationID, ExecutionID: snapshot.IdentityRef.ExecutionID, Intent: intent, Snapshot: snapshot, AcceptedAt: acceptedAt})
		require.NoError(t, e)
		require.False(t, replay)
		return a, snapshot
	}
	accepted, snapshot := freeze(false, owner.ID, owner.ResourceTenantID)
	actor := "governance:user:" + strconv.FormatUint(uint64(operator.ID), 10)
	require.NotEqual(t, originalActor, actor)
	peer := &registrationHTTPPeer{tenant: owner.ResourceTenantID, actor: actor}
	client := registrationHTTPClient(t, peer)
	view := func(a *data.ModelDevAcceptance, s cpup01.Snapshot) *ownerpb.GetExecutionResponse {
		wire, e := contractpb.EncodeSnapshot(s)
		require.NoError(t, e)
		observed := timestamppb.Now()
		return &ownerpb.GetExecutionResponse{Execution: &ownerpb.ExecutionView{
			Identity: &trainingpb.ExecutionIdentity{ExecutionId: a.ExecutionID, OperationId: a.OperationID, ExecutionSpecHash: a.ExecutionSpecHash}, Kind: trainingpb.ExecutionKind_EXECUTION_KIND_FINETUNING, ImplementationId: "builtin.sft_lora", ComputePresetId: s.ComputeOrigin.ComputePresetID,
			ComputeOrigin: wire.ComputeOrigin, Timing: wire.Timing, AcceptedAt: wire.Timing.AcceptedAt, DeadlineAt: wire.Timing.ExecutionDeadlineAt, ObservedAt: observed,
			History:           &ownerpb.ExecutionHistoryView{RunId: uuid.NewString(), WorkflowUid: uuid.NewString(), ObservedAt: observed, RunState: ownerpb.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_SUCCEEDED, RunFinishedAt: wire.Timing.AcceptedAt, WorkflowPhase: ownerpb.WorkflowTerminalPhase_WORKFLOW_TERMINAL_PHASE_SUCCEEDED, WorkflowFinishedAt: wire.Timing.AcceptedAt, WorkflowResourceVersion: "51930001", WorkflowNodesSha256: strings.Repeat("a", 64)},
			States:            &ownerpb.ExecutionStates{ComputeState: ownerpb.ComputeState_COMPUTE_STATE_SUCCEEDED, DeliveryState: ownerpb.DeliveryState_DELIVERY_STATE_PUBLISHED, CloseState: ownerpb.CloseState_CLOSE_STATE_CLOSED, ResourceState: ownerpb.ResourceState_RESOURCE_STATE_NOT_APPLICABLE},
			ModelRegistration: &ownerpb.ModelRegistrationView{State: "READY", RegistrationId: uuid.NewString(), ModelId: targetModelID(s), ModelVersionId: uuid.NewString()},
		}}
	}
	peer.setReply(view(accepted, snapshot))
	computeReader := &readinessHTTPCompute{t: t, tenant: owner.ResourceTenantID, actor: actor}
	bff := service.NewModelDevService(data.NewModelDevAuthorizationRepo(runtime), data.NewTenantRepo(bctx, runtime), acceptances, client, computeReader)
	server := khttp.NewServer(khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(tenantChecker), auth.WithInjectMetadata(false), auth.WithInjectEnt(true)), authzMiddleware.Server(policy.Engine())))
	registerModelDevRegistrationHTTP(server, bff)
	registerModelDevReadinessHTTP(server, bff)
	registerModelDevCatalogueHTTP(server, bff)
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	post := func(execution, body, query, access string) (int, map[string]any) {
		req, e := http.NewRequestWithContext(ctx, "POST", web.URL+"/api/v1/modeldev/executions/"+execution+":resume-model-registration"+query, bytes.NewBufferString(body))
		require.NoError(t, e)
		if access != "" {
			req.Header.Set("Authorization", "Bearer "+access)
		}
		req.Header.Set("x-ani-tenant-id", uuid.NewString())
		req.Header.Set("x-ani-actor", "governance:user:7")
		resp, e := web.Client().Do(req)
		require.NoError(t, e)
		defer resp.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 16385))
		require.NoError(t, e)
		out := map[string]any{}
		if len(raw) > 0 {
			require.NoError(t, json.Unmarshal(raw, &out))
		}
		require.Contains(t, resp.Header.Get("Cache-Control"), "no-store")
		return resp.StatusCode, out
	}

	t.Run("implementation mode and capability tuple", func(t *testing.T) {
		get := func(query, access string) (int, map[string]any) {
			request, e := http.NewRequestWithContext(ctx, "GET", web.URL+data.ModelDevListImplementationsPath+query, nil)
			require.NoError(t, e)
			if access != "" {
				request.Header.Set("Authorization", "Bearer "+access)
			}
			response, e := web.Client().Do(request)
			require.NoError(t, e)
			defer response.Body.Close()
			raw, e := io.ReadAll(io.LimitReader(response.Body, cpup01.MaxControlRPCBytes+1))
			require.NoError(t, e)
			out := map[string]any{}
			require.NoError(t, json.Unmarshal(raw, &out))
			return response.StatusCode, out
		}
		for _, mode := range []string{"cpu", "gpu"} {
			code, out := get("?device_mode="+mode+"&kind=FINETUNING&page=1&pageSize=20", token)
			require.Equal(t, http.StatusOK, code, "CURRENT_CATALOGUE_HTTP: explicit mode must reach the current owner contract: %v", out)
			require.Equal(t, "1", out["total"])
			items := out["items"].([]any)
			require.Len(t, items, 1)
			item := items[0].(map[string]any)
			require.Equal(t, "builtin.sft_lora", item["implementation_id"])
			caps := catalogueHTTPCapabilities(mode)
			digest, e := caps.Digest()
			require.NoError(t, e)
			require.Equal(t, digest, item["runtime_capabilities_sha256"], "CURRENT_CATALOGUE_CAPABILITIES: preserve exact owner capability digest")
			wire := item["runtime_capabilities"].(map[string]any)
			require.Equal(t, caps.ImageDigest, wire["image_digest"])
			require.Equal(t, caps.EntrypointSHA256, wire["entrypoint_sha256"])
			require.Equal(t, []any{mode}, item["supported_device_modes"])
		}
		require.Equal(t, int32(2), peer.catalogueCalls.Load())
		code, _ := get("?device_mode=cpu", "")
		require.Equal(t, http.StatusUnauthorized, code)
		for _, query := range []string{"", "?device_mode=", "?device_mode=CPU", "?device_mode=tpu", "?device_mode=cpu&device_mode=gpu", "?deviceMode=cpu"} {
			code, _ := get(query, token)
			require.Equal(t, http.StatusBadRequest, code, query)
		}
		require.Equal(t, int32(2), peer.catalogueCalls.Load(), "invalid or unauthenticated requests must not enter the external owner")
		calls := peer.catalogueCalls.Load()
		require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[10].id).SetStatus(rolepermission.StatusOff).Exec(sys))
		code, _ = get("?device_mode=cpu", token)
		require.Equal(t, http.StatusForbidden, code)
		require.Equal(t, calls, peer.catalogueCalls.Load(), "revoked ordinary list permission must stop before RPC")
		require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[10].id).SetStatus(rolepermission.StatusOn).Exec(sys))
	})
	// Source-only Readiness runs through the same real JWT, tenant, Casbin and
	// restricted PG path. Only external service facts are substituted.
	t.Run("source-only Readiness current HTTP grants", func(t *testing.T) {
		requestReadiness := func(access string) (int, map[string]any) {
			body := `{"base_model_version_id":"` + conformance.FinetuningSnapshotV1().Finetuning.BaseModel.VersionID + `","compute_preset_id":"` + conformance.FinetuningIntentV1().ComputePresetID + `"}`
			req, e := http.NewRequestWithContext(ctx, "POST", web.URL+data.ModelDevReadinessPath, strings.NewReader(body))
			require.NoError(t, e)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-ani-tenant-id", uuid.NewString())
			req.Header.Set("x-ani-actor", "governance:user:7")
			if access != "" {
				req.Header.Set("Authorization", "Bearer "+access)
			}
			resp, e := web.Client().Do(req)
			require.NoError(t, e)
			defer resp.Body.Close()
			raw, e := io.ReadAll(io.LimitReader(resp.Body, cpup01.MaxControlRPCBytes+1))
			require.NoError(t, e)
			out := map[string]any{}
			require.NoError(t, json.Unmarshal(raw, &out))
			return resp.StatusCode, out
		}
		before, e := runtime.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Count(sys)
		require.NoError(t, e)
		code, _ := requestReadiness("")
		require.Equal(t, http.StatusUnauthorized, code)
		for _, index := range []int{3, 4, 5, 7} {
			require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[index].id).SetStatus(rolepermission.StatusOff).Exec(sys))
		}
		code, out := requestReadiness(token)
		require.Equal(t, http.StatusOK, code, "source-only Readiness must not require target write/read or execution Create")
		require.Equal(t, "ELIGIBLE", out["assessment"])
		require.Equal(t, "9007199254740993", out["limits"].(map[string]any)["max_content_bytes"])
		require.Equal(t, conformance.FinetuningSnapshotV1().Finetuning.BaseModel.VersionID, out["model_version_id"])
		require.True(t, out["runtime_validation_required"].(bool))
		require.Equal(t, int32(1), peer.readinessCalls.Load())
		for _, index := range []int{1, 2, 8, 9} {
			require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[index].id).SetStatus(rolepermission.StatusOff).Exec(sys))
			code, _ = requestReadiness(token)
			require.Equal(t, http.StatusForbidden, code, "revoked current source, Readiness or TRAINING compute grant must stop before the external RPC")
			require.Equal(t, int32(1), peer.readinessCalls.Load())
			require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[index].id).SetStatus(rolepermission.StatusOn).Exec(sys))
		}
		for _, index := range []int{3, 4, 5, 7} {
			require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[index].id).SetStatus(rolepermission.StatusOn).Exec(sys))
		}
		after, e := runtime.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Count(sys)
		require.NoError(t, e)
		require.Equal(t, before, after, "Readiness cannot persist execution acceptance")
		require.Equal(t, 1, computeReader.calls)
	})
	code, out := post(accepted.ExecutionID, "", "", token)
	require.Equal(t, http.StatusOK, code, "current real auth, original frozen source and owner receipt must resume")
	require.Equal(t, accepted.ExecutionID, out["execution_id"])
	require.Nil(t, out["execution"])
	registration := out["model_registration"].(map[string]any)
	require.Equal(t, "READY", registration["state"])
	require.Equal(t, peer.response().Execution.ModelRegistration.ModelVersionId, registration["model_version_id"])
	require.Equal(t, int32(1), peer.calls.Load())
	history := out["history"].(map[string]any)
	require.Equal(t, peer.response().Execution.History.RunId, history["run_id"])
	require.Equal(t, "SUCCEEDED", history["run_state"])
	originalReply := peer.response()
	for _, registration := range []*ownerpb.ModelRegistrationView{{State: "PENDING"}, {State: "DEPENDENCY_UNAVAILABLE", FailureCode: "MODEL_DEPENDENCY_UNAVAILABLE"}} {
		pending := proto.Clone(originalReply).(*ownerpb.GetExecutionResponse)
		pending.Execution.ModelRegistration = registration
		peer.setReply(pending)
		code, out = post(accepted.ExecutionID, "", "", token)
		require.Equal(t, http.StatusOK, code)
		r := out["model_registration"].(map[string]any)
		require.Equal(t, registration.State, r["state"])
		require.Empty(t, r["registration_id"])
		require.Empty(t, r["model_id"])
		require.Empty(t, r["model_version_id"])
		require.Equal(t, originalReply.Execution.History.RunId, out["history"].(map[string]any)["run_id"], "registration recovery preserves the original owner history")
	}
	peer.setReply(originalReply)
	for _, bad := range []struct{ body, query string }{{`{}`, ""}, {`{"tenant_id":"injected"}`, ""}, {`{"new_model_name":"another"}`, ""}, {" ", ""}, {"", "?"}, {"", "?actor=injected"}, {"", "?idempotency_key=new"}} {
		before := peer.calls.Load()
		code, _ = post(accepted.ExecutionID, bad.body, bad.query, token)
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, before, peer.calls.Load())
	}
	before := peer.calls.Load()
	code, _ = post(accepted.ExecutionID, "", "", "")
	require.Equal(t, http.StatusUnauthorized, code)
	require.Equal(t, before, peer.calls.Load())
	code, _ = post(uuid.NewString(), "", "", token)
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, before, peer.calls.Load())
	foreign, e := writer.Client().Tenant.Create().SetName("foreign registration " + id).SetCode("foreign-registration-" + id).SetResourceTenantID(uuid.NewString()).SetStatus(tenant.StatusOn).SetPlanID(plan.ID).Save(sys)
	require.NoError(t, e)
	cleanup(func(c context.Context) error { return writer.Client().Tenant.DeleteOneID(foreign.ID).Exec(c) })
	cleanup(func(c context.Context) error {
		_, e := writer.Client().ModelDevAcceptance.Delete().Where(modeldevacceptance.TenantIDEQ(foreign.ID)).Exec(c)
		return e
	})
	foreignAccepted, _ := freeze(false, foreign.ID, foreign.ResourceTenantID)
	code, _ = post(foreignAccepted.ExecutionID, "", "", token)
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, before, peer.calls.Load(), "another tenant's original acceptance never reaches the owner")

	// The active actor differs from the accepted actor. Historical Create does
	// not authorize Resume, and a cached JWT/Casbin grant cannot repair revocation.
	for _, index := range []int{0, 1, 2, 3, 4, 6} {
		require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[index].id).SetStatus(rolepermission.StatusOff).Exec(sys))
		before = peer.calls.Load()
		code, _ = post(accepted.ExecutionID, "", "", token)
		require.Equal(t, http.StatusForbidden, code)
		require.Equal(t, before, peer.calls.Load())
		require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[index].id).SetStatus(rolepermission.StatusOn).Exec(sys))
	}
	newAccepted, newSnapshot := freeze(true, owner.ID, owner.ResourceTenantID)
	newReply := view(newAccepted, newSnapshot)
	newReply.Execution.ModelRegistration.ModelId = uuid.NewString()
	peer.setReply(newReply)
	require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[4].id).SetStatus(rolepermission.StatusOff).Exec(sys))
	code, _ = post(newAccepted.ExecutionID, "", "", token)
	require.Equal(t, http.StatusOK, code, "new-model branch uses create-model permission, not existing-model permission")
	require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[4].id).SetStatus(rolepermission.StatusOn).Exec(sys))
	require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[5].id).SetStatus(rolepermission.StatusOff).Exec(sys))
	before = peer.calls.Load()
	code, _ = post(newAccepted.ExecutionID, "", "", token)
	require.Equal(t, http.StatusForbidden, code)
	require.Equal(t, before, peer.calls.Load())
	peer.setReply(view(accepted, snapshot))
	code, _ = post(accepted.ExecutionID, "", "", token)
	require.Equal(t, http.StatusOK, code, "existing-model branch does not require create-model permission")
	require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[5].id).SetStatus(rolepermission.StatusOn).Exec(sys))
	peer.setReply(view(accepted, snapshot))
	for _, change := range []func(*ownerpb.GetExecutionResponse){
		func(r *ownerpb.GetExecutionResponse) { r.Execution.Identity.OperationId = uuid.NewString() },
		func(r *ownerpb.GetExecutionResponse) {
			r.Execution.Identity.ExecutionSpecHash = string(bytes.Repeat([]byte("b"), 64))
		},
		func(r *ownerpb.GetExecutionResponse) { r.Execution.ModelRegistration.ModelId = uuid.NewString() },
	} {
		original := peer.response()
		bad := proto.Clone(original).(*ownerpb.GetExecutionResponse)
		change(bad)
		peer.setReply(bad)
		code, _ = post(accepted.ExecutionID, "", "", token)
		require.Equal(t, http.StatusServiceUnavailable, code)
		peer.setReply(original)
	}
	peer.setRemote(status.Error(codes.Unavailable, "private registration receipt outcome unknown"))
	before = peer.calls.Load()
	code, _ = post(accepted.ExecutionID, "", "", token)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Equal(t, before+1, peer.calls.Load(), "unknown outcome is not automatically repeated")
	// No new acceptance, stop, or registration key is manufactured by recovery.
	count, e := writer.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Count(sys)
	require.NoError(t, e)
	require.Equal(t, 2, count)
	actualSource, e := acceptances.FindRegistrationSource(sys, owner.ID, owner.ResourceTenantID, accepted.ExecutionID)
	require.NoError(t, e)
	require.Equal(t, accepted.Scope, actualSource.Scope)
	require.Equal(t, accepted.IntentHash, actualSource.IntentHash)
	require.Equal(t, accepted.ExecutionSpecHash, actualSource.ExecutionSpecHash)

	// An omitted target is a distinct frozen intent. It keeps the real base
	// read grants while requiring no target-model writes, even on replay.
	artifactAcceptance, artifactSnapshot := freeze(false, owner.ID, owner.ResourceTenantID, true)
	require.Nil(t, artifactSnapshot.Finetuning.NewModelName)
	require.Nil(t, artifactSnapshot.Finetuning.TargetModelID)
	for _, index := range []int{3, 4, 5} {
		require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[index].id).SetStatus(rolepermission.StatusOff).Exec(sys))
	}
	valid, claims := checker.IsValidAccessToken(ctx, token, false)
	require.True(t, valid)
	requestContext := auth.NewContext(sys, claims)
	input := service.ModelDevCreateInput{Intent: artifactAcceptance.Intent, IdempotencyKey: artifactAcceptance.Scope.IdempotencyKey}
	replayed, err := bff.CreateExecution(requestContext, input)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, artifactAcceptance.ExecutionID, replayed.ExecutionId)
	changed := input
	changed.Intent.Name += "-different"
	_, err = bff.CreateExecution(requestContext, changed)
	require.Error(t, err)
	require.Equal(t, int32(http.StatusConflict), errors.FromError(err).Code, "the same key cannot replace the frozen intent")
	before = peer.calls.Load()
	code, _ = post(artifactAcceptance.ExecutionID, "", "", token)
	require.Equal(t, http.StatusPreconditionFailed, code, "artifact-only output has no registration to resume")
	require.Equal(t, before, peer.calls.Load())
	for _, index := range []int{1, 2} {
		require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[index].id).SetStatus(rolepermission.StatusOff).Exec(sys))
		_, err = bff.CreateExecution(requestContext, input)
		require.Error(t, err)
		require.Equal(t, int32(http.StatusForbidden), errors.FromError(err).Code, "a frozen key cannot bypass current base-model read access")
		require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[index].id).SetStatus(rolepermission.StatusOn).Exec(sys))
	}
	for _, index := range []int{3, 4, 5} {
		require.NoError(t, writer.Client().RolePermission.UpdateOneID(routes[index].id).SetStatus(rolepermission.StatusOn).Exec(sys))
	}
	withTarget := input
	name := "late-registration-target"
	withTarget.Intent.NewModelName = &name
	_, err = bff.CreateExecution(requestContext, withTarget)
	require.Error(t, err)
	require.Equal(t, int32(http.StatusConflict), errors.FromError(err).Code, "current write authority cannot add a target to an already accepted artifact-only intent")
	t.Log("normal JWT/Redis/Casbin, current PG grants, original frozen FT source, strict HTTP and mTLS boundary passed; actual provider Resume remains separate")
}

type registrationHTTPPeer struct {
	ownerpb.UnimplementedModelDevOperationsServiceServer
	ownerpb.UnimplementedModelDevQueryServiceServer
	readinessCalls atomic.Int32
	catalogueCalls atomic.Int32
	mu             sync.Mutex
	tenant, actor  string
	reply          *ownerpb.GetExecutionResponse
	remote         error
	calls          atomic.Int32
}

func (p *registrationHTTPPeer) ResumeModelRegistration(ctx context.Context, in *ownerpb.ResumeModelRegistrationRequest) (*ownerpb.GetExecutionResponse, error) {
	p.calls.Add(1)
	md, _ := metadata.FromIncomingContext(ctx)
	for k, v := range map[string]string{"x-ani-tenant-id": p.tenant, "x-ani-actor": p.actor, "x-ani-data-scope": "tenant-all", "x-ani-authorized-method": ownerpb.ModelDevOperationsService_ResumeModelRegistration_FullMethodName} {
		if a := md.Get(k); len(a) != 1 || a[0] != v {
			return nil, status.Error(codes.PermissionDenied, "delegation mismatch")
		}
	}
	if in.ExecutionId == "" || len(in.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid original execution")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.remote != nil {
		return nil, p.remote
	}
	return p.reply, nil
}
func registrationHTTPTLS(t *testing.T) (data.ModelDevClientConfig, *tls.Config) {
	t.Helper()
	directory := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ModelDev response substitute CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, public, private)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caFile := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	issue := func(label string, serial int64, names []string, usage x509.ExtKeyUsage) (string, string, tls.Certificate) {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: names, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		encoded, err := x509.CreateCertificate(rand.Reader, leaf, ca, pub, private)
		if err != nil {
			t.Fatal(err)
		}
		keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certFile, keyFile := filepath.Join(directory, label+".pem"), filepath.Join(directory, label+".key")
		if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0600); err != nil {
			t.Fatal(err)
		}
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		return certFile, keyFile, pair
	}
	_, _, server := issue("server", 2, []string{"ani-modeldev-service"}, x509.ExtKeyUsageServerAuth)
	certFile, keyFile, _ := issue("governance", 3, []string{"ani-governance"}, x509.ExtKeyUsageClientAuth)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("MODELDEV_RESPONSE_PREFLIGHT: generated CA invalid")
	}
	return data.ModelDevClientConfig{CAFile: caFile, CertFile: certFile, KeyFile: keyFile, Timeout: time.Second}, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{server}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}
}
func registrationHTTPClient(t *testing.T, peer *registrationHTTPPeer) *data.ModelDevClient {
	t.Helper()
	config, security := registrationHTTPTLS(t)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, e)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	ownerpb.RegisterModelDevOperationsServiceServer(server, peer)
	ownerpb.RegisterModelDevQueryServiceServer(server, peer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	config.Address = listener.Addr().String()
	client, closeClient, e := data.NewModelDevClient(config)
	require.NoError(t, e)
	t.Cleanup(closeClient)
	return client
}

func targetModelID(s cpup01.Snapshot) string {
	if s.Finetuning.TargetModelID != nil {
		return *s.Finetuning.TargetModelID
	}
	return uuid.NewString()
}

// Reuses the existing ModelDev HTTP fixture guards with a separate test tag,
// so this slice does not activate the historical Release provider fixture.
func prepareRegistrationHTTPDatabase(t *testing.T, ctx context.Context) (*entCrud.EntClient[*ent.Client], *entCrud.EntClient[*ent.Client]) {
	t.Helper()
	runtimeDSN := os.Getenv("ANI_TEST_DATABASE_DSN")
	file := os.Getenv("ANI_MODELDEV_FIXTURE_DSN_FILE")
	if runtimeDSN == "" || os.Getenv("ANI_TEST_DATABASE_EXCLUSIVE") != "1" || !filepath.IsAbs(file) {
		t.Fatal("MODELDEV_REGISTRATION_PREFLIGHT: explicit exclusive PG and private fixture identity required")
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		t.Fatal("MODELDEV_REGISTRATION_PREFLIGHT: bounded private fixture DSN file required")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("MODELDEV_REGISTRATION_PREFLIGHT: cannot read fixture identity")
	}
	fixtureDSN := strings.TrimSpace(string(raw))
	runtimeConfig, runtimeErr := pgx.ParseConfig(runtimeDSN)
	fixtureConfig, fixtureErr := pgx.ParseConfig(fixtureDSN)
	if runtimeErr != nil || fixtureErr != nil || runtimeConfig.Host != fixtureConfig.Host || runtimeConfig.Port != fixtureConfig.Port || runtimeConfig.Database != fixtureConfig.Database || runtimeConfig.User == fixtureConfig.User {
		t.Fatal("MODELDEV_REGISTRATION_PREFLIGHT: runtime/fixture must be separate roles on the same task database")
	}
	runtime, writer := openRegistrationHTTPPG(t, runtimeDSN), openRegistrationHTTPPG(t, fixtureDSN)
	var runtimeUser, fixtureUser, runtimeDatabase, fixtureDatabase, runtimeAddress, fixtureAddress string
	var runtimePort, fixturePort int
	identitySQL := `SELECT current_user,current_database(),coalesce(inet_server_addr()::text,''),coalesce(inet_server_port(),0)`
	require.NoError(t, runtime.DB().QueryRowContext(ctx, identitySQL).Scan(&runtimeUser, &runtimeDatabase, &runtimeAddress, &runtimePort))
	require.NoError(t, writer.DB().QueryRowContext(ctx, identitySQL).Scan(&fixtureUser, &fixtureDatabase, &fixtureAddress, &fixturePort))
	require.Equal(t, runtimeConfig.User, runtimeUser)
	require.Equal(t, fixtureConfig.User, fixtureUser)
	require.NotEqual(t, runtimeUser, fixtureUser)
	require.Equal(t, runtimeDatabase, fixtureDatabase)
	require.Equal(t, runtimeAddress, fixtureAddress)
	require.Equal(t, runtimePort, fixturePort)
	var superuser, bypassRLS, canCreate, canTemp bool
	require.NoError(t, runtime.DB().QueryRowContext(ctx, `SELECT rolsuper,rolbypassrls,has_database_privilege(current_database(),'CREATE'),has_database_privilege(current_database(),'TEMP') FROM pg_roles WHERE rolname=current_user`).Scan(&superuser, &bypassRLS, &canCreate, &canTemp))
	require.False(t, superuser)
	require.False(t, bypassRLS)
	require.False(t, canCreate)
	require.False(t, canTemp)
	return runtime, writer
}

func openRegistrationHTTPPG(t *testing.T, dsn string) *entCrud.EntClient[*ent.Client] {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("MODELDEV_REGISTRATION_PREFLIGHT: PG client configuration failed")
	}
	driver := entsql.OpenDB("postgres", db)
	client := ent.NewClient(ent.Driver(driver))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("MODELDEV_REGISTRATION_PREFLIGHT: PG unavailable")
	}
	return entCrud.NewEntClient(client, driver)
}

func (p *registrationHTTPPeer) setReply(reply *ownerpb.GetExecutionResponse) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reply = reply
}
func (p *registrationHTTPPeer) response() *ownerpb.GetExecutionResponse {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reply
}
func (p *registrationHTTPPeer) setRemote(err error) { p.mu.Lock(); defer p.mu.Unlock(); p.remote = err }

// The compute service is an external boundary; current TRAINING authorization
// remains the real Governance repository before this fact can be requested.
type readinessHTTPCompute struct {
	t             *testing.T
	tenant, actor string
	calls         int
}

func (p *readinessHTTPCompute) ResolveComputePreset(_ context.Context, tenant, actor string, request *compute.ResolveComputePresetRequest) (*compute.ComputeSelection, error) {
	require.Equal(p.t, p.tenant, tenant)
	require.Equal(p.t, p.actor, actor)
	require.Equal(p.t, p.tenant, request.TenantId)
	require.Equal(p.t, compute.Usage_TRAINING, request.Usage)
	p.calls++
	selection := &compute.ComputeSelection{SchemaVersion: compute.SelectionSchemaVersion, PresetId: request.PresetId, Code: "cpu-small", Name: "CPU small", ComputeType: "cpu", CpuMillis: 1000, MemoryMib: 1024}
	var err error
	selection.SelectionDigest, err = compute.SelectionDigest(selection)
	return selection, err
}
func (p *registrationHTTPPeer) CheckFinetuningReadiness(ctx context.Context, in *ownerpb.CheckFinetuningReadinessRequest) (*ownerpb.CheckFinetuningReadinessResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	for key, want := range map[string]string{"x-ani-tenant-id": p.tenant, "x-ani-actor": p.actor, "x-ani-data-scope": "tenant-all", "x-ani-authorized-method": ownerpb.ModelDevQueryService_CheckFinetuningReadiness_FullMethodName} {
		if values := md.Get(key); len(values) != 1 || values[0] != want {
			return nil, status.Error(codes.PermissionDenied, "delegation mismatch")
		}
	}
	origin, err := contractpb.DecodeComputeOrigin(in.ComputeOrigin)
	if err != nil || origin.ComputePresetID != in.ComputePresetId {
		return nil, status.Error(codes.InvalidArgument, "invalid compute origin")
	}
	p.readinessCalls.Add(1)
	caps := conformance.FinetuningRuntimeCapabilities()
	wire, err := contractpb.EncodeRuntimeCapabilities(caps)
	if err != nil {
		return nil, err
	}
	digest, err := caps.Digest()
	if err != nil {
		return nil, err
	}
	plan, err := contractpb.EncodeTemplatePlan(conformance.FinetuningTemplatePlan())
	if err != nil {
		return nil, err
	}
	base := conformance.FinetuningSnapshotV1().Finetuning.BaseModel
	return &ownerpb.CheckFinetuningReadinessResponse{Assessment: ownerpb.FinetuningAssessment_FINETUNING_ASSESSMENT_ELIGIBLE, RuntimeCapabilities: wire, RuntimeCapabilitiesSha256: digest, TemplateCandidates: wire.RegisteredTemplates, RuntimeValidationRequired: true,
		ModelId: base.ModelID, ModelVersionId: in.BaseModelVersionId, ModelType: base.Facts.ModelType, ContextLimit: base.Facts.ContextLimit, TemplatePlan: plan,
		Limits:          &ownerpb.ContentLimits{MaxContentBytes: 9007199254740993},
		StructureLimits: &ownerpb.StructureLimits{MaxModelFiles: cpup01.MaxModelFiles, MaxOutputFiles: cpup01.MaxOutputFiles, MaxArchiveEntries: cpup01.MaxArchiveEntries, MaxRelativePathBytes: cpup01.MaxRelativePathBytes, MaxStorageKeyBytes: cpup01.MaxStorageKeyBytes, MaxManifestBytes: cpup01.MaxManifestBytes, MaxMaterialBytes: cpup01.MaxMaterialBytes, MaxModelConfigBytes: cpup01.MaxModelConfigBytes, MaxArchiveMetadataBytes: cpup01.MaxArchiveMetadataBytes, MaxRecordBytes: cpup01.MaxRecordBytes, MaxJsonDepth: cpup01.MaxJSONDepth, MaxControlRpcBytes: cpup01.MaxControlRPCBytes, MaxPublicationCandidateBytes: cpup01.MaxPublicationCandidateBytes, MaxPublicationRefBytes: cpup01.MaxPublicationRefBytes}}, nil
}

func catalogueHTTPCapabilities(mode string) cpup01.RuntimeCapabilities {
	caps := conformance.FinetuningRuntimeCapabilities()
	if mode == "gpu" {
		caps.ImageDigest = "registry.example.test/gpu/sft@sha256:" + strings.Repeat("b", 64)
	}
	return caps
}
func (p *registrationHTTPPeer) ListImplementations(ctx context.Context, in *ownerpb.ListImplementationsRequest) (*ownerpb.ListImplementationsResponse, error) {
	p.catalogueCalls.Add(1)
	md, _ := metadata.FromIncomingContext(ctx)
	for key, want := range map[string]string{"x-ani-tenant-id": p.tenant, "x-ani-actor": p.actor, "x-ani-data-scope": "tenant-all", "x-ani-authorized-method": ownerpb.ModelDevQueryService_ListImplementations_FullMethodName} {
		if got := md.Get(key); len(got) != 1 || got[0] != want {
			return nil, status.Error(codes.PermissionDenied, "delegation mismatch")
		}
	}
	if in.DeviceMode == nil || (*in.DeviceMode != "cpu" && *in.DeviceMode != "gpu") {
		return nil, status.Error(codes.InvalidArgument, "explicit device mode required")
	}
	if in.Kind == nil || *in.Kind != trainingpb.ExecutionKind_EXECUTION_KIND_FINETUNING || in.Page == nil || in.Page.Page == nil || *in.Page.Page != 1 || in.Page.PageSize == nil || *in.Page.PageSize != 20 {
		return nil, status.Error(codes.InvalidArgument, "kind/page mismatch")
	}
	caps := catalogueHTTPCapabilities(*in.DeviceMode)
	wire, err := contractpb.EncodeRuntimeCapabilities(caps)
	if err != nil {
		return nil, err
	}
	digest, err := caps.Digest()
	if err != nil {
		return nil, err
	}
	return &ownerpb.ListImplementationsResponse{Total: 1, Implementations: []*ownerpb.ImplementationView{{ImplementationId: "builtin.sft_lora", Name: "SFT LoRA", Kind: trainingpb.ExecutionKind_EXECUTION_KIND_FINETUNING, Version: "1", ParameterContractVersion: "ani.sft.parameters.v3", SupportedDeviceModes: []string{*in.DeviceMode}, Available: true, RuntimeCapabilities: wire, RuntimeCapabilitiesSha256: digest}}}, nil
}
