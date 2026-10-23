//go:build modeldev_pg && modeldev_contract

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
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

	entsql "entgo.io/ent/dialect/sql"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
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
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	authzEngine "go-wind-admin/pkg/localdeps/kratos-authz/engine"
	authzMiddleware "go-wind-admin/pkg/localdeps/kratos-authz/middleware"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestModelDevCreatePersistsBefore202AndReplaysOriginal(t *testing.T) {
	provider := testutil.ReadModelDevContractFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	sys := appViewer.NewSystemViewerContext(ctx)
	runtime, writer := prepareModelDevHTTPDatabase(t, ctx)
	removeAfterTest := func(remove func(context.Context) error) {
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
			defer stop()
			require.NoError(t, remove(cleanup), "remove only the owned HTTP fixture")
		})
	}
	suffix := uuid.NewString()
	plan, err := writer.Client().Plan.Create().SetName("modeldev-http-" + suffix).Save(sys)
	require.NoError(t, err, "MODELDEV_CREATE_PREFLIGHT: plan fixture")
	removeAfterTest(func(c context.Context) error { return writer.Client().Plan.DeleteOneID(plan.ID).Exec(c) })
	module, err := writer.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleModel).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error { return writer.Client().PlanModule.DeleteOneID(module.ID).Exec(c) })
	owner, err := writer.Client().Tenant.Create().SetName("modeldev HTTP fixture").SetCode("cpu-http-" + suffix).
		SetResourceTenantID(provider.Scope.ResourceTenantID).SetStatus(tenant.StatusOn).SetPlanID(plan.ID).Save(sys)
	require.NoError(t, err, "MODELDEV_CREATE_PREFLIGHT: fresh resource tenant fixture required")
	removeAfterTest(func(c context.Context) error { return writer.Client().Tenant.DeleteOneID(owner.ID).Exec(c) })
	operator, err := writer.Client().User.Create().SetTenantID(owner.ID).SetUsername("cpu-http-" + suffix).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error { return writer.Client().User.DeleteOneID(operator.ID).Exec(c) })
	currentRole, err := writer.Client().Role.Create().SetTenantID(owner.ID).SetName("modeldev HTTP create").
		SetCode("tenant:modeldev-http:" + suffix).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error { return writer.Client().Role.DeleteOneID(currentRole.ID).Exec(c) })
	membership, err := writer.Client().UserRole.Create().SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).
		SetStatus(userrole.StatusActive).SetStartAt(time.Now().Add(-time.Hour)).SetEndAt(time.Now().Add(time.Hour)).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error {
		_, e := writer.Client().UserRole.Delete().Where(userrole.IDEQ(membership.ID)).Exec(c)
		return e
	})
	createPermission, err := writer.Client().Permission.Create().SetName("modeldev HTTP create").SetCode("modeldev.http.create." + suffix).SetStatus(permission.StatusOn).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error {
		return writer.Client().Permission.DeleteOneID(createPermission.ID).Exec(c)
	})
	createAPI, err := writer.Client().Api.Create().SetModule("ModelDevService").SetScope(api.ScopeAdmin).
		SetPath("/api/v1/modeldev/executions").SetMethod("POST").SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(sys)
	require.NoError(t, err, "MODELDEV_CREATE_PREFLIGHT: this exclusive database must have no conflicting route fixture")
	removeAfterTest(func(c context.Context) error { return writer.Client().Api.DeleteOneID(createAPI.ID).Exec(c) })
	permissionAPI, err := writer.Client().PermissionApi.Create().SetPermissionID(createPermission.ID).SetAPIID(createAPI.ID).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error {
		return writer.Client().PermissionApi.DeleteOneID(permissionAPI.ID).Exec(c)
	})
	grant, err := writer.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(createPermission.ID).
		SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
	require.NoError(t, err)
	removeAfterTest(func(c context.Context) error { return writer.Client().RolePermission.DeleteOneID(grant.ID).Exec(c) })

	keyBytes := make([]byte, 32)
	_, err = rand.Read(keyBytes)
	require.NoError(t, err)
	for _, name := range []string{"GWA_AUTH_JWT_PRIVATE_KEY", "GWA_AUTH_JWT_PUBLIC_KEY", "GWA_AUTH_JWT_KEY"} {
		t.Setenv(name, "")
	}
	bctx := testutil.NewBootstrapContext(&conf.Bootstrap{
		Authz: &conf.Authorization{Type: "casbin"},
		Authn: &conf.Authentication{Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(keyBytes)}},
	})
	redisAddress := os.Getenv("ANI_MODELDEV_REDIS_ADDR")
	host, port, addressErr := net.SplitHostPort(redisAddress)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if addressErr != nil || host != "127.0.0.1" || portErr != nil || portNumber == 0 {
		t.Fatal("MODELDEV_CREATE_PREFLIGHT: explicit task loopback Redis required")
	}
	rdb := redis.NewClient(&redis.Options{Addr: redisAddress})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	require.NoError(t, rdb.Ping(ctx).Err(), "MODELDEV_CREATE_PREFLIGHT: actual Redis")
	cache := data.NewUserTokenCache(bctx, rdb)
	authenticator := data.NewAuthenticator(bctx, cache)
	checker := data.NewTokenChecker(bctx, authenticator, authv1.ClientType_admin)
	tenantChecker := data.NewTenantAccessCheckerImpl(bctx, runtime)
	permissions := data.NewPermissionRepo(bctx, runtime, data.NewPermissionApiRepo(bctx, runtime), data.NewPermissionMenuRepo(bctx, runtime))
	roles := data.NewRoleRepo(bctx, runtime, data.NewRolePermissionRepo(bctx, runtime), data.NewRoleOrgUnitRepo(bctx, runtime), permissions,
		data.NewRoleMetadataRepo(bctx, runtime), data.NewRoleFieldPermissionRepo(bctx, runtime))
	policy := authorizer.NewAuthorizer(bctx, data.NewAuthorizerProvider(bctx, roles, data.NewApiRepo(bctx, runtime)))
	require.NotNil(t, policy.Engine())
	require.Equal(t, "casbin", policy.Engine().Name())
	require.NoError(t, policy.ResetPolicies(ctx))
	payload := &authv1.UserTokenPayload{UserId: operator.ID, TenantId: trans.Ptr(owner.ID), Roles: []string{*currentRole.Code}, DataScopes: []identityv1.DataScope{identityv1.DataScope_ALL}}
	token, _, err := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, payload)
	require.NoError(t, err, "MODELDEV_CREATE_PREFLIGHT: actual JWT and Redis session issuance")
	removeAfterTest(func(c context.Context) error {
		return cache.RevokeTokenByJti(c, authv1.ClientType_admin, operator.ID, payload.GetJti())
	})
	valid, verified := checker.IsValidAccessToken(ctx, token, false)
	require.True(t, valid)
	require.NotNil(t, verified)
	require.Equal(t, operator.ID, verified.GetUserId())
	require.Equal(t, owner.ID, verified.GetTenantId())
	require.NoError(t, tenantChecker.CheckTenantAccess(ctx, owner.ID, "/api/v1/modeldev/executions", "POST"))
	allowed, err := policy.Engine().IsAuthorized(ctx, authzEngine.Subject(*currentRole.Code), "POST", "/api/v1/modeldev/executions", authzEngine.Project(strconv.FormatUint(uint64(owner.ID), 10)))
	require.NoError(t, err)
	require.True(t, allowed, "MODELDEV_CREATE_PREFLIGHT: actual loaded Casbin policy")
	requestCtx := viewer.WithContext(auth.NewContext(ctx, verified), appViewer.NewUserViewer(uint64(operator.ID), uint64(owner.ID), 0, "", appViewer.BuildDataScopes(verified.GetDataScopes(), verified.GetDataScopeUnitIds(), verified.GetDataScope())))
	authorization := data.NewModelDevAuthorizationRepo(runtime)
	require.NoError(t, authorization.AuthorizeCreate(requestCtx, owner.ID, operator.ID), "MODELDEV_CREATE_PREFLIGHT: current DB authorization")
	principal, err := auth.PrincipalFromContext(requestCtx)
	require.NoError(t, err)
	actor, err := principal.Actor()
	require.NoError(t, err)

	resolver, closeResolver, err := data.NewModelDevClient(data.ModelDevClientConfig{Address: provider.Address, CAFile: provider.TLS.CAFile, CertFile: provider.TLS.CertFile, KeyFile: provider.TLS.KeyFile, Timeout: 3 * time.Second})
	require.NoError(t, err, "MODELDEV_CREATE_PREFLIGHT: actual typed ModelDev client")
	t.Cleanup(closeResolver)
	selection := testutil.ModelDevContractSelection(t, provider)
	preflight, err := resolver.ResolvePrepared(ctx, data.ModelDevResolveScope{ResourceTenantID: provider.Scope.ResourceTenantID, Actor: provider.Scope.Actor}, provider.Intent, provider.IdentityRef, selection, provider.AcceptedAt)
	require.NoError(t, err, "MODELDEV_CREATE_PREFLIGHT: actual ModelDev buildApp/mTLS/READY provider")
	wantCanonical, wantHash := testutil.ExpectedModelDevContractSnapshot(t)
	actualCanonical, err := preflight.Snapshot.Canonical()
	require.NoError(t, err)
	require.Equal(t, wantCanonical, actualCanonical)
	require.Equal(t, wantHash, preflight.ExecutionSpecHash)
	computeProvider := &modelDevHTTPComputeFixture{selection: selection}
	removeAfterTest(func(c context.Context) error {
		_, e := writer.Client().ModelDevAcceptance.Delete().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Exec(c)
		return e
	})
	acceptances := data.NewModelDevAcceptanceRepo(runtime)
	bff := service.NewModelDevService(authorization, data.NewTenantRepo(bctx, runtime), acceptances, resolver, computeProvider)
	server := khttp.NewServer(khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(tenantChecker), auth.WithInjectMetadata(false), auth.WithInjectEnt(true)), authzMiddleware.Server(policy.Engine())))
	registerModelDevHTTP(server, bff)
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	post := func(intent cpup01.Intent, key string, retiredFields ...map[string]any) (int, []byte) {
		t.Helper()
		raw, marshalErr := json.Marshal(intent)
		require.NoError(t, marshalErr)
		keyJSON, marshalErr := json.Marshal(key)
		require.NoError(t, marshalErr)
		body := append(append(append(bytes.Clone(raw[:len(raw)-1]), []byte(`,"idempotency_key":`)...), keyJSON...), '}')
		if len(retiredFields) != 0 {
			legacy, err := json.Marshal(retiredFields[0])
			require.NoError(t, err)
			body = append(append(body[:len(body)-1], ','), legacy[1:]...)
		}
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, web.URL+"/api/v1/modeldev/executions", bytes.NewReader(body))
		require.NoError(t, requestErr)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		response, requestErr := web.Client().Do(request)
		require.NoError(t, requestErr)
		defer response.Body.Close()
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 16385))
		require.NoError(t, readErr)
		require.LessOrEqual(t, len(responseBody), 16384)
		return response.StatusCode, responseBody
	}
	observer := openModelDevHTTPPG(t, os.Getenv("ANI_TEST_DATABASE_DSN"))
	t.Log("MODELDEV_CREATE_PREFLIGHT PASS: actual JWT/Redis, TenantAccess/Casbin/current DB, runtime role, official TRAINING selection and real ModelDev provider")
	key := uuid.NewString()
	before := time.Now().UTC()
	statusCode, responseBody := post(provider.Intent, key)
	after := time.Now().UTC()
	if statusCode == http.StatusNotImplemented {
		var failure struct {
			Reason string `json:"reason"`
		}
		require.NoError(t, json.Unmarshal(responseBody, &failure))
		require.Equal(t, "MODELDEV_CREATE_NOT_IMPLEMENTED", failure.Reason, "unexpected 501 is not the planned service RED")
		count, readErr := observer.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Count(sys)
		require.NoError(t, readErr)
		require.Zero(t, count, "the unimplemented Create must not leave an acceptance")
		t.Fatal("MODELDEV_CREATE_BEHAVIOR: service not implemented after all real preflight checks PASS")
	}
	require.Equal(t, http.StatusAccepted, statusCode)
	var first modeldevv1.CreateExecutionResponse
	require.NoError(t, protojson.Unmarshal(responseBody, &first))
	operationID, err := uuid.Parse(first.OperationId)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, operationID)
	executionID, err := uuid.Parse(first.ExecutionId)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, executionID)
	require.NotEqual(t, operationID, executionID)
	require.False(t, first.Replayed)
	require.Equal(t, "ACCEPTED", first.ComputeState)
	require.Equal(t, "PENDING", first.DeliveryState)
	require.Equal(t, "OPEN", first.CloseState)
	require.Equal(t, "NOT_APPLICABLE", first.ResourceState)

	stored, err := observer.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Only(sys)
	require.NoError(t, err, "a third connection must observe durable acceptance before HTTP202 is trusted")
	require.Equal(t, first.OperationId, stored.OperationID)
	require.Equal(t, first.ExecutionId, stored.ExecutionID)
	require.Equal(t, provider.Scope.ResourceTenantID, stored.ResourceTenantID)
	require.Equal(t, actor, stored.Actor)
	require.Equal(t, data.ModelDevCreateAction, stored.Action)
	require.Equal(t, key, stored.IdempotencyKey)
	require.Equal(t, "QUEUED", string(stored.DispatchState))
	require.False(t, stored.AcceptedAt.Before(before.Truncate(time.Microsecond)))
	require.False(t, stored.AcceptedAt.After(after))
	intentCanonical, intentHash, err := cpup01.CanonicalIntent(provider.Intent)
	require.NoError(t, err)
	require.Equal(t, intentCanonical, stored.IntentCanonical)
	require.Equal(t, intentHash, stored.IntentHash)
	expectedBytes, expectedHash := testutil.ExpectedModelDevContractSnapshotAt(t, cpup01.IdentityRef{
		TenantID: provider.Scope.ResourceTenantID, Actor: actor, OperationID: stored.OperationID, ExecutionID: stored.ExecutionID, IntentHash: stored.IntentHash,
	}, stored.AcceptedAt)
	require.Equal(t, expectedBytes, stored.SnapshotCanonical)
	require.Equal(t, expectedHash, stored.ExecutionSpecHash)
	require.Equal(t, 1, computeProvider.calls)

	statusCode, _ = post(provider.Intent, uuid.NewString(), map[string]any{"source_execution_id": uuid.NewString()})
	require.Equal(t, http.StatusBadRequest, statusCode, "the retired source-execution request must be rejected")
	count, readErr := observer.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Count(sys)
	require.NoError(t, readErr)
	require.Equal(t, 1, count, "retired fields must not leave another acceptance")

	// The original key must resolve from persisted facts even after the current
	// external Res catalogue is unavailable. No Release/generation gate exists.
	computeProvider.unavailable = true
	statusCode, responseBody = post(provider.Intent, key)
	require.Equal(t, http.StatusAccepted, statusCode, "current catalogue failure must not block an authorized original-key replay")
	var replay modeldevv1.CreateExecutionResponse
	require.NoError(t, protojson.Unmarshal(responseBody, &replay))
	require.True(t, replay.Replayed)
	replay.Replayed = false
	require.True(t, proto.Equal(&first, &replay), "replay must return the complete original receipt")
	changed := provider.Intent
	changed.Name = "different-intent"
	statusCode, _ = post(changed, key)
	require.Equal(t, http.StatusConflict, statusCode)

	// ClaimDelivery scans the managed queue globally. Require this exclusive
	// fixture to own its only row before advancing it through the real repo.
	count, readErr = observer.Client().ModelDevAcceptance.Query().Count(sys)
	require.NoError(t, readErr)
	require.Equal(t, 1, count, "delivery projection fixture must not claim another acceptance")
	claim, err := acceptances.ClaimDelivery(sys, "http-projection-"+suffix, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, owner.ID, claim.Acceptance.Scope.TenantID)
	require.Equal(t, stored.OperationID, claim.Acceptance.OperationID)
	deferred, err := acceptances.DeferDelivery(sys, claim, data.ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"})
	require.NoError(t, err)
	require.True(t, deferred)
	unknown, err := observer.Client().ModelDevAcceptance.Query().Where(
		modeldevacceptance.TenantIDEQ(owner.ID), modeldevacceptance.OperationIDEQ(stored.OperationID),
	).Only(sys)
	require.NoError(t, err)
	require.Equal(t, modeldevacceptance.DispatchStateUNKNOWN, unknown.DispatchState)
	require.Empty(t, unknown.OwnerReceiptCanonical)
	statusCode, responseBody = post(provider.Intent, key)
	require.Equal(t, http.StatusAccepted, statusCode, "UNKNOWN must preserve the accepted original-key response")
	var unknownReply modeldevv1.CreateExecutionResponse
	require.NoError(t, protojson.Unmarshal(responseBody, &unknownReply))
	wantProjection := &modeldevv1.CreateExecutionResponse{
		OperationId: stored.OperationID, ExecutionId: stored.ExecutionID,
		Replayed:     true,
		ComputeState: "ACCEPTED", DeliveryState: "PENDING", ResourceState: "NOT_APPLICABLE", CloseState: "OPEN",
	}
	require.True(t, proto.Equal(wantProjection, &unknownReply), "UNKNOWN exposes original acceptance knowledge, not an invented owner ACK")
	count, readErr = observer.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Count(sys)
	require.NoError(t, readErr)
	require.Equal(t, 1, count, "UNKNOWN replay must not create another acceptance")

	// Observe the real database backoff through ClaimDelivery; do not alter the
	// stored retry time or dispatch state to make this projection test advance.
	claimContext, cancelClaim := context.WithTimeout(sys, 3*time.Second)
	defer cancelClaim()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		claim, err = acceptances.ClaimDelivery(claimContext, "http-projection-"+suffix, time.Minute)
		require.NoError(t, err)
		if claim != nil {
			break
		}
		select {
		case <-claimContext.Done():
			t.Fatal("delivery projection fixture did not reclaim its original within three seconds")
		case <-poll.C:
		}
	}
	poll.Stop()
	cancelClaim()
	require.Equal(t, owner.ID, claim.Acceptance.Scope.TenantID)
	require.Equal(t, stored.OperationID, claim.Acceptance.OperationID)
	// This authored typed receipt is a BFF persistence/projection fixture. It is
	// not an actual owner ACK or proof of training; the Resolve provider retains
	// its zero-execution assertion and receives no AcceptExecution from here.
	ownerReceipt := data.ModelDevOwnerReceipt{
		OperationID: stored.OperationID, ExecutionID: stored.ExecutionID, ExecutionSpecHash: stored.ExecutionSpecHash,
		ComputeState: "SUCCEEDED", DeliveryState: "PUBLISHED", ResourceState: "NOT_APPLICABLE", CloseState: "CLOSED",
		Revision: 37, Replayed: false,
	}
	acked, err := acceptances.AckDelivery(sys, claim, ownerReceipt)
	require.NoError(t, err)
	require.True(t, acked)
	durable, err := data.NewModelDevAcceptanceRepo(observer).FindAccepted(sys, claim.Acceptance.Scope, provider.Intent)
	require.NoError(t, err)
	require.NotNil(t, durable)
	require.Equal(t, "ACKED", durable.DispatchState)
	require.Equal(t, &ownerReceipt, durable.OwnerReceipt, "independent connection must recover the complete persisted projection input")
	statusCode, responseBody = post(provider.Intent, key)
	require.Equal(t, http.StatusAccepted, statusCode, "ACKED must preserve authorized original-key replay")
	var ackedReply modeldevv1.CreateExecutionResponse
	require.NoError(t, protojson.Unmarshal(responseBody, &ackedReply))
	wantProjection.ComputeState, wantProjection.DeliveryState = ownerReceipt.ComputeState, ownerReceipt.DeliveryState
	wantProjection.ResourceState, wantProjection.CloseState = ownerReceipt.ResourceState, ownerReceipt.CloseState
	require.True(t, proto.Equal(wantProjection, &ackedReply), "ACKED must project all four persisted owner states and original identity")
	beforeRevocation, err := observer.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Only(sys)
	require.NoError(t, err, "ACKED replay must leave exactly one original acceptance")
	require.Equal(t, modeldevacceptance.DispatchStateACKED, beforeRevocation.DispatchState)
	require.NotEmpty(t, beforeRevocation.OwnerReceiptCanonical)
	t.Log("MODELDEV_CREATE_PROJECTION PASS: real HTTP/JWT replay of persisted UNKNOWN and ACKED; authored receipt fixture only, not owner delivery or training")

	require.NoError(t, writer.Client().UserRole.DeleteOneID(membership.ID).Exec(sys))
	valid, _ = checker.IsValidAccessToken(ctx, token, false)
	require.True(t, valid, "the old JWT session remains valid while its current DB membership is revoked")
	allowed, err = policy.Engine().IsAuthorized(ctx, authzEngine.Subject(*currentRole.Code), "POST", "/api/v1/modeldev/executions", authzEngine.Project(strconv.FormatUint(uint64(owner.ID), 10)))
	require.NoError(t, err)
	require.True(t, allowed, "the unchanged Casbin cache must not substitute for current DB authorization")
	statusCode, _ = post(provider.Intent, key)
	require.Equal(t, http.StatusForbidden, statusCode, "revocation must deny even the original accepted key")
	unchanged, err := observer.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Only(sys)
	require.NoError(t, err)
	require.Equal(t, stored.OperationID, unchanged.OperationID)
	require.Equal(t, stored.ExecutionID, unchanged.ExecutionID)
	require.Equal(t, stored.IntentCanonical, unchanged.IntentCanonical)
	require.Equal(t, stored.SnapshotCanonical, unchanged.SnapshotCanonical)
	require.Equal(t, stored.IntentHash, unchanged.IntentHash)
	require.Equal(t, stored.ExecutionSpecHash, unchanged.ExecutionSpecHash)
	require.True(t, stored.AcceptedAt.Equal(unchanged.AcceptedAt))
	require.Equal(t, beforeRevocation.DispatchState, unchanged.DispatchState)
	require.Equal(t, beforeRevocation.OwnerReceiptCanonical, unchanged.OwnerReceiptCanonical)
}

func prepareModelDevHTTPDatabase(t *testing.T, ctx context.Context) (*entCrud.EntClient[*ent.Client], *entCrud.EntClient[*ent.Client]) {
	t.Helper()
	runtimeDSN := os.Getenv("ANI_TEST_DATABASE_DSN")
	file := os.Getenv("ANI_MODELDEV_FIXTURE_DSN_FILE")
	if runtimeDSN == "" || os.Getenv("ANI_TEST_DATABASE_EXCLUSIVE") != "1" || !filepath.IsAbs(file) {
		t.Fatal("MODELDEV_CREATE_PREFLIGHT: explicit exclusive PG and private fixture identity required")
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		t.Fatal("MODELDEV_CREATE_PREFLIGHT: bounded private fixture DSN file required")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal("MODELDEV_CREATE_PREFLIGHT: cannot read fixture identity")
	}
	fixtureDSN := strings.TrimSpace(string(raw))
	runtimeConfig, runtimeErr := pgx.ParseConfig(runtimeDSN)
	fixtureConfig, fixtureErr := pgx.ParseConfig(fixtureDSN)
	if runtimeErr != nil || fixtureErr != nil || runtimeConfig.Host != fixtureConfig.Host || runtimeConfig.Port != fixtureConfig.Port || runtimeConfig.Database != fixtureConfig.Database || runtimeConfig.User == fixtureConfig.User {
		t.Fatal("MODELDEV_CREATE_PREFLIGHT: runtime/fixture must be separate roles on the same task database")
	}
	runtime, writer := openModelDevHTTPPG(t, runtimeDSN), openModelDevHTTPPG(t, fixtureDSN)
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

func openModelDevHTTPPG(t *testing.T, dsn string) *entCrud.EntClient[*ent.Client] {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("MODELDEV_CREATE_PREFLIGHT: PG client configuration failed")
	}
	driver := entsql.OpenDB("postgres", db)
	client := ent.NewClient(ent.Driver(driver))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("MODELDEV_CREATE_PREFLIGHT: PG unavailable")
	}
	return entCrud.NewEntClient(client, driver)
}

// Only the external Resource RPC boundary is a fixture. Admission authorization,
// Prepare/Finalize, persistence and HTTP/session handling remain real.
type modelDevHTTPComputeFixture struct {
	selection   *compute.ComputeSelection
	unavailable bool
	calls       int
}

func (f *modelDevHTTPComputeFixture) ResolveComputePreset(ctx context.Context, tenant, actor string, request *compute.ResolveComputePresetRequest) (*compute.ComputeSelection, error) {
	f.calls++
	if f.unavailable {
		return nil, status.Error(codes.Unavailable, "external Res fixture unavailable")
	}
	if request == nil || request.TenantId != tenant || actor == "" || request.Usage != compute.Usage_TRAINING || request.PresetId != f.selection.PresetId {
		return nil, status.Error(codes.InvalidArgument, "invalid official TRAINING selection request")
	}
	return proto.Clone(f.selection).(*compute.ComputeSelection), ctx.Err()
}
