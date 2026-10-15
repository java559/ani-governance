//go:build gpu_joint

package gpucontract

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	quota "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/gpuusagesync"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"go-wind-admin/app/admin/service/internal/data/ent/quotareleasereceipt"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/internal/server"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/app/admin/service/tests/testutil"
	"go-wind-admin/pkg/authorizer"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	authzMiddleware "go-wind-admin/pkg/localdeps/kratos-authz/middleware"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// TestActualInferenceOwnerWiring uses the real BFF, current Ent authorization,
// Accelerator resolver, quota transaction, compiled adapter, Inference RPC/PG
// and refund adapter. The Inference test process deliberately does not execute
// model/Kubernetes work. Completion notifications below are explicit fixtures;
// no deletion/physical release conclusion follows from their acceptance.
func TestActualInferenceOwnerWiring(t *testing.T) {
	jointDir := os.Getenv("GOV_ACC_JOINT_DIR")
	require.NotEmpty(t, jointDir, "isolated existing Acc/Gov contract environment is required")
	require.True(t, data.InferenceModuleRegistered(), "registered Inference module authority is a prerequisite; never borrow Accelerator rights")
	binary := os.Getenv("INFERENCE_WIRING_TEST_BINARY")
	require.NotEmpty(t, binary)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ready := filepath.Join(jointDir, "inference-ready")
	process, err := startActualInferenceOwner(ctx, binary, jointDir, ready, "initial")
	require.NoError(t, err)
	var ownerMu sync.Mutex
	firstOwnerPID := process.cmd.Process.Pid
	var restartedOwnerPID int
	ownerProcessRestarted := false
	t.Cleanup(func() { ownerMu.Lock(); defer ownerMu.Unlock(); process.stop() })
	clientConfig, err := readConfig[data.InferenceClientConfig]("inference-client-config.json")
	require.NoError(t, err)
	actualAddress, err := os.ReadFile(ready)
	require.NoError(t, err)
	clientConfig.Address = string(actualAddress)
	ownerClient, closeOwner, err := data.NewInferenceClient(clientConfig)
	require.NoError(t, err)
	defer closeOwner()
	govConfig, err := readConfig[testGovConfig]("governance-config.json")
	require.NoError(t, err)
	accelerator, closeAcc, err := data.NewAcceleratorClient(govConfig.Accelerator)
	require.NoError(t, err)
	defer closeAcc()
	ledger, entClient := jointLedger(t)
	bootstrapContext := bootstrap.NewContextWithParam(ctx, nil, nil, bLogger.NopLogger())
	registry := service.NewQuotaAdapterRegistry()
	lostAckClient := &actualInferenceLostAckClient{actual: ownerClient, afterFirstAck: func() error {
		ownerMu.Lock()
		defer ownerMu.Unlock()
		// The owner has already committed CREATE and returned its real ACK.
		// Restart the actual receiver on the same managed address/PG/identity
		// before losing the response and retrying the original command.
		process.stop()
		restarted, err := startActualInferenceOwner(ctx, binary, jointDir, ready, "restarted")
		if err != nil {
			return err
		}
		process = restarted
		address, err := os.ReadFile(ready)
		if err != nil {
			return err
		}
		if string(address) != clientConfig.Address {
			return fmt.Errorf("restarted actual owner changed its fixed managed address")
		}
		restartedOwnerPID = process.cmd.Process.Pid
		ownerProcessRestarted = firstOwnerPID != restartedOwnerPID
		if !ownerProcessRestarted {
			return fmt.Errorf("actual owner restart did not change process PID")
		}
		return nil
	}}
	t.Cleanup(func() {
		lostAckClient.mu.Lock()
		defer lostAckClient.mu.Unlock()
		t.Logf("actual owner CREATE attempts=%d response_lost=%t first_rpc_error=%v last_rpc_error=%v", lostAckClient.createCalls, lostAckClient.droppedAck, lostAckClient.firstCreateError, lostAckClient.lastCreateError)
	})
	binding := service.NewInferenceGpuBinding(lostAckClient, data.NewInferenceAuthorizationRepo(entClient))
	require.NoError(t, registry.Register(binding))
	worker := service.NewQuotaDispatchWorker(bootstrapContext, ledger, registry)
	acceptance, err := service.NewGpuAcceptance(ledger, registry, data.NewTenantRepo(bootstrapContext, entClient), accelerator.Catalog, binding, worker)
	require.NoError(t, err)
	bff := service.NewInferenceService(acceptance)
	seed, err := readConfig[struct {
		Request *inferencev1.GpuRequest `json:"request"`
	}]("seed.json")
	require.NoError(t, err)
	require.NotNil(t, seed.Request)
	seed.Request.ContainerName = "kserve-container"
	seed.Request.Replicas = 1
	fixtureName := "inference-wiring-" + uuid.NewString()
	input := &view.CreateInferenceRequest{Data: &view.CreateInferenceData{IdempotencyKey: uuid.NewString(), Name: fixtureName, ModelVersionId: "30000000-0000-4000-8000-000000000001", ServedModelName: fixtureName, EngineType: "vllm", EngineImage: "fixture.invalid/held-model:software-only", Command: "owner-supplied-engine", Cpu: "1", Memory: "1Gi", Replicas: int32(seed.Request.Replicas), RuntimeMode: inferencev1.RuntimeMode_RUNTIME_MODE_DEPLOYMENT, Gpu: &view.GpuSelection{ClusterId: seed.Request.ClusterId, PoolId: seed.Request.PoolId, ProfileId: seed.Request.ProfileId, ProfileVersion: seed.Request.ProfileVersion}}}
	requestCtx := viewer.WithContext(ctx, appViewer.NewUserViewer(1, 1, 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	principalCtx := auth.NewPrincipalContext(requestCtx, &auth.Principal{Type: auth.SubjectUser, ID: 1, TenantID: 1})
	fixtureCtx := appViewer.NewSystemViewerContext(ctx)
	operator, err := entClient.Client().User.Query().Where(user.IDEQ(1)).Only(fixtureCtx)
	if err != nil {
		operator, err = entClient.Client().User.Create().SetID(1).SetTenantID(1).SetUsername(fixtureName).SetStatus(user.StatusNormal).Save(fixtureCtx)
	}
	require.NoError(t, err)
	require.Equal(t, uint32(1), operator.ID)
	_, err = bff.CreateInference(principalCtx, input)
	require.Error(t, err, "current Inference rights are required even with valid Accelerator grant")
	module, err := entClient.Client().PlanModule.Create().SetPlanID(1).SetModule(planmodule.Module("INFERENCE")).Save(fixtureCtx)
	require.NoError(t, err)
	currentRole, err := entClient.Client().Role.Create().SetTenantID(1).SetName(fixtureName).SetCode("tenant:inference-wiring:" + fixtureName).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(fixtureCtx)
	require.NoError(t, err)
	_, err = entClient.Client().UserRole.Create().SetTenantID(1).SetUserID(1).SetRoleID(currentRole.ID).SetStatus(userrole.StatusActive).Save(fixtureCtx)
	require.NoError(t, err)
	var createGrantID, deleteGrantID uint32
	for _, path := range []string{data.InferenceCreatePath, data.InferenceDeletePath} {
		p, err := entClient.Client().Permission.Create().SetName(fixtureName + path).SetCode(fixtureName + path).SetStatus(permission.StatusOn).Save(fixtureCtx)
		require.NoError(t, err)
		apiRow, err := entClient.Client().Api.Create().SetModule("InferenceService").SetPath(path).SetMethod("POST").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModule("INFERENCE")).SetStatus(api.StatusOn).Save(fixtureCtx)
		require.NoError(t, err)
		_, err = entClient.Client().PermissionApi.Create().SetPermissionID(p.ID).SetAPIID(apiRow.ID).Save(fixtureCtx)
		require.NoError(t, err)
		grant, err := entClient.Client().RolePermission.Create().SetTenantID(1).SetRoleID(currentRole.ID).SetPermissionID(p.ID).SetStatus(rolepermission.StatusOff).SetEffect(rolepermission.EffectAllow).Save(fixtureCtx)
		require.NoError(t, err)
		if path == data.InferenceCreatePath {
			createGrantID = grant.ID
		} else {
			deleteGrantID = grant.ID
		}
	}
	// Actual signed JWT + Redis session, tenant/plan gate and loaded Casbin
	// policy protect the same strict public routes used by production.
	keyBytes := make([]byte, 32)
	_, err = rand.Read(keyBytes)
	require.NoError(t, err)
	for _, name := range []string{"GWA_AUTH_JWT_PRIVATE_KEY", "GWA_AUTH_JWT_PUBLIC_KEY", "GWA_AUTH_JWT_KEY"} {
		t.Setenv(name, "")
	}
	authContext := testutil.NewBootstrapContext(&conf.Bootstrap{Authz: &conf.Authorization{Type: "casbin"}, Authn: &conf.Authentication{Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(keyBytes)}}})
	redisAddress := os.Getenv("ANI_INFERENCE_REDIS_ADDR")
	redisHost, redisPort, addressErr := net.SplitHostPort(redisAddress)
	portNumber, portErr := strconv.ParseUint(redisPort, 10, 16)
	require.NoError(t, addressErr)
	require.NoError(t, portErr)
	require.Equal(t, "127.0.0.1", redisHost)
	require.NotZero(t, portNumber)
	rdb := redis.NewClient(&redis.Options{Addr: redisAddress})
	defer rdb.Close()
	require.NoError(t, rdb.Ping(ctx).Err())
	cache := data.NewUserTokenCache(authContext, rdb)
	authenticator := data.NewAuthenticator(authContext, cache)
	checker := data.NewTokenChecker(authContext, authenticator, authv1.ClientType_admin)
	tenantChecker := data.NewTenantAccessCheckerImpl(authContext, entClient)
	permissions := data.NewPermissionRepo(authContext, entClient, data.NewPermissionApiRepo(authContext, entClient), data.NewPermissionMenuRepo(authContext, entClient))
	roles := data.NewRoleRepo(authContext, entClient, data.NewRolePermissionRepo(authContext, entClient), data.NewRoleOrgUnitRepo(authContext, entClient), permissions, data.NewRoleMetadataRepo(authContext, entClient), data.NewRoleFieldPermissionRepo(authContext, entClient))
	policy := authorizer.NewAuthorizer(authContext, data.NewAuthorizerProvider(authContext, roles, data.NewApiRepo(authContext, entClient)))
	require.NotNil(t, policy.Engine())
	require.Equal(t, "casbin", policy.Engine().Name())
	require.NoError(t, policy.ResetPolicies(ctx))
	payload := &authv1.UserTokenPayload{UserId: 1, TenantId: trans.Ptr(uint32(1)), Roles: []string{*currentRole.Code}, DataScopes: []identityv1.DataScope{identityv1.DataScope_ALL}}
	token, _, err := authenticator.CreateUserToken(ctx, authv1.ClientType_admin, payload)
	require.NoError(t, err)
	defer cache.RevokeTokenByJti(context.Background(), authv1.ClientType_admin, 1, payload.GetJti())
	publicServer := khttp.NewServer(khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(tenantChecker), auth.WithInjectMetadata(false), auth.WithInjectEnt(true)), authzMiddleware.Server(policy.Engine())))
	server.RegisterInferenceHTTPServer(publicServer, bff)
	web := httptest.NewServer(publicServer)
	defer web.Close()
	post := func(path string, message proto.Message, accessToken string) (int, *view.InferenceAcceptance) {
		t.Helper()
		raw, err := protojson.Marshal(message)
		require.NoError(t, err)
		request, err := nethttp.NewRequestWithContext(ctx, "POST", web.URL+path, bytes.NewReader(raw))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		if accessToken != "" {
			request.Header.Set("Authorization", "Bearer "+accessToken)
		}
		response, err := web.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 16385))
		require.NoError(t, err)
		require.LessOrEqual(t, len(body), 16384)
		if response.StatusCode != nethttp.StatusAccepted {
			return response.StatusCode, nil
		}
		accepted := new(view.InferenceAcceptance)
		require.NoError(t, protojson.Unmarshal(body, accepted))
		return response.StatusCode, accepted
	}
	statusCode, _ := post(data.InferenceCreatePath, input, "")
	require.Equal(t, nethttp.StatusUnauthorized, statusCode)
	statusCode, _ = post(data.InferenceCreatePath, input, token)
	require.Equal(t, nethttp.StatusForbidden, statusCode, "real signed user without route grant must be denied")
	_, err = entClient.Client().RolePermission.UpdateOneID(createGrantID).SetStatus(rolepermission.StatusOn).Save(fixtureCtx)
	require.NoError(t, err)
	require.NoError(t, policy.ResetPolicies(ctx))
	statusCode, accepted := post(data.InferenceCreatePath, input, token)
	require.Equal(t, nethttp.StatusAccepted, statusCode)
	require.NotNil(t, accepted)
	require.Equal(t, "ACCEPTED", accepted.Result)
	charges, err := ledger.GetChargesForOperation(ctx, 1, accepted.OperationId)
	require.NoError(t, err)
	require.Len(t, charges, 1)
	require.Equal(t, 6144*int64(seed.Request.Replicas), charges[0].OriginalUnits)
	// Real worker delivery verifies the real owner durable ACK before ACKED.
	require.NoError(t, worker.Start(ctx))
	defer worker.Stop(context.Background())
	waitJoint(t, "actual Inference PG durable ACK after process restart and lost response", func() bool {
		lostAckClient.mu.Lock()
		restartErr := lostAckClient.afterFirstAckError
		lostAckClient.mu.Unlock()
		require.NoError(t, restartErr, "actual owner process restart failed")
		op, err := ledger.GetOperationForUser(ctx, 1, accepted.OperationId)
		return err == nil && op.DispatchState == quotaoperation.DispatchStateAcked && op.AttemptCount >= 2
	})
	lostAckClient.mu.Lock()
	createCalls, droppedAck, firstOperationID, firstResourceID := lostAckClient.createCalls, lostAckClient.droppedAck, lostAckClient.firstOperationID, lostAckClient.firstResourceID
	lostAckClient.mu.Unlock()
	require.True(t, droppedAck, "the first actual owner durable ACK must have been lost at the response boundary")
	require.GreaterOrEqual(t, createCalls, 2, "retry must invoke the actual Inference RPC again")
	require.Equal(t, accepted.OperationId, firstOperationID)
	require.Equal(t, accepted.ResourceId, firstResourceID)
	ownerMu.Lock()
	wasRestarted, secondOwnerPID := ownerProcessRestarted, restartedOwnerPID
	ownerMu.Unlock()
	require.True(t, wasRestarted)
	require.NotEqual(t, firstOwnerPID, secondOwnerPID)
	afterRetry, err := ledger.GetChargesForOperation(ctx, 1, accepted.OperationId)
	require.NoError(t, err)
	require.Len(t, afterRetry, 1)
	require.Equal(t, charges[0].ChargeID, afterRetry[0].ChargeID)
	require.Equal(t, charges[0].OriginalUnits, afterRetry[0].OriginalUnits)
	// Read back the accepted owner PG record and run the actual production
	// renderer. This external subprocess imports only its own repository's
	// runtime/storage packages; no cross-repository internal import or K8s call.
	projectionFile := filepath.Join(jointDir, "actual-inference-projection.json")
	inspect := exec.CommandContext(ctx, binary, "-test.run=^TestInspectManagedOwnerProjection$", "-test.v")
	inspect.Env = append(os.Environ(), "INFERENCE_PROJECTION_TENANT=11111111-1111-4111-8111-111111111111", "INFERENCE_PROJECTION_RESOURCE="+accepted.ResourceId, "INFERENCE_PROJECTION_RESULT_FILE="+projectionFile)
	inspectOutput, err := inspect.CombinedOutput()
	require.NoError(t, err, "actual PG-to-render projection: %s", inspectOutput)
	var projection struct {
		Runtime struct {
			TenantID, ServiceID, Image, ModelVersionID, RuntimeMode string
			Generation                                              int64
			Replicas                                                int32
			ManagedGPU                                              bool
			GPUPlan                                                 json.RawMessage
			CommandArgv                                             []string
		}
		OriginalContext struct {
			TenantID, ResourceID, OriginalCreateOperationID string
			Charges                                         []struct {
				ChargeID      string `json:"charge_id"`
				QuotaCode     string `json:"quota_code"`
				OriginalUnits int64  `json:"original_units"`
			}
		} `json:"original_context"`
		Object map[string]any
	}
	projectionJSON, err := os.ReadFile(projectionFile)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(projectionJSON, &projection))
	require.True(t, projection.Runtime.ManagedGPU)
	require.Equal(t, accepted.ResourceId, projection.Runtime.ServiceID)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", projection.Runtime.TenantID)
	require.Equal(t, int64(1), projection.Runtime.Generation)
	require.Equal(t, int32(1), projection.Runtime.Replicas)
	require.Equal(t, input.Data.Engine.Image, projection.Runtime.Image)
	require.Equal(t, input.Data.Engine.Command, projection.Runtime.CommandArgv)
	require.Equal(t, input.Data.ModelVersionId, projection.Runtime.ModelVersionID)
	require.Equal(t, accepted.OperationId, projection.OriginalContext.OriginalCreateOperationID)
	require.Equal(t, accepted.ResourceId, projection.OriginalContext.ResourceID)
	require.Equal(t, projection.Runtime.TenantID, projection.OriginalContext.TenantID)
	require.Len(t, projection.OriginalContext.Charges, len(charges))
	require.Equal(t, charges[0].ChargeID, projection.OriginalContext.Charges[0].ChargeID)
	require.Equal(t, charges[0].QuotaCode, projection.OriginalContext.Charges[0].QuotaCode)
	require.Equal(t, charges[0].OriginalUnits, projection.OriginalContext.Charges[0].OriginalUnits)
	storedOperation, err := ledger.GetOperationForUser(ctx, 1, accepted.OperationId)
	require.NoError(t, err)
	frozen, err := service.DecodeGpuCanonical([]byte(storedOperation.CanonicalRequest))
	require.NoError(t, err)
	var projectedPlan acc.ResolvedGpuPlan
	require.NoError(t, json.Unmarshal(projection.Runtime.GPUPlan, &projectedPlan))
	require.True(t, proto.Equal(frozen.GpuPlan, &projectedPlan), "the actual owner PG/runtime plan must equal Gov's original resolved plan")
	require.Equal(t, "InferenceService", projection.Object["kind"])
	predictor := projection.Object["spec"].(map[string]any)["predictor"].(map[string]any)
	require.Equal(t, "volcano", predictor["schedulerName"])
	require.Equal(t, frozen.GpuPlan.Runtime.SchedulerName, predictor["schedulerName"])
	require.Equal(t, float64(1), predictor["minReplicas"])
	require.Equal(t, float64(1), predictor["maxReplicas"])
	selector := predictor["nodeSelector"].(map[string]any)
	require.Len(t, selector, len(frozen.GpuPlan.Runtime.NodeLabels))
	for _, label := range frozen.GpuPlan.Runtime.NodeLabels {
		require.Equal(t, label.Value, selector[label.Key])
	}
	annotations := predictor["annotations"].(map[string]any)
	for _, annotation := range frozen.GpuPlan.Runtime.PodAnnotations {
		require.Equal(t, annotation.Value, annotations[annotation.Key])
	}
	require.Equal(t, "hami-core", annotations["volcano.sh/vgpu-mode"])
	require.Equal(t, frozen.GpuPlan.Runtime.QueueName, annotations["scheduling.volcano.sh/queue-name"])
	containers := predictor["containers"].([]any)
	require.Len(t, containers, 1)
	container := containers[0].(map[string]any)
	require.Equal(t, "kserve-container", container["name"])
	require.Equal(t, input.Data.Engine.Image, container["image"])
	limits := container["resources"].(map[string]any)["limits"].(map[string]any)
	for _, limit := range frozen.GpuPlan.Runtime.LimitsPerContainer {
		require.Equal(t, limit.Value, limits[limit.Key])
	}
	require.Equal(t, "2", limits["cpu"])
	require.Equal(t, "2Gi", limits["memory"])
	require.Equal(t, strconv.FormatInt(frozen.GpuPlan.Encoding.MemoryBlocksPerDevice, 10), limits["volcano.sh/vgpu-memory"])
	// A01 uses the real Sync client and its real persisted acknowledgement.
	// Public GetGpuUsage also reads back the actual Acc projection; Sync remains
	// a derived side path and performs no binding deletion or owner execution.
	syncWorker := service.NewGpuUsageSyncWorker(ledger, accelerator, nil)
	usageRef := &acc.GpuUsageRef{TenantId: storedOperation.ResourceTenantID, OwnerService: storedOperation.OwnerService, ResourceId: accepted.ResourceId, CreateOperationId: accepted.OperationId}
	assertUsageAck := func(revision uint64, state acc.UsageState) {
		t.Helper()
		syncRow, err := entClient.Client().GpuUsageSync.Query().Where(gpuusagesync.TenantIDEQ(1), gpuusagesync.OperationIDEQ(accepted.OperationId)).Only(fixtureCtx)
		require.NoError(t, err)
		require.Equal(t, int64(revision), syncRow.Revision)
		require.Equal(t, int64(revision), syncRow.AckedRevision)
		require.Equal(t, state.String(), syncRow.State)
		require.Equal(t, usageRef.TenantId, syncRow.ResourceTenantID)
		require.Equal(t, usageRef.ResourceId, syncRow.ResourceID)
		require.Equal(t, usageRef.OwnerService, syncRow.OwnerService)
		actualUsage, err := accelerator.Usage.GetGpuUsage(ctx, &acc.GetGpuUsageRequest{Context: &acc.TenantContext{RequestId: uuid.NewString(), TenantId: usageRef.TenantId, Actor: &acc.Actor{Type: "user", Id: "1"}}, Ref: usageRef})
		require.NoError(t, err)
		require.NotNil(t, actualUsage)
		require.True(t, proto.Equal(usageRef, actualUsage.Ref))
		require.Equal(t, revision, actualUsage.Revision)
		require.Equal(t, state, actualUsage.State)
		require.True(t, proto.Equal(frozen.GpuPlan, actualUsage.Plan), "Sync must retain the original immutable plan")
	}
	require.NoError(t, syncWorker.Step(ctx))
	assertUsageAck(1, acc.UsageState_DECLARED)
	statusCode, replayed := post(data.InferenceCreatePath, input, token)
	require.Equal(t, nethttp.StatusAccepted, statusCode)
	require.NotNil(t, replayed)
	require.True(t, replayed.Replayed)
	require.Equal(t, accepted.OperationId, replayed.OperationId)
	_, err = entClient.Client().RolePermission.UpdateOneID(createGrantID).SetStatus(rolepermission.StatusOff).Save(fixtureCtx)
	require.NoError(t, err)
	statusCode, _ = post(data.InferenceCreatePath, input, token)
	require.Equal(t, nethttp.StatusForbidden, statusCode, "replay cannot bypass current grant withdrawal even with stale Casbin policy")
	_, err = entClient.Client().RolePermission.UpdateOneID(createGrantID).SetStatus(rolepermission.StatusOn).Save(fixtureCtx)
	require.NoError(t, err)
	otherCtx := viewer.WithContext(ctx, appViewer.NewUserViewer(1, 2, 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	_, err = bff.CreateInference(auth.NewPrincipalContext(otherCtx, &auth.Principal{Type: auth.SubjectUser, ID: 1, TenantID: 2}), input)
	require.Error(t, err, "cross-tenant principal cannot read the accepted original")
	// The production receiver uses the same exact configured fixed owner map.
	t.Setenv("ANI_QUOTA_ENABLED", "true")
	t.Setenv("ANI_QUOTA_INTERNAL_ADDR", govConfig.ReleaseAddress)
	t.Setenv("ANI_QUOTA_CA_FILE", govConfig.CA)
	t.Setenv("ANI_QUOTA_CERT_FILE", govConfig.Cert)
	t.Setenv("ANI_QUOTA_KEY_FILE", govConfig.Key)
	registeredSAN := os.Getenv("INFERENCE_WIRING_REFUND_DNS_SAN")
	require.NotEmpty(t, registeredSAN)
	t.Setenv("ANI_QUOTA_INFERENCE_DNS_SAN", registeredSAN)
	receiver, err := server.NewQuotaInternalServer(server.QuotaInternalConfigFromEnv(), ledger)
	require.NoError(t, err)
	receiverDone := make(chan error, 1)
	go func() { receiverDone <- receiver.Start(ctx) }()
	defer func() { require.NoError(t, receiver.Stop(context.Background())); require.NoError(t, <-receiverDone) }()
	notice := map[string]any{"ReleaseEventID": uuid.NewString(), "OriginalCreateOperationID": accepted.OperationId, "ResourceID": accepted.ResourceId, "TenantID": "11111111-1111-4111-8111-111111111111", "Reason": "RESOURCE_RELEASED", "Items": []map[string]any{{"ChargeID": charges[0].ChargeID, "QuotaCode": charges[0].QuotaCode, "ReleasedTotal": charges[0].OriginalUnits}}, "ResourceRefs": []string{accepted.ResourceId}}
	refundConfigFile := filepath.Join(jointDir, "inference-refund-config.json")
	runInjected := func(label, expectedCode string) []struct {
		ChargeID                    string
		AppliedDelta, ReleasedTotal int64
	} {
		t.Helper()
		noticePath := filepath.Join(jointDir, "injected-refund-"+label+".json")
		raw, err := json.Marshal(notice)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(noticePath, raw, 0600))
		resultPath := filepath.Join(jointDir, "injected-refund-result-"+label+".json")
		refund := exec.CommandContext(ctx, binary, "-test.run=^TestInjectedQuotaRefund$", "-test.v")
		refund.Env = append(os.Environ(), "INFERENCE_REFUND_NOTIFICATION_FILE="+noticePath, "INFERENCE_REFUND_CONFIG_FILE="+refundConfigFile, "INFERENCE_REFUND_RESULT_FILE="+resultPath)
		output, callErr := refund.CombinedOutput()
		var result struct {
			Code    string `json:"code"`
			Receipt *struct {
				Items []struct {
					ChargeID                    string
					AppliedDelta, ReleasedTotal int64
				}
			} `json:"receipt"`
		}
		raw, err = os.ReadFile(resultPath)
		require.NoError(t, err, "read actual client result: %s", output)
		require.NoError(t, json.Unmarshal(raw, &result))
		require.Equal(t, expectedCode, result.Code, "actual client result: %s", output)
		if expectedCode != "OK" {
			require.Error(t, callErr)
			return nil
		}
		require.NoError(t, callErr, "actual refund invocation: %s", output)
		require.NotNil(t, result.Receipt)
		return result.Receipt.Items
	}
	// The real Inference port independently requires its persisted DELETE.
	runInjected("no-delete", "InvalidArgument")
	// The actual Gov receiver also requires its own persisted DELETE, even when
	// a correctly authenticated owner bypasses that client's local validation.
	refundConfig, err := readConfig[struct {
		Endpoint string
		TLS      struct{ CAFile, CertFile, KeyFile, ServerName string }
	}]("inference-refund-config.json")
	require.NoError(t, err)
	refundTLS, err := testTLS(refundConfig.TLS.CAFile, refundConfig.TLS.CertFile, refundConfig.TLS.KeyFile, refundConfig.TLS.ServerName)
	require.NoError(t, err)
	refundConnection, err := grpc.NewClient(refundConfig.Endpoint, grpc.WithTransportCredentials(credentials.NewTLS(refundTLS)))
	require.NoError(t, err)
	defer refundConnection.Close()
	noDeleteCtx, noDeleteCancel := context.WithTimeout(ctx, time.Second)
	_, err = quota.NewQuotaReleaseServiceClient(refundConnection).ReportQuotaRelease(noDeleteCtx, &quota.ReportQuotaReleaseRequest{ReleaseEventId: uuid.NewString(), OperationId: accepted.OperationId, Reason: quota.ReleaseReason_RESOURCE_RELEASED, Items: []*quota.QuotaReleaseItem{{ChargeId: charges[0].ChargeID, QuotaCode: charges[0].QuotaCode, ReleasedTotal: charges[0].OriginalUnits}}})
	noDeleteCancel()
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	beforeDelete, err := ledger.GetChargesForOperation(ctx, 1, accepted.OperationId)
	require.NoError(t, err)
	require.Zero(t, beforeDelete[0].ReleasedUnits)
	deleteInput := &view.DeleteInferenceRequest{Data: &view.DeleteInferenceData{IdempotencyKey: uuid.NewString(), ResourceId: accepted.ResourceId}}
	deletePath := "/api/v1/inference/services/" + accepted.ResourceId + ":delete"
	statusCode, _ = post(deletePath, deleteInput, token)
	require.Equal(t, nethttp.StatusForbidden, statusCode, "create grant cannot authorize delete")
	_, err = entClient.Client().RolePermission.UpdateOneID(deleteGrantID).SetStatus(rolepermission.StatusOn).Save(fixtureCtx)
	require.NoError(t, err)
	require.NoError(t, policy.ResetPolicies(ctx))
	statusCode, deleteResult := post(deletePath, deleteInput, token)
	require.Equal(t, nethttp.StatusAccepted, statusCode)
	require.NotNil(t, deleteResult)
	require.Equal(t, "QUEUED_FOR_OWNER", deleteResult.Result)
	waitJoint(t, "actual owner DELETE PG durable ACK", func() bool {
		op, err := ledger.GetOperationForUser(ctx, 1, deleteResult.OperationId)
		return err == nil && op.DispatchState == quotaoperation.DispatchStateAcked
	})
	// Q03 also bypasses the owner's local validation and reaches the actual
	// receiver with invalid original refs/vectors and real TLS identities.
	// Every rejection must leave the real charge, account and receipts intact.
	beforeNegativeReceipts, err := entClient.Client().QuotaReleaseReceipt.Query().Where(quotareleasereceipt.TenantIDEQ(1), quotareleasereceipt.OwnerServiceEQ("ani-inference")).Count(fixtureCtx)
	require.NoError(t, err)
	beforeNegativeAccount, err := entClient.Client().QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(1), quotaaccount.QuotaCodeEQ(charges[0].QuotaCode)).Only(fixtureCtx)
	require.NoError(t, err)
	assertNegativeUnchanged := func() {
		t.Helper()
		currentCharges, err := ledger.GetChargesForOperation(ctx, 1, accepted.OperationId)
		require.NoError(t, err)
		require.Len(t, currentCharges, 1)
		require.Equal(t, charges[0].ChargeID, currentCharges[0].ChargeID)
		require.Equal(t, charges[0].OriginalUnits, currentCharges[0].OriginalUnits)
		require.Zero(t, currentCharges[0].ReleasedUnits)
		currentAccount, err := entClient.Client().QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(1), quotaaccount.QuotaCodeEQ(charges[0].QuotaCode)).Only(fixtureCtx)
		require.NoError(t, err)
		require.Equal(t, beforeNegativeAccount.OccupiedUnits, currentAccount.OccupiedUnits)
		currentReceipts, err := entClient.Client().QuotaReleaseReceipt.Query().Where(quotareleasereceipt.TenantIDEQ(1), quotareleasereceipt.OwnerServiceEQ("ani-inference")).Count(fixtureCtx)
		require.NoError(t, err)
		require.Equal(t, beforeNegativeReceipts, currentReceipts)
	}
	assertNegativeUnchanged()
	fullReleaseRequest := func() *quota.ReportQuotaReleaseRequest {
		return &quota.ReportQuotaReleaseRequest{ReleaseEventId: uuid.NewString(), OperationId: accepted.OperationId, Reason: quota.ReleaseReason_RESOURCE_RELEASED, Items: []*quota.QuotaReleaseItem{{ChargeId: charges[0].ChargeID, QuotaCode: charges[0].QuotaCode, ReleasedTotal: charges[0].OriginalUnits}}, ResourceRefs: []*quota.QuotaResourceRef{{ResourceId: accepted.ResourceId}}}
	}
	rejectAtReceiver := func(label string, client quota.QuotaReleaseServiceClient, request *quota.ReportQuotaReleaseRequest, expected codes.Code) error {
		t.Helper()
		callCtx, callCancel := context.WithTimeout(ctx, time.Second)
		response, callErr := client.ReportQuotaRelease(callCtx, request)
		callCancel()
		require.Nil(t, response, label)
		require.Equal(t, expected, status.Code(callErr), label)
		assertNegativeUnchanged()
		t.Logf("actual quota receiver rejected %s: %s; original charge/account/receipts unchanged", label, status.Code(callErr))
		return callErr
	}
	rawReleaseClient := quota.NewQuotaReleaseServiceClient(refundConnection)
	for _, negative := range []struct {
		label  string
		mutate func(*quota.ReportQuotaReleaseRequest)
		code   codes.Code
	}{
		{"wrong-original-create", func(r *quota.ReportQuotaReleaseRequest) { r.OperationId = deleteResult.OperationId }, codes.PermissionDenied},
		{"missing-gpu", func(r *quota.ReportQuotaReleaseRequest) { r.Items = nil }, codes.InvalidArgument},
		{"partial-gpu", func(r *quota.ReportQuotaReleaseRequest) { r.Items[0].ReleasedTotal-- }, codes.FailedPrecondition},
		{"over-gpu", func(r *quota.ReportQuotaReleaseRequest) { r.Items[0].ReleasedTotal++ }, codes.FailedPrecondition},
	} {
		request := fullReleaseRequest()
		negative.mutate(request)
		rejectAtReceiver(negative.label, rawReleaseClient, request, negative.code)
	}
	wrongOwnerTLS, err := testTLS(govConfig.CA, govConfig.Cert, govConfig.Key, refundTLS.ServerName)
	require.NoError(t, err)
	wrongOwnerConnection, err := grpc.NewClient(refundConfig.Endpoint, grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(wrongOwnerTLS)))
	require.NoError(t, err)
	defer wrongOwnerConnection.Close()
	rejectAtReceiver("wrong-dns-owner", quota.NewQuotaReleaseServiceClient(wrongOwnerConnection), fullReleaseRequest(), codes.Unauthenticated)
	wrongCATLS := refundTLS.Clone()
	wrongCATLS.RootCAs = x509.NewCertPool()
	wrongCAConnection, err := grpc.NewClient(refundConfig.Endpoint, grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(wrongCATLS)))
	require.NoError(t, err)
	defer wrongCAConnection.Close()
	wrongCAErr := rejectAtReceiver("wrong-ca", quota.NewQuotaReleaseServiceClient(wrongCAConnection), fullReleaseRequest(), codes.Unavailable)
	require.Contains(t, status.Convert(wrongCAErr).Message(), "certificate signed by unknown authority")
	originalItems := notice["Items"]
	for _, mutation := range []struct {
		label string
		items any
	}{{"missing-gpu", []map[string]any{}}, {"partial-gpu", []map[string]any{{"ChargeID": charges[0].ChargeID, "QuotaCode": charges[0].QuotaCode, "ReleasedTotal": charges[0].OriginalUnits - 1}}}, {"over-gpu", []map[string]any{{"ChargeID": charges[0].ChargeID, "QuotaCode": charges[0].QuotaCode, "ReleasedTotal": charges[0].OriginalUnits + 1}}}} {
		notice["Items"] = mutation.items
		runInjected(mutation.label, "InvalidArgument")
	}
	notice["Items"] = originalItems
	// Q02 loses only a committed transport response. This task-only loopback
	// proxy verifies the exact Inference leaf and forwards the actual RPC with
	// its owner TLS identity to the unchanged real Gov receiver/Ent handler.
	proxyTLS, err := testTLS(govConfig.CA, govConfig.Cert, govConfig.Key, "")
	require.NoError(t, err)
	proxyTLS.ClientAuth = tls.RequireAndVerifyClientCert
	expectedInferenceLeaf := bytes.Clone(refundTLS.Certificates[0].Certificate[0])
	proxyTLS.VerifyConnection = func(state tls.ConnectionState) error {
		if state.Version < tls.VersionTLS13 || len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, expectedInferenceLeaf) {
			return fmt.Errorf("test fault proxy requires the exact verified task Inference certificate")
		}
		return nil
	}
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	lostRefund := &actualQuotaLostResponseProxy{backend: quota.NewQuotaReleaseServiceClient(refundConnection), eventID: notice["ReleaseEventID"].(string)}
	proxyServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(proxyTLS)))
	quota.RegisterQuotaReleaseServiceServer(proxyServer, lostRefund)
	proxyDone := make(chan error, 1)
	go func() { proxyDone <- proxyServer.Serve(proxyListener) }()
	defer func() { proxyServer.GracefulStop(); require.NoError(t, <-proxyDone) }()
	var proxyRefundConfig map[string]any
	proxyConfigJSON, err := os.ReadFile(refundConfigFile)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(proxyConfigJSON, &proxyRefundConfig))
	proxyRefundConfig["Endpoint"] = proxyListener.Addr().String()
	proxyConfigFile := filepath.Join(jointDir, "inference-refund-lost-response-config.json")
	proxyConfigJSON, err = json.Marshal(proxyRefundConfig)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(proxyConfigFile, proxyConfigJSON, 0600))
	refundConfigFile = proxyConfigFile
	runInjected("committed-response-lost", "Unavailable")
	lostRefund.mu.Lock()
	responseDropped, firstResponse := lostRefund.dropped, lostRefund.firstResponse
	lostRefund.mu.Unlock()
	require.True(t, responseDropped)
	require.NotNil(t, firstResponse)
	require.Len(t, firstResponse.Items, 1)
	require.Equal(t, charges[0].OriginalUnits, firstResponse.Items[0].AppliedDelta)
	committedCharges, err := ledger.GetChargesForOperation(ctx, 1, accepted.OperationId)
	require.NoError(t, err)
	require.Len(t, committedCharges, 1)
	require.Equal(t, charges[0].OriginalUnits, committedCharges[0].ReleasedUnits)
	receiptsBeforeRetry, err := entClient.Client().QuotaReleaseReceipt.Query().Where(quotareleasereceipt.TenantIDEQ(1), quotareleasereceipt.OwnerServiceEQ("ani-inference"), quotareleasereceipt.ReleaseEventIDEQ(lostRefund.eventID)).All(fixtureCtx)
	require.NoError(t, err)
	require.Len(t, receiptsBeforeRetry, 1)
	replay := runInjected("committed-response-retry", "OK")
	require.Len(t, replay, 1)
	require.Equal(t, charges[0].ChargeID, replay[0].ChargeID)
	require.Zero(t, replay[0].AppliedDelta)
	require.Equal(t, charges[0].OriginalUnits, replay[0].ReleasedTotal)
	lostRefund.mu.Lock()
	forwardedSuccesses := lostRefund.forwardedSuccesses
	lostRefund.mu.Unlock()
	require.Equal(t, 2, forwardedSuccesses, "both attempts must succeed at the actual receiver; only the first response is lost")
	receiptsAfterRetry, err := entClient.Client().QuotaReleaseReceipt.Query().Where(quotareleasereceipt.TenantIDEQ(1), quotareleasereceipt.OwnerServiceEQ("ani-inference"), quotareleasereceipt.ReleaseEventIDEQ(lostRefund.eventID)).All(fixtureCtx)
	require.NoError(t, err)
	require.Len(t, receiptsAfterRetry, 1)
	require.Equal(t, receiptsBeforeRetry[0].ReceiptID, receiptsAfterRetry[0].ReceiptID)
	refundConfigFile = filepath.Join(jointDir, "inference-refund-config.json")
	notice["Reason"] = "ABORTED_CLEANED"
	runInjected("same-event-different-content", "FailedPrecondition")
	notice["Reason"] = "RESOURCE_RELEASED"
	charges, err = ledger.GetChargesForOperation(ctx, 1, accepted.OperationId)
	require.NoError(t, err)
	require.Equal(t, charges[0].OriginalUnits, charges[0].ReleasedUnits)
	require.NoError(t, syncWorker.Step(ctx))
	assertUsageAck(2, acc.UsageState_ENDED)
	invariants, err := ledger.RecomputeInvariants(ctx, 1)
	require.NoError(t, err)
	for _, invariant := range invariants {
		require.True(t, invariant.Balanced)
	}
	err = entClient.Client().PlanModule.DeleteOneID(module.ID).Exec(fixtureCtx)
	require.NoError(t, err)
	result := map[string]any{"scope": "actual public JWT/Redis/Casbin BFF, Inference RPC/PG/render and controlled injected refund; Kubernetes/model execution held", "hardware_observed": false, "create_operation": accepted.OperationId, "resource_id": accepted.ResourceId, "delete_operation": deleteResult.OperationId, "refunded_units": charges[0].ReleasedUnits, "rendered_projection_file": projectionFile, "release_event_id": lostRefund.eventID, "release_response_lost": responseDropped, "release_forwarded_successes": forwardedSuccesses, "release_receipt_id": receiptsAfterRetry[0].ReceiptID, "actual_acc_sync_states": []string{"DECLARED", "ENDED"}, "actual_acc_sync_acked_revisions": []int{1, 2}, "owner_process_restarted": wasRestarted, "initial_owner_pid": firstOwnerPID, "restarted_owner_pid": secondOwnerPID}
	evidence, err := json.MarshalIndent(result, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(jointDir, "actual-inference-wiring-result.json"), evidence, 0600))
	t.Log(fmt.Sprintf("actual owner create/delete/controlled refund persisted for %s; workload and cleanup not_verified", accepted.ResourceId))
}

// The only fault is loss of an already committed response. Every call still
// reaches the real Inference RPC/PG path; business acceptance and authorization
// are never replaced. DELETE passes through without fault injection.
type actualInferenceLostAckClient struct {
	actual                            service.InferenceOwnerClient
	mu                                sync.Mutex
	createCalls                       int
	droppedAck                        bool
	firstOperationID, firstResourceID string
	afterFirstAck                     func() error
	afterFirstAckError                error
	firstCreateError, lastCreateError error
}

func (c *actualInferenceLostAckClient) Create(ctx context.Context, request *inferencev1.CreateInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	c.mu.Lock()
	c.createCalls++
	c.mu.Unlock()
	response, err := c.actual.Create(ctx, request)
	if err != nil {
		c.mu.Lock()
		if c.firstCreateError == nil {
			c.firstCreateError = err
		}
		c.lastCreateError = err
		c.mu.Unlock()
		return nil, err
	}
	ack := response.GetDurableOwnerAck()
	if ack == nil || !ack.Accepted || ack.OperationId != request.RequestId || ack.ResourceId != request.GetGpuOwnerAttachment().GetRef().GetResourceId() {
		return response, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.droppedAck {
		c.droppedAck = true
		c.firstOperationID = ack.OperationId
		c.firstResourceID = ack.ResourceId
		if c.afterFirstAck != nil {
			if err := c.afterFirstAck(); err != nil {
				c.afterFirstAckError = err
				return nil, status.Error(codes.FailedPrecondition, "actual owner process restart after committed ACK failed")
			}
		}
		return nil, status.Error(codes.Unavailable, "test-only response loss after actual durable owner ACK")
	}
	if ack.OperationId != c.firstOperationID || ack.ResourceId != c.firstResourceID {
		return nil, status.Error(codes.FailedPrecondition, "actual owner replay changed original ACK IDs")
	}
	return response, nil
}

func (c *actualInferenceLostAckClient) Delete(ctx context.Context, request *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	return c.actual.Delete(ctx, request)
}

func startActualInferenceOwner(ctx context.Context, binary, jointDir, ready, label string) (*jointProcess, error) {
	if err := os.Remove(ready); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	log, err := os.Create(filepath.Join(jointDir, "actual-inference-owner-"+label+".log"))
	if err != nil {
		return nil, err
	}
	command := exec.Command(binary, "-test.run=^TestManagedOwnerProcess$", "-test.timeout=0", "-test.v")
	command.Env = append(os.Environ(), "INFERENCE_MANAGED_PROCESS=1", "INFERENCE_MANAGED_READY="+ready)
	command.Stdout = log
	command.Stderr = log
	if err := command.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	process := &jointProcess{cmd: command, done: make(chan error, 1), log: log}
	go func() { process.done <- command.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case err := <-process.done:
			_ = log.Close()
			process.cmd = nil
			return nil, fmt.Errorf("actual Inference owner %s exited before ready: %v; inspect %s", label, err, log.Name())
		default:
		}
		if _, err := os.Stat(ready); err == nil {
			return process, nil
		} else if !os.IsNotExist(err) {
			process.stop()
			return nil, err
		}
		select {
		case <-ctx.Done():
			process.stop()
			return nil, ctx.Err()
		case <-timeout.C:
			process.stop()
			return nil, fmt.Errorf("actual Inference owner %s readiness timed out; inspect %s", label, log.Name())
		case <-ticker.C:
		}
	}
}

// The necessary Q02 fault stays entirely in the test transport boundary. The
// unchanged real receiver determines owner, scope, cumulative release and
// receipt; this proxy never manufactures or edits a successful response.
type actualQuotaLostResponseProxy struct {
	quota.UnimplementedQuotaReleaseServiceServer
	backend            quota.QuotaReleaseServiceClient
	eventID            string
	mu                 sync.Mutex
	dropped            bool
	forwardedSuccesses int
	firstResponse      *quota.ReportQuotaReleaseResponse
}

func (p *actualQuotaLostResponseProxy) ReportQuotaRelease(ctx context.Context, request *quota.ReportQuotaReleaseRequest) (*quota.ReportQuotaReleaseResponse, error) {
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		ctx = metadata.NewOutgoingContext(ctx, incoming.Copy())
	}
	response, err := p.backend.ReportQuotaRelease(ctx, request)
	if err != nil || response == nil || request.GetReleaseEventId() != p.eventID {
		return response, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forwardedSuccesses++
	if !p.dropped {
		p.dropped = true
		p.firstResponse = response
		return nil, status.Error(codes.Unavailable, "test-only response loss after actual quota release commit")
	}
	return response, nil
}
