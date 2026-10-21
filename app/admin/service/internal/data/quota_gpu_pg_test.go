//go:build quota_pg

package data

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go-wind-admin/pkg/audit"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	protocol "github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"google.golang.org/protobuf/encoding/protojson"

	"go-wind-admin/app/admin/service/internal/data/ent"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

//go:embed testdata/gpu/role_and_rls.sql
var gpuRoleAndRLS string

//go:embed testdata/gpu/null_tenant.sql
var gpuNullTenant string

//go:embed testdata/gpu/temp_forbidden.sql
var gpuTempForbidden string

func TestQuotaGpuProductionEntComposition(t *testing.T) {
	for _, trace := range []bool{false, true} {
		t.Run(fmt.Sprint(trace), func(t *testing.T) {
			dsn := os.Getenv("ANI_TEST_DATABASE_DSN")
			require.NotEmpty(t, dsn)
			encoded, e := json.Marshal(map[string]any{"data": map[string]any{"database": map[string]any{"driver": "postgres", "source": dsn, "enableTrace": trace, "migrate": false}}})
			require.NoError(t, e)
			cfg := &conf.Bootstrap{}
			require.NoError(t, protojson.Unmarshal(encoded, cfg))
			app := bootstrap.NewContextWithParam(context.Background(), &conf.AppInfo{}, cfg, bLogger.NopLogger())
			client, cleanup, e := NewEntClient(app)
			require.NoError(t, e)
			defer cleanup()
			require.Equal(t, "postgres", cfg.Data.Database.GetDriver(), "composition must not mutate caller config")
			r := NewQuotaLedgerRepo(app, client)
			require.NoError(t, r.transaction(context.Background(), func(tx *ent.Tx) error {
				ctx := appViewer.NewSystemViewerContext(context.Background())
				_, e := tx.QuotaDefinition.Query().All(ctx)
				return e
			}))
			cleanLedger(t, client)
			tid, _ := seedTenantPlan(t, client, "ent-production", 10)
			events := make([]audit.AuditEvent, 0)
			accCtx := context.WithValue(context.Background(), audit.AccumulatorKey(), &events)
			accepted, e := r.Occupy(accCtx, occupyInput(client, tid, "actor", uuid.NewString(), 2))
			require.NoError(t, e)
			require.NotEmpty(t, events)
			reads, writes := 0, 0
			for _, event := range events {
				require.True(t, event.DataMasked)
				require.Equal(t, sha256Hex(event.SqlText), event.SqlDigest)
				if event.Dialect == "tx" {
					if event.IsWrite {
						writes++
					} else {
						reads++
					}
				}
			}
			require.Positive(t, reads)
			require.Positive(t, writes)
			// Audit records describe executed statements, including rolled-back
			// attempts. Their presence is not a claim that the transaction committed.
			events = nil
			abort := errors.New("audit rollback fixture")
			var rollbackID uint32
			require.ErrorIs(t, r.transaction(accCtx, func(tx *ent.Tx) error {
				ctx := appViewer.NewSystemViewerContext(accCtx)
				p, err := tx.Plan.Create().SetName("audit-rollback-sensitive-value").Save(ctx)
				if err != nil {
					return err
				}
				rollbackID = p.ID
				return abort
			}), abort)
			require.NotEmpty(t, events)
			rollbackWrite := false
			for _, event := range events {
				require.NotContains(t, event.SqlText, "audit-rollback-sensitive-value")
				require.Equal(t, sha256Hex(event.SqlText), event.SqlDigest)
				rollbackWrite = rollbackWrite || (event.Dialect == "tx" && event.IsWrite && strings.Contains(event.SqlText, "sys_plans"))
			}
			require.True(t, rollbackWrite)
			_, e = client.Client().Plan.Get(appViewer.NewSystemViewerContext(context.Background()), rollbackID)
			require.True(t, ent.IsNotFound(e))
			require.NoError(t, r.CancelUnsent(context.Background(), tid, accepted.OperationID))
			balances, e := r.RecomputeInvariants(context.Background(), tid)
			require.NoError(t, e)
			require.NotEmpty(t, balances)
			for _, balance := range balances {
				require.True(t, balance.Balanced)
				require.Zero(t, balance.OccupiedUnits)
			}
		})
	}
}

func gpuLedgerInput(t *testing.T, c *ent.Client, tid, pid uint32) *QuotaOccupyInput {
	t.Helper()
	ctx := appViewer.NewSystemViewerContext(context.Background())
	for _, code := range []string{"gpu.physical.count", "storage.bytes"} {
		require.NoError(t, c.PlanQuota.Create().SetPlanID(pid).SetQuotaCode(code).SetQuotaValue(100).Exec(ctx))
	}
	in := occupyInputForResourceTenant(tid, c.Tenant.GetX(ctx, tid).ResourceTenantID, "actor", uuid.NewString(), 2)

	// This fixture exercises the current registered instance codec, including the
	// mixed immutable vector. A schema marker without its business snapshot is
	// deliberately not a valid managed command.
	in.OwnerService = commandcontract.InstanceOwner
	in.Action = commandcontract.InstanceCreateAction
	in.InstanceContractVersion = CurrentInstanceContractVersion
	in.Items = []QuotaOccupyItem{{"gpu.physical.count", 2}, {"storage.bytes", 10}}
	business := cpuConfigurationBusiness()
	business.ComputeType = "gpu"
	request := &acc.GpuRequest{ClusterId: business.ClusterId, PoolId: uuid.NewString(), ProfileId: uuid.NewString(), ProfileVersion: 1, Replicas: 1, DevicesPerReplica: 2, ContainerName: "workspace"}
	execution := &acc.GpuExecution{SchedulerName: "volcano", QueueName: "gpu", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": uuid.NewString(), "accelerator.ani.io/model-key": "nvidia-tesla-t4", "accelerator.ani.io/baseline-id": uuid.NewString()}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "2", "volcano.sh/vgpu-memory": "15360", "volcano.sh/vgpu-cores": "100"}}
	var e error
	execution.ExecutionDigest, e = protocol.GpuExecutionDigest(execution)
	require.NoError(t, e)
	business.GpuExecution = execution
	business.GpuDisplay = &acc.GpuDisplay{ProfileId: request.ProfileId, ProfileVersion: request.ProfileVersion, ProfileName: "ledger whole GPU", Mode: acc.SupplyMode_WHOLE_EXCLUSIVE, ModelKey: "nvidia-tesla-t4"}
	payload, e := res.CanonicalBusinessPayloadVersion(business, CurrentInstanceContractVersion)
	require.NoError(t, e)
	digest := sha256.Sum256(payload)
	canonical := commandcontract.GpuCanonical{SchemaVersion: 3, InstanceContractVersion: CurrentInstanceContractVersion, GpuRequest: request, BusinessPayload: payload, BusinessPayloadDigest: hex.EncodeToString(digest[:]), MeteringVersion: commandcontract.GpuMeteringVersion, QuotaItems: []commandcontract.GpuQuotaItem{{QuotaCode: "gpu.physical.count", Units: 2}, {QuotaCode: "storage.bytes", Units: 10}}}
	blob, e := commandcontract.CanonicalJSON(canonical)
	require.NoError(t, e)
	_, e = commandcontract.DecodeGpuCanonical(blob)
	require.NoError(t, e)
	in.CanonicalRequest = string(blob)
	return in
}
func gpuDelete(t *testing.T, in *QuotaOccupyInput) *QuotaDeleteInput {
	t.Helper()
	snapshot, err := commandcontract.DecodeGpuCanonical([]byte(in.CanonicalRequest))
	require.NoError(t, err)
	// Resource DELETE adds only its observed version to the same typed canonical.
	snapshot.DeleteExpectedVersion = 1
	canonical, err := commandcontract.CanonicalJSON(snapshot)
	require.NoError(t, err)
	return &QuotaDeleteInput{TenantID: in.TenantID, ActorType: in.ActorType, ActorID: in.ActorID, OwnerService: in.OwnerService, Action: commandcontract.InstanceDeleteAction, IdempotencyKey: uuid.NewString(), RequestHash: "delete-" + in.ResourceID + "-v1", ResourceID: in.ResourceID, CanonicalRequest: string(canonical)}
}
func gpuProjectionPayload(in *QuotaOccupyInput, id string, rev int64, hash string) string {
	b, _ := json.Marshal(map[string]any{"ref": map[string]string{"tenant_id": in.ResourceTenantID, "owner_service": in.OwnerService, "resource_id": in.ResourceID, "create_operation_id": id}, "revision": rev, "state": rev, "payload_digest": hash})
	return string(b)
}

func TestQuotaGpuRestrictedRoleAndExplicitIsolation(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	r := newTestRepo(c)
	ctx := context.Background()
	var compliant bool
	require.NoError(t, c.DB().QueryRowContext(ctx, gpuRoleAndRLS).Scan(&compliant))
	require.True(t, compliant, "must use non-owner restricted runtime with no RLS/policies/TEMP/DDL")
	_, e := c.DB().ExecContext(ctx, gpuNullTenant)
	require.Error(t, e)
	_, e = c.DB().ExecContext(ctx, gpuTempForbidden)
	require.Error(t, e)
	a, p := seedTenantPlan(t, c, "gpu-isolation-a", 20)
	b, pb := seedTenantPlan(t, c, "gpu-isolation-b", 20)
	in := gpuLedgerInput(t, c.Client(), a, p)
	original, e := r.Occupy(ctx, in)
	require.NoError(t, e)
	other := gpuLedgerInput(t, c.Client(), b, pb)
	other.IdempotencyKey = in.IdempotencyKey
	other.ActorType = in.ActorType
	other.ActorID = in.ActorID
	other.ResourceID = in.ResourceID
	own, e := r.Occupy(ctx, other)
	require.NoError(t, e)
	require.NotEqual(t, original.OperationID, own.OperationID)
	_, e = r.GetOperationForUser(ctx, b, original.OperationID)
	require.True(t, ent.IsNotFound(e))
	_, e = r.GetChargesForOperation(ctx, b, original.OperationID)
	require.Error(t, e)
	_, e = r.FindIdempotentOperation(ctx, b, in.ActorType, in.ActorID, in.Action, uuid.NewString())
	require.True(t, ent.IsNotFound(e))
	charges, e := r.GetChargesForOperation(ctx, a, original.OperationID)
	require.NoError(t, e)
	e = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		_, e := tx.QuotaCharge.Create().SetChargeID(uuid.NewString()).SetTenantID(b).SetOperationID(original.OperationID).SetQuotaCode(charges[0].QuotaCode).SetOriginalUnits(1).Save(ctx)
		return e
	})
	require.Error(t, e, "cross-tenant FK must reject known other-tenant UUID")
	ok, e := r.AckDispatched(ctx, b, original.OperationID, 1, `{}`)
	require.NoError(t, e)
	require.False(t, ok)
	require.Error(t, r.CancelUnsent(ctx, b, original.OperationID))
	// The explicit tenant clause in GetOperation is also the mutation target.
}

func TestQuotaGpuDeleteReleaseAndProjectionRecovery(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	r := newTestRepo(c)
	ctx := context.Background()
	tid, pid := seedTenantPlan(t, c, "gpu-recovery", 20)
	in := gpuLedgerInput(t, c.Client(), tid, pid)
	accepted, e := r.Occupy(ctx, in)
	require.NoError(t, e)
	charges, e := r.GetChargesForOperation(ctx, tid, accepted.OperationID)
	require.NoError(t, e)
	require.Len(t, charges, 2)
	rel := &QuotaReleaseInput{OwnerService: in.OwnerService, OperationID: accepted.OperationID, ResourceID: accepted.ResourceID, Complete: true, ReleaseEventID: uuid.NewString(), Reason: "RESOURCE_RELEASED", PayloadHash: "gpu-full", PayloadJSON: `{"fixture":"ledger-only"}`}
	_, e = r.Release(ctx, rel)
	require.NoError(t, e, "authenticated owner declares complete resource cleanup without transferring quantities")
	// The same original event is replayed below after a separately persisted
	// delete. It must not change the already returned GPU balance a second time.
	claimed, e := r.ClaimDispatchable(ctx, "owner-worker", time.Minute, 1)
	require.NoError(t, e)
	require.Len(t, claimed, 1)
	deleted, e := r.CreateDeleteOperation(ctx, gpuDelete(t, in))
	require.NoError(t, e)
	require.False(t, deleted.LocalCanceled)
	require.Len(t, deleted.ChargeIDs, 2)
	// DELETE does not have to be ACKED; the prior owner declaration already returned GPU.
	_, e = r.Release(ctx, rel)
	require.NoError(t, e)
	rows, e := r.RecomputeInvariants(ctx, tid)
	require.NoError(t, e)
	for _, row := range rows {
		require.True(t, row.Balanced)
		if isGPUCode(row.QuotaCode) {
			require.Zero(t, row.OccupiedUnits)
		} else if row.QuotaCode == "storage.bytes" {
			require.Zero(t, row.OccupiedUnits)
		}
	}
	op, all, hasDelete, e := r.LoadGpuProjectionSource(ctx, tid, accepted.OperationID)
	require.NoError(t, e)
	require.True(t, hasDelete)
	require.Len(t, all, 2)
	require.Equal(t, 1, op.AttemptCount)
	// Rebuild a missing row directly at terminal revision; no business attempt changes.
	payload := gpuProjectionPayload(in, accepted.OperationID, 2, "terminal")
	require.NoError(t, r.UpsertGpuUsageProjection(ctx, tid, accepted.OperationID, 2, "ENDED", payload, "terminal"))
	syncs, e := r.ClaimGpuUsageSync(ctx, "sync-a", time.Minute, 1)
	require.NoError(t, e)
	require.Len(t, syncs, 1)
	ok, e := r.AckGpuUsageSync(ctx, tid, accepted.OperationID, 1, syncs[0].LeaseGeneration)
	require.NoError(t, e)
	require.False(t, ok)
	ok, e = r.AckGpuUsageSync(ctx, tid, accepted.OperationID, 2, syncs[0].LeaseGeneration)
	require.NoError(t, e)
	require.True(t, ok)
	require.NoError(t, r.UpsertGpuUsageProjection(ctx, tid, accepted.OperationID, 1, "DECLARED", gpuProjectionPayload(in, accepted.OperationID, 1, "declared"), "declared"))
	syncs, e = r.ClaimGpuUsageSync(ctx, "sync-b", time.Minute, 1)
	require.NoError(t, e)
	require.Empty(t, syncs)
	require.Error(t, r.UpsertGpuUsageProjection(ctx, tid, accepted.OperationID, 2, "ENDED", `{"ref":null}`, "terminal"))
	again, e := r.GetOperationForUser(ctx, tid, accepted.OperationID)
	require.NoError(t, e)
	require.Equal(t, op.AttemptCount, again.AttemptCount)
	// A separate non-GPU resource retains its independent partial-release API.
	storage := *in
	storage.IdempotencyKey = uuid.NewString()
	storage.ResourceID = uuid.NewString()
	storage.RequestHash = "separate-storage"
	storage.CanonicalRequest = `{"resource":"storage"}`
	storage.OwnerService = "ani-storage-service"
	storage.Action = "STORAGE_CREATE"
	storage.InstanceContractVersion = 0
	storage.Items = []QuotaOccupyItem{{"storage.bytes", 10}}
	storageOp, e := r.Occupy(ctx, &storage)
	require.NoError(t, e)
	storageCharges, e := r.GetChargesForOperation(ctx, tid, storageOp.OperationID)
	require.NoError(t, e)
	require.Len(t, storageCharges, 1)
	out, e := r.Release(ctx, &QuotaReleaseInput{OwnerService: storage.OwnerService, OperationID: storageOp.OperationID, ReleaseEventID: uuid.NewString(), Reason: "RESOURCE_RELEASED", PayloadHash: "nongpu-partial", PayloadJSON: `{"fixture":"independent-storage"}`, Items: []QuotaReleaseItemInput{{storageCharges[0].ChargeID, "storage.bytes", 3}}})
	require.NoError(t, e)
	require.EqualValues(t, 3, out[0].AppliedDelta)

}

func TestQuotaGpuAtomicCancelAndSameKey(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	r := newTestRepo(c)
	ctx := context.Background()
	tid, pid := seedTenantPlan(t, c, "gpu-same-key", 20)
	in := gpuLedgerInput(t, c.Client(), tid, pid)
	var wg sync.WaitGroup
	results := make(chan *QuotaOccupyResult, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); v, e := r.Occupy(ctx, in); results <- v; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	id := ""
	for v := range results {
		if id == "" {
			id = v.OperationID
		}
		require.Equal(t, id, v.OperationID)
	}
	del := gpuDelete(t, in)
	v, e := r.CreateDeleteOperation(ctx, del)
	require.NoError(t, e)
	require.True(t, v.LocalCanceled)
	replay, e := r.CreateDeleteOperation(ctx, del)
	require.NoError(t, e)
	require.True(t, replay.Replayed)
	require.Equal(t, v.OperationID, replay.OperationID)
	del.IdempotencyKey = uuid.NewString()
	second, e := r.CreateDeleteOperation(ctx, del)
	require.NoError(t, e)
	require.True(t, second.LocalCanceled)
	require.NotEqual(t, v.OperationID, second.OperationID)
	dispatch, e := r.ClaimDispatchable(ctx, "worker", time.Minute, 10)
	require.NoError(t, e)
	require.Empty(t, dispatch)
	balances, e := r.RecomputeInvariants(ctx, tid)
	require.NoError(t, e)
	for _, row := range balances {
		require.Zero(t, row.OccupiedUnits)
		require.True(t, row.Balanced)
	}
	// Current subscription revocation cannot invalidate an already-authorized replay.
	sys := appViewer.NewSystemViewerContext(ctx)
	require.NoError(t, c.Client().Tenant.UpdateOneID(tid).SetExpiredAt(time.Now().Add(-time.Hour)).Exec(sys))
	repeated, e := r.Occupy(ctx, in)
	require.NoError(t, e)
	require.True(t, repeated.Replayed)
	require.Equal(t, id, repeated.OperationID, fmt.Sprintf("original operation %s", id))
	// A missing sole GPU charge still appears in recovery scanning because the
	// immutable canonical marks GPU; source validation must report corruption.
	all, e := r.GetChargesForOperation(ctx, tid, id)
	require.NoError(t, e)
	for _, charge := range all {
		if isGPUCode(charge.QuotaCode) {
			require.NoError(t, c.Client().QuotaCharge.DeleteOneID(charge.ID).Exec(sys))
		}
	}
	candidates, e := r.ScanGpuCreatePage(ctx, 0, 100)
	require.NoError(t, e)
	require.NotEmpty(t, candidates)
	_, _, _, e = r.LoadGpuProjectionSource(ctx, tid, id)
	require.Error(t, e)
}

func TestQuotaGpuProjectionNullAndForeignRefConstraints(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	r := newTestRepo(c)
	ctx := context.Background()
	tid, pid := seedTenantPlan(t, c, "gpu-json-constraints", 20)
	in := gpuLedgerInput(t, c.Client(), tid, pid)
	created, e := r.Occupy(ctx, in)
	require.NoError(t, e)
	params := ent.GpuUsageSync{TenantID: &tid, OperationID: created.OperationID, Revision: 1, State: "DECLARED", PayloadHash: "digest", ResourceTenantID: in.ResourceTenantID, OwnerService: in.OwnerService, ResourceID: in.ResourceID}
	for _, payload := range []string{`null`, `{}`, `{"revision":1,"state":1,"payload_digest":"digest","ref":null}`, `{"revision":null,"state":1,"payload_digest":"digest","ref":{}}`} {
		params.PayloadJSON = payload
		e = r.transaction(ctx, func(tx *ent.Tx) error {
			ctx := appViewer.NewSystemViewerContext(context.Background())
			e := upsertGpuUsageSync(ctx, tx, &params)
			return e
		})
		require.Error(t, e, "JSON NULL may not bypass a CHECK")
	}
	params.PayloadJSON = gpuProjectionPayload(in, created.OperationID, 1, "digest")
	params.ResourceTenantID = uuid.NewString()
	e = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(context.Background())
		e := upsertGpuUsageSync(ctx, tx, &params)
		return e
	})
	require.Error(t, e, "JSON ref must match relational columns")
	params.ResourceTenantID = in.ResourceTenantID
	params.TenantID = ptr(tid + 100000)
	e = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(context.Background())
		e := upsertGpuUsageSync(ctx, tx, &params)
		return e
	})
	require.Error(t, e, "original ref FK must retain tenant")
}

// Complete release derives quantities under the ledger locks and fences event
// reuse across tenant ledgers using the existing globally unique receipt ID.
func TestQuotaResourceReleasedOriginalLedgerAndEventFence(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	r := newTestRepo(c)
	ctx := context.Background()
	a, pa := seedTenantPlan(t, c, "full-release-a", 20)
	b, pb := seedTenantPlan(t, c, "full-release-b", 20)
	firstInput := gpuLedgerInput(t, c.Client(), a, pa)
	otherInput := gpuLedgerInput(t, c.Client(), b, pb)
	first, e := r.Occupy(ctx, firstInput)
	require.NoError(t, e)
	other, e := r.Occupy(ctx, otherInput)
	require.NoError(t, e)
	release := &QuotaReleaseInput{OwnerService: firstInput.OwnerService, ReleaseEventID: uuid.NewString(), OperationID: first.OperationID, ResourceID: first.ResourceID, Complete: true, Reason: "RESOURCE_RELEASED", PayloadHash: "full-original", PayloadJSON: `{"reason":"RESOURCE_RELEASED"}`}
	wrong := *release
	wrong.ResourceID = uuid.NewString()
	_, e = r.Release(ctx, &wrong)
	require.Error(t, e)
	wrong = *release
	wrong.OwnerService = "another-owner"
	_, e = r.Release(ctx, &wrong)
	require.Error(t, e)
	results, e := r.Release(ctx, release)
	require.NoError(t, e)
	require.Len(t, results, 2)
	for _, result := range results {
		require.Positive(t, result.AppliedDelta)
		require.Equal(t, result.AppliedDelta, result.ReleasedTotal)
	}
	repeated, e := r.Release(ctx, release)
	require.NoError(t, e)
	for _, result := range repeated {
		require.Zero(t, result.AppliedDelta)
	}
	changed := *release
	changed.PayloadHash = "same-event-different-reason"
	changed.Reason = "ABORTED_CLEANED"
	_, e = r.Release(ctx, &changed)
	require.Error(t, e)
	anotherEvent := *release
	anotherEvent.ReleaseEventID = uuid.NewString()
	anotherEvent.PayloadHash = "same-resource-another-event"
	repeated, e = r.Release(ctx, &anotherEvent)
	require.NoError(t, e)
	for _, result := range repeated {
		require.Zero(t, result.AppliedDelta)
	}
	crossTenant := *release
	crossTenant.OperationID = other.OperationID
	crossTenant.ResourceID = other.ResourceID
	crossTenant.PayloadHash = "same-event-another-tenant"
	_, e = r.Release(ctx, &crossTenant)
	require.Error(t, e, "an owner cannot reuse a complete event for another tenant")
	for _, tid := range []uint32{a, b} {
		rows, e := r.RecomputeInvariants(ctx, tid)
		require.NoError(t, e)
		for _, row := range rows {
			require.True(t, row.Balanced)
			if tid == a {
				require.Zero(t, row.OccupiedUnits)
			} else if row.QuotaCode == "gpu.physical.count" {
				require.EqualValues(t, 2, row.OccupiedUnits)
			} else if row.QuotaCode == "storage.bytes" {
				require.EqualValues(t, 10, row.OccupiedUnits)
			}
		}
	}
}
