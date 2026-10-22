//go:build quota_pg && inference_compute_joint

package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	authpb "go-wind-admin/api/gen/go/authentication/service/v1"
	identity "go-wind-admin/api/gen/go/identity/service/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	iview "go-wind-admin/api/gen/go/inference/view/v1"
	"go-wind-admin/app/admin/service/cmd/server/assets"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"sync/atomic"
)

// One fixture runs the production Gov JWT/Casbin/module checks, ledger,
// dispatch, owner mTLS/Model wrappers/PG/runner/close recovery and release RPC.
// Res/Model/ACC/runtime responses are external software-test boundaries only.
func TestInferenceComputeSoftwareFlow(t *testing.T) {
	for _, mode := range []string{"cpu", "shared", "whole"} {
		t.Run(mode, func(t *testing.T) {
			f := newComputeCrossFixture(t, mode)
			f.gate.dropAck.Store(true)
			f.assertHTTPIdentityBoundary()
			accepted := new(view.InferenceAcceptance)
			f.call("POST", "/api/v1/inference/services", f.create(), accepted, 202)
			require.NotEmpty(t, accepted.OperationId)
			require.NotEmpty(t, accepted.ResourceId)
			f.waitOwner(accepted.OperationId, "succeeded")
			original, err := f.ledger.GetOperationForUser(context.Background(), f.tenant.ID, accepted.OperationId)
			require.NoError(t, err)
			originalBytes := original.CanonicalRequest
			// The same key must replay the original accepted intent even if external
			// authorities disappear. Owner replay also precedes Model access.
			f.providers.unavailable = true
			repeated := new(view.InferenceAcceptance)
			f.call("POST", "/api/v1/inference/services", f.create(), repeated, 202)
			require.True(t, repeated.Replayed)
			require.Equal(t, accepted.OperationId, repeated.OperationId)
			require.Equal(t, int64(1), f.providers.computeCalls)
			require.Equal(t, int64(1), f.providers.modelCalls)
			require.NoError(t, os.WriteFile(filepath.Join(f.control, "model-unavailable"), nil, 0600))
			// Name-only updates inherit artifacts and frozen resources, with no Model.
			update := &view.UpdateInferenceRequest{IdempotencyKey: uuid.NewString(), ResourceId: accepted.ResourceId, ExpectedGeneration: 1, UpdateMask: fieldMask("served_model_name"), ServedModelName: "renamed"}
			changed := new(view.InferenceAcceptance)
			f.call("POST", "/api/v1/inference/services/"+accepted.ResourceId+":update", update, changed, 202)
			f.waitOwner(changed.OperationId, "succeeded")
			for _, action := range []string{"stop", "start", "restart"} {
				current, err := f.owner.GetTrusted(context.Background(), f.tenant.ResourceTenantID, f.actor, &runtimev1.GetInferenceServiceRequest{ResourceId: accepted.ResourceId})
				require.NoError(t, err)
				cmd := &view.InferenceServiceCommandRequest{IdempotencyKey: uuid.NewString(), ResourceId: accepted.ResourceId, ExpectedGeneration: current.Generation}
				out := new(view.InferenceAcceptance)
				f.call("POST", "/api/v1/inference/services/"+accepted.ResourceId+":"+action, cmd, out, 202)
				f.waitOwner(out.OperationId, "succeeded")
			}
			currentView := new(view.InferenceView)
			f.call("GET", "/api/v1/inference/services/"+accepted.ResourceId, &view.GetInferenceRequest{}, currentView, 200)
			require.Equal(t, "renamed", currentView.ServedModelName)
			require.EqualValues(t, 1500, currentView.CpuMillis)
			require.EqualValues(t, 2048, currentView.MemoryMib)
			require.Equal(t, f.providers.selection.SelectionDigest, currentView.SelectionDigest)
			require.Greater(t, currentView.Generation, int64(1))
			f.call("POST", "/api/v1/inference/services", f.create(), repeated, 202)
			require.Equal(t, accepted.OperationId, repeated.OperationId)
			unchanged, err := f.ledger.GetOperationForUser(context.Background(), f.tenant.ID, accepted.OperationId)
			require.NoError(t, err)
			require.Equal(t, originalBytes, unchanged.CanonicalRequest)
			f.assertAccounting(mode, false)
			if mode != "cpu" {
				require.NoError(t, os.WriteFile(filepath.Join(f.control, "cleanup-model-fails"), nil, 0600))
			}
			deletion := new(view.InferenceAcceptance)
			f.call("POST", "/api/v1/inference/services/"+accepted.ResourceId+":delete", &view.DeleteInferenceRequest{IdempotencyKey: uuid.NewString(), ResourceId: accepted.ResourceId}, deletion, 202)
			if mode != "cpu" {
				require.Eventually(t, func() bool { return f.eventCount("cleanup_model") > 0 }, 10*time.Second, 20*time.Millisecond)
				require.Zero(t, f.eventCount("observe_release"), "CleanupModel failure must precede release coordination")
				require.Zero(t, f.eventCount("release_report"))
				f.assertAccounting(mode, false)
				require.NoError(t, os.Remove(filepath.Join(f.control, "cleanup-model-fails")))
				// Absence/Unknown does not authorize a report or a refund.
				require.Eventually(t, func() bool { return f.eventCount("observe_release") > 0 }, 10*time.Second, 20*time.Millisecond)
				require.Zero(t, f.eventCount("release_report"))
				f.assertAccounting(mode, false)
				f.accReleased()
			}
			f.waitOwner(deletion.OperationId, "succeeded")
			f.assertAccounting(mode, true)
			f.assertUsage(mode)
			f.assertNoLegacy()
			require.Equal(t, int64(1), f.providers.computeCalls)
			require.Equal(t, int64(1), f.providers.modelCalls)
			require.Equal(t, 1, f.eventCount("model_get"), "update/start/stop/restart/replay must not re-read Model")
		})
	}
}

// DELETE-first is parameterized over CPU/GPU and late/never CREATE. A process
// restart uses the same database and discovers only-close tenants from PG.
func TestInferenceComputeDeleteFirstRecovery(t *testing.T) {
	for _, mode := range []string{"cpu", "shared"} {
		for _, late := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/late=%t", mode, late), func(t *testing.T) {
				f := newComputeCrossFixture(t, mode)
				f.gate.holdAfterFirst.Store(true)
				require.NoError(t, os.WriteFile(filepath.Join(f.control, "model-unavailable"), nil, 0600))
				a := new(view.InferenceAcceptance)
				f.call("POST", "/api/v1/inference/services", f.create(), a, 202)
				require.Eventually(t, func() bool { return f.eventCount("model_get") > 0 }, 10*time.Second, 20*time.Millisecond)
				op, err := f.ledger.GetOperationForUser(context.Background(), f.tenant.ID, a.OperationId)
				require.NoError(t, err)
				require.NotEqual(t, quotaoperation.DispatchStateAcked, op.DispatchState)
				require.NoError(t, os.WriteFile(filepath.Join(f.control, "pause"), nil, 0600))
				d := new(view.InferenceAcceptance)
				f.call("POST", "/api/v1/inference/services/"+a.ResourceId+":delete", &view.DeleteInferenceRequest{IdempotencyKey: uuid.NewString(), ResourceId: a.ResourceId}, d, 202)
				require.Eventually(t, func() bool {
					o, e := f.ledger.GetOperationForUser(context.Background(), f.tenant.ID, d.OperationId)
					return e == nil && o.DispatchState == quotaoperation.DispatchStateAcked
				}, 10*time.Second, 20*time.Millisecond)
				f.stopOwner()
				require.NoError(t, os.Remove(filepath.Join(f.control, "pause")))
				f.startOwner()
				if mode != "cpu" {
					require.Eventually(t, func() bool { return f.eventCount("observe_release") > 0 }, 10*time.Second, 20*time.Millisecond)
					require.Zero(t, f.eventCount("release_report"))
					pending, err := f.owner.GetOperationTrusted(context.Background(), f.tenant.ResourceTenantID, f.actor, &runtimev1.GetOperationRequest{OperationId: d.OperationId})
					require.NoError(t, err)
					require.Equal(t, "observe_release", pending.Step)
					f.assertAccounting(mode, false)
					// Lose the reply after Gov has applied the original event, before owner
					// marks complete. Restart/retry must send the identical persisted report.
					require.NoError(t, os.WriteFile(filepath.Join(f.control, "lose-release-ack-once"), nil, 0600))
					f.accReleased()
					require.Eventually(t, func() bool { return f.eventCount("release_applied_ack_lost") == 1 }, 10*time.Second, 20*time.Millisecond)
					waiting := new(iview.Operation)
					f.call("GET", "/api/v1/inference/operations/"+d.OperationId, &view.GetInferenceOperationRequest{}, waiting, 200)
					require.Equal(t, "report_resource_released", waiting.Step)
					require.Equal(t, "APPLIED", waiting.RefundState)
					require.NotEqual(t, "succeeded", waiting.Phase)

					f.stopOwner()
					require.NoError(t, os.Remove(filepath.Join(f.control, "pause")))
					f.startOwner()
				}
				f.waitOwner(d.OperationId, "succeeded")
				if mode != "cpu" {
					values := f.eventValues("release_report")
					require.GreaterOrEqual(t, len(values), 2)
					for _, value := range values[1:] {
						require.JSONEq(t, string(values[0]), string(value), "retries must reuse persisted original report")
					}
				}
				f.assertAccounting(mode, true)
				if late {
					// Original Gov dispatch may retry after the close; it must be accepted as
					// blocked, independently queryable and unable to resurrect a service.
					f.gate.holdAfterFirst.Store(false)
					require.Eventually(t, func() bool {
						o, e := f.owner.GetOperationTrusted(context.Background(), f.tenant.ResourceTenantID, f.actor, &runtimev1.GetOperationRequest{OperationId: a.OperationId})
						return e == nil && o.Phase == "failed" && o.ErrorCode == "cancelled_by_delete"
					}, 15*time.Second, 25*time.Millisecond)
				}
				listed, err := f.owner.ListOperationsTrusted(context.Background(), f.tenant.ResourceTenantID, f.actor, &runtimev1.ListOperationsRequest{ResourceId: a.ResourceId})
				require.NoError(t, err)
				wantTotal := int64(1)
				if late {
					wantTotal = 2
				}
				require.Equal(t, wantTotal, listed.Total)
				require.Len(t, listed.Items, int(wantTotal))
				seen := map[string]bool{}
				for _, item := range listed.Items {
					require.False(t, seen[item.Id])
					seen[item.Id] = true
				}
				require.True(t, seen[d.OperationId])
				require.Equal(t, late, seen[a.OperationId])
				var services, reservations int
				require.NoError(t, f.ownerDB.QueryRow(context.Background(), "SELECT count(*) FROM inference_services WHERE tenant_id=$1", f.tenant.ResourceTenantID).Scan(&services))
				require.Zero(t, services)
				require.NoError(t, f.ownerDB.QueryRow(context.Background(), "SELECT count(*) FROM inference_quota_reservations WHERE tenant_id=$1", f.tenant.ResourceTenantID).Scan(&reservations))
				require.Zero(t, reservations)
				f.assertNoLegacy()
			})
		}
	}
}

type computeCrossProviders struct {
	selection                *compute.ComputeSelection
	plan                     *acc.ResolvedGpuRequest
	computeCalls, modelCalls int64
	usageCalls               int
	usageRevisions           map[string]int
	unavailable              bool
	mu                       sync.Mutex
}

func (p *computeCrossProviders) ResolveComputePreset(_ context.Context, tenant, actor string, r *compute.ResolveComputePresetRequest) (*compute.ComputeSelection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.computeCalls++
	if p.unavailable {
		return nil, io.EOF
	}
	if tenant != r.TenantId || r.Usage != compute.Usage_INFERENCE || r.PresetId != p.selection.PresetId {
		return nil, io.ErrUnexpectedEOF
	}
	return proto.Clone(p.selection).(*compute.ComputeSelection), nil
}
func (p *computeCrossProviders) GetModelVersion(_ context.Context, tenant, actor string, r *modelv1.GetModelVersionRequest) (*modelv1.GetModelVersionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.modelCalls++
	if p.unavailable {
		return nil, io.EOF
	}
	return &modelv1.GetModelVersionResponse{Model: &modelv1.Model{TenantId: tenant}, Version: &modelv1.ModelVersion{Id: r.ModelVersionId, Status: "ready"}}, nil
}
func (p *computeCrossProviders) ResolveGpuRequest(_ context.Context, r *acc.ResolveGpuRequestRequest, _ ...grpc.CallOption) (*acc.ResolvedGpuRequest, error) {
	if p.plan == nil {
		return nil, fmt.Errorf("CPU called GPU")
	}
	return proto.Clone(p.plan).(*acc.ResolvedGpuRequest), nil
}

func (p *computeCrossProviders) SyncGpuUsage(_ context.Context, r *acc.SyncGpuUsageRequest) (*acc.SyncGpuUsageResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usageCalls++
	if p.usageRevisions == nil {
		p.usageRevisions = map[string]int{}
	}
	p.usageRevisions[fmt.Sprint(r.Projection.Revision)]++
	return &acc.SyncGpuUsageResponse{Ref: r.Projection.Ref, AppliedRevision: r.Projection.Revision, State: r.Projection.State}, nil
}

type computeCrossOwnerGate struct {
	t *testing.T
	*data.InferenceClient
	holdAfterFirst atomic.Bool
	dropAck        atomic.Bool
	calls          atomic.Int64
}

func (g *computeCrossOwnerGate) CreateTrusted(ctx context.Context, tenant, actor string, r *runtimev1.CreateInferenceServiceRequest) (*runtimev1.OperationResponse, error) {
	if g.calls.Add(1) > 1 && g.holdAfterFirst.Load() {
		return nil, status.Error(codes.Unavailable, "controlled CREATE transport outage")
	}
	response, err := g.InferenceClient.CreateTrusted(ctx, tenant, actor, r)
	if err != nil {
		g.t.Logf("owner CREATE rejected: %v", err)
	}
	if err == nil && g.dropAck.CompareAndSwap(true, false) {
		return nil, status.Error(codes.Unavailable, "durable owner accepted; controlled ACK loss")
	}
	return response, err
}

type computeCrossOutput struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *computeCrossOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *computeCrossOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type computeCrossFixture struct {
	gate                                    *computeCrossOwnerGate
	t                                       *testing.T
	tenant                                  *ent.Tenant
	actor, token, url, control, pki, binary string
	ledger                                  *data.QuotaLedgerRepo
	worker                                  *service.QuotaDispatchWorker
	providers                               *computeCrossProviders
	owner                                   *data.InferenceClient
	ownerDB                                 *pgxpool.Pool
	request                                 *view.CreateInferenceRequest
	process                                 *exec.Cmd
	done                                    chan error
	output                                  computeCrossOutput
	ownerClose                              func()
	ownerAddress                            string
	config                                  string
}

func newComputeCrossFixture(t *testing.T, mode string) *computeCrossFixture {
	t.Helper()
	ctx := context.Background()
	sys := appViewer.NewSystemViewerContext(ctx)
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	f := &computeCrossFixture{t: t, control: t.TempDir(), pki: computeCrossPKI(t), binary: os.Getenv("INFERENCE_COMPUTE_TEST_BINARY")}
	require.True(t, filepath.IsAbs(f.binary), "final matching owner test binary required")
	var err error
	f.ownerDB, err = pgxpool.New(ctx, os.Getenv("INFERENCE_PG_DSN"))
	require.NoError(t, err)
	t.Cleanup(f.ownerDB.Close)
	key := make([]byte, 32)
	_, err = rand.Read(key)
	require.NoError(t, err)
	cfg := &conf.Bootstrap{Authz: &conf.Authorization{Type: "casbin"}, Authn: &conf.Authentication{Jwt: &conf.Authentication_Jwt{Method: "HS256", Key: hex.EncodeToString(key)}}}
	bctx := testutil.NewBootstrapContext(cfg)
	plan, err := c.Client().Plan.Create().SetName("compute-" + uuid.NewString()).Save(sys)
	require.NoError(t, err)
	f.tenant, err = c.Client().Tenant.Create().SetName("compute-" + uuid.NewString()).SetCode("compute-" + uuid.NewString()).SetPlanID(plan.ID).Save(sys)
	require.NoError(t, err)
	for _, module := range []planmodule.Module{planmodule.Module("INFERENCE"), planmodule.ModuleAccelerator} {
		require.NoError(t, c.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(module).Exec(sys))
	}
	for _, code := range []string{service.GpuPhysicalQuotaCode, service.GpuSharedQuotaCode} {
		require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(plan.ID).SetQuotaCode(code).SetQuotaValue(100000).Exec(sys))
	}
	u, err := c.Client().User.Create().SetTenantID(f.tenant.ID).SetUsername("compute-" + uuid.NewString()).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	f.actor = "governance:user:" + strconv.FormatUint(uint64(u.ID), 10)
	r, err := c.Client().Role.Create().SetTenantID(f.tenant.ID).SetName("compute").SetCode("tenant:compute:" + uuid.NewString()).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	require.NoError(t, c.Client().UserRole.Create().SetTenantID(f.tenant.ID).SetUserID(u.ID).SetRoleID(r.ID).SetStatus(userrole.StatusActive).Exec(sys))
	catalog, err := dbbootstrap.Catalog(assets.OpenApiData)
	require.NoError(t, err)
	for _, entry := range catalog {
		if entry.Module != "InferenceService" {
			continue
		}
		route, e := c.Client().Api.Query().Where(api.PathEQ(entry.Path), api.MethodEQ(entry.Method)).Only(sys)
		if ent.IsNotFound(e) {
			route, e = c.Client().Api.Create().SetModule(entry.Module).SetOperation(entry.Operation).SetPath(entry.Path).SetMethod(entry.Method).SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModule("INFERENCE")).SetStatus(api.StatusOn).Save(sys)
		}
		require.NoError(t, e)
		grant, e := c.Client().Permission.Create().SetName("compute-" + uuid.NewString()).SetCode("compute:" + uuid.NewString()).SetStatus(permission.StatusOn).Save(sys)
		require.NoError(t, e)
		require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(grant.ID).SetAPIID(route.ID).Exec(sys))
		require.NoError(t, c.Client().RolePermission.Create().SetTenantID(f.tenant.ID).SetRoleID(r.ID).SetPermissionID(grant.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Exec(sys))
	}
	f.ledger = data.NewQuotaLedgerRepo(bctx, c)
	qs, err := NewQuotaInternalServer(QuotaInternalServerConfig{Enabled: true, Address: "127.0.0.1:0", CAFile: filepath.Join(f.pki, "ca.pem"), CertFile: filepath.Join(f.pki, "gov-server.pem"), KeyFile: filepath.Join(f.pki, "gov-server.key"), CertOwnerMap: map[string]string{"ani-inference-service": "ani-inference"}}, f.ledger)
	require.NoError(t, err)
	qdone := make(chan error, 1)
	go func() { qdone <- qs.Start(ctx) }()
	t.Cleanup(func() { require.NoError(t, qs.Stop(ctx)); require.NoError(t, <-qdone) })
	f.config = filepath.Join(f.control, "owner.json")
	bc := map[string]any{"server": map[string]any{"grpc": map[string]any{"network": "tcp", "addr": "127.0.0.1:0", "timeout": "3s"}}, "managed_gpu": map[string]any{"refund": map[string]any{"address": qs.listener.Addr().String(), "ca_file": filepath.Join(f.pki, "ca.pem"), "cert_file": filepath.Join(f.pki, "inf-client.pem"), "key_file": filepath.Join(f.pki, "inf-client.key"), "server_name": "ani-governance", "timeout": "3s"}}}
	raw, err := json.Marshal(bc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.config, raw, 0600))
	f.providers = computeCrossVector(t, mode)
	require.NoError(t, os.WriteFile(filepath.Join(f.control, "acc-result.json"), []byte(`{"Finding":"RELEASE_UNKNOWN"}`), 0600))
	f.startOwner()
	f.gate = &computeCrossOwnerGate{InferenceClient: f.owner, t: t}
	t.Cleanup(f.stopOwner)
	usage := service.NewGpuUsageSyncWorker(f.ledger, f.providers, func(err error) { t.Log("GPU Usage retry", err) })
	require.NoError(t, usage.Start(ctx))
	t.Cleanup(func() { require.NoError(t, usage.Stop(ctx)) })
	tenants := data.NewTenantRepo(bctx, c)
	binding := service.NewInferenceComputeBinding(f.gate, data.NewInferenceAuthorizationRepo(c), f.providers, f.providers)
	registry := service.NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(binding))
	f.worker = service.NewQuotaDispatchWorker(bctx, f.ledger, registry)
	require.NoError(t, f.worker.Start(ctx))
	t.Cleanup(func() { require.NoError(t, f.worker.Stop(ctx)) })
	acceptance, err := service.NewGpuAcceptance(f.ledger, registry, tenants, f.providers, binding, f.worker)
	require.NoError(t, err)
	handler := service.NewInferenceServiceWithOwnerAndModel(acceptance, f.owner, tenants, f.providers)
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	tokens := data.NewAuthenticator(bctx, data.NewUserTokenCache(bctx, rdb))
	checker := data.NewTokenChecker(bctx, tokens, authpb.ClientType_admin)
	roles := data.NewRoleRepo(bctx, c, data.NewRolePermissionRepo(bctx, c), data.NewRoleOrgUnitRepo(bctx, c), data.NewPermissionRepo(bctx, c, data.NewPermissionApiRepo(bctx, c), data.NewPermissionMenuRepo(bctx, c)), data.NewRoleMetadataRepo(bctx, c), data.NewRoleFieldPermissionRepo(bctx, c))
	policy := authorizer.NewAuthorizer(bctx, data.NewAuthorizerProvider(bctx, roles, data.NewApiRepo(bctx, c)))
	require.NoError(t, policy.ResetPolicies(ctx))
	f.token, _, err = tokens.CreateUserToken(ctx, authpb.ClientType_admin, &authpb.UserTokenPayload{UserId: u.ID, TenantId: trans.Ptr(f.tenant.ID), Roles: []string{*r.Code}, DataScopes: []identity.DataScope{identity.DataScope_ALL}, Application: authpb.ApplicationType_console.Enum(), Sid: trans.Ptr(uuid.NewString())})
	require.NoError(t, err)
	h := khttp.NewServer(khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(data.NewTenantAccessCheckerImpl(bctx, c)), auth.WithInjectMetadata(false), auth.WithInjectEnt(true)), authz.Server(policy.Engine())))
	RegisterInferenceHTTPServer(h, handler)
	httpServer := httptest.NewServer(h)
	t.Cleanup(httpServer.Close)
	f.url = httpServer.URL
	f.request = &view.CreateInferenceRequest{IdempotencyKey: uuid.NewString(), PresetId: f.providers.selection.PresetId, Name: "software-" + uuid.NewString(), ModelVersionId: uuid.NewString(), ServedModelName: "software-model", EngineImage: "runtime.invalid/fixture:v1", EngineType: "vllm", Command: "complete caller command --served-model-name software-model", Replicas: 1}
	return f
}
func fieldMask(paths ...string) *fieldmaskpb.FieldMask { return &fieldmaskpb.FieldMask{Paths: paths} }
func (f *computeCrossFixture) assertHTTPIdentityBoundary() {
	f.t.Helper()
	body, err := protojson.Marshal(f.create())
	require.NoError(f.t, err)
	for _, key := range []bool{false, true} {
		r, err := http.NewRequest("POST", f.url+"/api/v1/inference/services", bytes.NewReader(body))
		require.NoError(f.t, err)
		r.Header.Set("Content-Type", "application/json")
		want := http.StatusUnauthorized
		if key {
			r.Header.Set("X-Access-Key", "ak-software-test")
			r.Header.Set("X-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
			r.Header.Set("X-Signature", strings.Repeat("0", 64))
			want = http.StatusForbidden
		}
		reply, err := http.DefaultClient.Do(r)
		require.NoError(f.t, err)
		_, _ = io.Copy(io.Discard, reply.Body)
		_ = reply.Body.Close()
		require.Equal(f.t, want, reply.StatusCode)
	}
}
func (f *computeCrossFixture) create() *view.CreateInferenceRequest {
	return proto.Clone(f.request).(*view.CreateInferenceRequest)
}
func (f *computeCrossFixture) call(method, path string, in, out proto.Message, expected int) {
	f.t.Helper()
	raw, err := protojson.Marshal(in)
	require.NoError(f.t, err)
	r, err := http.NewRequest(method, f.url+path, bytes.NewReader(raw))
	require.NoError(f.t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.token)
	response, err := http.DefaultClient.Do(r)
	require.NoError(f.t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(f.t, err)
	require.Equal(f.t, expected, response.StatusCode, "%s %s: %s", method, path, body)
	require.NoError(f.t, protojson.Unmarshal(body, out))
}
func (f *computeCrossFixture) waitAck(id string) {
	f.t.Helper()
	require.Eventually(f.t, func() bool {
		o, e := f.ledger.GetOperationForUser(context.Background(), f.tenant.ID, id)
		return e == nil && o.DispatchState == quotaoperation.DispatchStateAcked
	}, 20*time.Second, 25*time.Millisecond)
}
func (f *computeCrossFixture) waitOwner(id, phase string) {
	f.t.Helper()
	if _, err := f.ledger.GetOperationForUser(context.Background(), f.tenant.ID, id); err == nil {
		f.waitAck(id)
	}
	require.Eventually(f.t, func() bool {
		o, e := f.owner.GetOperationTrusted(context.Background(), f.tenant.ResourceTenantID, f.actor, &runtimev1.GetOperationRequest{OperationId: id})
		return e == nil && o.Phase == phase
	}, 20*time.Second, 20*time.Millisecond, "owner operation %s -> %s; output=%s", id, phase, f.output.String())
	op := new(iview.Operation)
	f.call("GET", "/api/v1/inference/operations/"+id, &view.GetInferenceOperationRequest{}, op, 200)
	require.Equal(f.t, phase, op.Phase)
}
func (f *computeCrossFixture) eventValues(name string) []json.RawMessage {
	raw, err := os.ReadFile(filepath.Join(f.control, "events.jsonl"))
	if err != nil {
		return nil
	}
	var values []json.RawMessage
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		var event struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		}
		if json.Unmarshal(line, &event) == nil && event.Name == name {
			values = append(values, event.Value)
		}
	}
	return values
}
func (f *computeCrossFixture) eventCount(name string) int {
	raw, err := os.ReadFile(filepath.Join(f.control, "events.jsonl"))
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		var event struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(line, &event) == nil && event.Name == name {
			count++
		}
	}
	return count
}
func (f *computeCrossFixture) assertNoLegacy() {
	f.t.Helper()
	for _, n := range []string{"legacy_reserve", "legacy_confirm", "legacy_release", "legacy_get", "gpu_resolve"} {
		require.Zero(f.t, f.eventCount(n), n)
	}
	var count int
	require.NoError(f.t, f.ownerDB.QueryRow(context.Background(), "SELECT count(*) FROM inference_quota_reservations WHERE tenant_id=$1", f.tenant.ResourceTenantID).Scan(&count))
	require.Zero(f.t, count)
}
func (f *computeCrossFixture) assertUsage(mode string) {
	f.t.Helper()
	if mode == "cpu" {
		f.providers.mu.Lock()
		defer f.providers.mu.Unlock()
		require.Zero(f.t, f.providers.usageCalls)
		return
	}
	require.Eventually(f.t, func() bool {
		f.providers.mu.Lock()
		defer f.providers.mu.Unlock()
		return f.providers.usageRevisions["1"] == 1 && f.providers.usageRevisions["2"] == 1
	}, 10*time.Second, 20*time.Millisecond, "Gov must write each frozen GPU Usage revision once")
}
func (f *computeCrossFixture) assertAccounting(mode string, released bool) {
	rows, err := f.ledger.RecomputeInvariants(context.Background(), f.tenant.ID)
	require.NoError(f.t, err)
	for _, r := range rows {
		require.True(f.t, r.Balanced)
		if mode == "cpu" || released {
			require.Zero(f.t, r.OccupiedUnits)
		} else if r.QuotaCode == service.GpuSharedQuotaCode && mode == "shared" {
			require.EqualValues(f.t, 8192, r.OccupiedUnits)
		} else if r.QuotaCode == service.GpuPhysicalQuotaCode && mode == "whole" {
			require.EqualValues(f.t, 2, r.OccupiedUnits)
		}
	}
}
func (f *computeCrossFixture) accReleased() {
	require.NoError(f.t, os.WriteFile(filepath.Join(f.control, "acc-result.json"), []byte(`{"Finding":"ALLOCATION_RELEASED","EvidenceID":"software-isolated-evidence","EvidenceDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), 0600))
}
func (f *computeCrossFixture) startOwner() {
	f.t.Helper()
	ready := filepath.Join(f.control, "ready")
	_ = os.Remove(ready)
	cmd := exec.Command(f.binary, "-test.run=^TestComputeOwnerProcess$", "-test.timeout=4m", "-test.v")
	cmd.Env = append(os.Environ(), "INFERENCE_COMPUTE_PROCESS=1", "INFERENCE_COMPUTE_CLUSTER_ID="+f.providers.selection.GetGpu().GetClusterId(), "INFERENCE_COMPUTE_CONTROL_DIR="+f.control, "INFERENCE_MANAGED_OWNER_CONFIG_FILE="+f.config, "INFERENCE_MANAGED_READY="+ready, "ANI_SERVER_TLS_CA_FILE="+filepath.Join(f.pki, "ca.pem"), "ANI_SERVER_TLS_CERT_FILE="+filepath.Join(f.pki, "inf-server.pem"), "ANI_SERVER_TLS_KEY_FILE="+filepath.Join(f.pki, "inf-server.key"))
	cmd.Stdout = &f.output
	cmd.Stderr = &f.output
	require.NoError(f.t, cmd.Start())
	f.process = cmd
	f.done = make(chan error, 1)
	go func() { f.done <- cmd.Wait() }()
	var address string
	require.Eventually(f.t, func() bool { raw, e := os.ReadFile(ready); address = string(raw); return e == nil && address != "" }, 15*time.Second, 20*time.Millisecond, "owner fixture startup")
	if f.owner == nil {
		f.ownerAddress = address
		raw, err := os.ReadFile(f.config)
		require.NoError(f.t, err)
		var config map[string]any
		require.NoError(f.t, json.Unmarshal(raw, &config))
		config["server"].(map[string]any)["grpc"].(map[string]any)["addr"] = address
		raw, err = json.Marshal(config)
		require.NoError(f.t, err)
		require.NoError(f.t, os.WriteFile(f.config, raw, 0600))
		f.owner, f.ownerClose, err = data.NewInferenceClient(data.InferenceClientConfig{Address: address, CAFile: filepath.Join(f.pki, "ca.pem"), CertFile: filepath.Join(f.pki, "gov-client.pem"), KeyFile: filepath.Join(f.pki, "gov-client.key"), ServerName: "ani-inference-service", Timeout: 3 * time.Second})
		require.NoError(f.t, err)
		f.t.Cleanup(f.ownerClose)
	} else {
		require.Equal(f.t, f.ownerAddress, address, "restart must use the original endpoint")
	}
}
func (f *computeCrossFixture) stopOwner() {
	if f.process == nil {
		return
	}
	require.NoError(f.t, f.process.Process.Signal(syscall.SIGTERM))
	select {
	case err := <-f.done:
		require.NoError(f.t, err, "owner: %s", f.output.String())
	case <-time.After(8 * time.Second):
		_ = f.process.Process.Kill()
		<-f.done
		f.t.Error("owner did not stop")
	}
	f.process = nil
}
func computeCrossVector(t *testing.T, mode string) *computeCrossProviders {
	p := &computeCrossProviders{selection: &compute.ComputeSelection{SchemaVersion: 1, PresetId: uuid.NewString(), Code: "compute-software", Name: "Software compute", ComputeType: "cpu", CpuMillis: 1500, MemoryMib: 2048}}
	if mode != "cpu" {
		g := &compute.GpuSelection{ProfileId: uuid.NewString(), ProfileVersion: 1, SpecDigest: strings.Repeat("b", 64), DevicesPerUnit: 1, ClusterId: uuid.NewString(), PoolId: uuid.NewString(), Mode: acc.SupplyMode_SHARED_FIXED, ModelKey: "nvidia-test", MemoryMibPerDevice: 8192, CoreLimitPercent: 25}
		code := service.GpuSharedQuotaCode
		units := int64(8192)
		if mode == "whole" {
			g.Mode = acc.SupplyMode_WHOLE_EXCLUSIVE
			g.DevicesPerUnit = 2
			g.CoreLimitPercent = 100
			code = service.GpuPhysicalQuotaCode
			units = 2
		}
		p.selection.ComputeType = "gpu"
		p.selection.Gpu = g
		execution := &acc.GpuExecution{SchedulerName: "volcano", QueueName: "gpu", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": uuid.NewString(), "accelerator.ani.io/model-key": g.ModelKey, "accelerator.ani.io/baseline-id": uuid.NewString()}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": strconv.FormatUint(uint64(g.DevicesPerUnit), 10), "volcano.sh/vgpu-memory": "8192", "volcano.sh/vgpu-cores": strconv.FormatUint(uint64(g.CoreLimitPercent), 10)}}
		var err error
		execution.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(execution)
		require.NoError(t, err)
		p.plan = &acc.ResolvedGpuRequest{Execution: execution, Selection: &acc.GpuDisplay{ProfileId: g.ProfileId, ProfileVersion: 1, ProfileName: "software", ModelKey: g.ModelKey, Mode: g.Mode}, QuotaItems: []*acc.GpuQuotaItem{{QuotaCode: code, Units: units}}}
	}
	var err error
	p.selection.SelectionDigest, err = compute.SelectionDigest(p.selection)
	require.NoError(t, err)
	return p
}
func computeCrossPKI(t *testing.T) string {
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated-compute-test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	for name, dns := range map[string]string{"gov-server": "ani-governance", "inf-server": "ani-inference-service", "gov-client": "ani-governance", "inf-client": "ani-inference-service"} {
		usage := x509.ExtKeyUsageServerAuth
		if strings.HasSuffix(name, "client") {
			usage = x509.ExtKeyUsageClientAuth
		}
		issueQuotaCert(t, ca, key, []string{dns}, usage, dir, name)
	}
	return dir
}
