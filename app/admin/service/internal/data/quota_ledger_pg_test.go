//go:build quota_pg

package data

// QUOTA-GPU-LOCAL-01 强制 PostgreSQL 集成测试（计划 §15）。
// 运行方式：go test -tags quota_pg ./app/admin/service/internal/data -run '^TestQuotaPostgres' -count=1
// DSN 由任务环境提供（环境变量 ANI_TEST_DATABASE_DSN，指向已按
// expand→data→constraints 完成迁移的隔离库）；缺少 DSN 必须失败，不得 Skip。

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql pgx 驱动

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	entsql "entgo.io/ent/dialect/sql"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"

	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planquota"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/internal/data/ent/quotadefinition"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

// ── 用例 ─────────────────────────────────────────────────────

// TestQuotaPostgresOccupyBasics 基本占额 + 幂等重放 + 同 key 异内容冲突。
func TestQuotaPostgresOccupyBasics(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_occupy_basics", 8)

	res, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "10000000-0000-4000-8000-000000000001", 2))
	require.NoError(t, err)
	require.False(t, res.Replayed)
	require.Len(t, res.ChargeIDs, 1)
	require.Equal(t, int64(2), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))

	// 同 key 同内容：重放，不重复扣额。
	replay, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "10000000-0000-4000-8000-000000000001", 2))
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, res.OperationID, replay.OperationID)
	require.Equal(t, int64(2), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))

	// 同 key 不同内容（规范请求哈希不同）：409 IDEMPOTENCY_CONFLICT。
	conflict := occupyInput(c, tenantId, "7", "10000000-0000-4000-8000-000000000001", 3)
	conflict.RequestHash = "hash-other-content"
	_, err = repo.Occupy(context.Background(), conflict)
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_CONFLICT", codeOf(t, err))

	// 额度不足：409。
	_, err = repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "10000000-0000-4000-8000-000000000002", 7))
	require.Error(t, err)
	require.Equal(t, "QUOTA_EXCEEDED", codeOf(t, err))

	// 政策缺失：403 QUOTA_NOT_CONFIGURED（另一租户未配 quota）。
	other := occupyInput(c, tenantId, "7", "10000000-0000-4000-8000-000000000003", 1)
	other.Items[0].QuotaCode = "storage.bytes"
	_, err = repo.Occupy(context.Background(), other)
	require.Error(t, err)
	require.Equal(t, "QUOTA_NOT_CONFIGURED", codeOf(t, err))
}

// TestQuotaPostgresConcurrentLimit CON-01：上限 8，20 并发各 1，只接受 8 个。
func TestQuotaPostgresConcurrentLimit(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_concurrent", 8)

	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	errs := make([]error, 0, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("30000000-0000-4000-8000-%012d", n)
			in := occupyInput(c, tenantId, fmt.Sprintf("%d", 100+n), key, 1)
			in.ResourceID = fmt.Sprintf("40000000-0000-4000-8000-%012d", n)
			_, err := repo.Occupy(context.Background(), in)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				accepted++
			} else {
				errs = append(errs, err)
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, 8, accepted, "只能接受 8 个")
	require.Len(t, errs, 12)
	for _, err := range errs {
		require.Equal(t, "QUOTA_EXCEEDED", codeOf(t, err))
	}
	require.Equal(t, int64(8), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))
}

// TestQuotaPostgresReleaseCumulative FAIL-10/FAIL-11/FAIL-12/FAIL-13：累计释放语义。
func TestQuotaPostgresReleaseCumulative(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_release", 8)

	res, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "50000000-0000-4000-8000-000000000001", 2))
	require.NoError(t, err)
	charge := chargeOf(t, c, res.OperationID)

	// 先累计 2。
	res1, err := repo.Release(context.Background(), releaseInput(res.OperationID, charge.ChargeID, "60000000-0000-4000-8000-000000000001", QuotaCodeOwnerLab, 2))
	require.NoError(t, err)
	require.Len(t, res1, 1)
	require.Equal(t, int64(2), res1[0].ReleasedTotal)
	require.Equal(t, int64(0), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))

	// 后到旧累计 1：no-op，保持 2。
	res2, err := repo.Release(context.Background(), releaseInput(res.OperationID, charge.ChargeID, "60000000-0000-4000-8000-000000000002", QuotaCodeOwnerLab, 1))
	require.NoError(t, err)
	require.Equal(t, int64(2), res2[0].ReleasedTotal)
	require.Equal(t, int64(0), res2[0].AppliedDelta)
	require.Equal(t, int64(0), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))

	// 相同 eventId 不同内容：冲突。
	conflictRelease := releaseInput(res.OperationID, charge.ChargeID, "60000000-0000-4000-8000-000000000001", QuotaCodeOwnerLab, 1)
	conflictRelease.PayloadHash = "phash-different"
	_, err = repo.Release(context.Background(), conflictRelease)
	require.Error(t, err)
	require.Equal(t, "QUOTA_RELEASE_CONFLICT", grpcCodeReason(t, err))

	// released_total 超 original：拒绝，无负数。
	_, err = repo.Release(context.Background(), releaseInput(res.OperationID, charge.ChargeID, "60000000-0000-4000-8000-000000000003", QuotaCodeOwnerLab, 3))
	require.Error(t, err)

	// 未知 charge：NotFound。
	_, err = repo.Release(context.Background(), releaseInput(res.OperationID, "99999999-9999-4999-8999-999999999999", "60000000-0000-4000-8000-000000000004", QuotaCodeOwnerLab, 1))
	require.Error(t, err)
	require.Equal(t, "QUOTA_RELEASE_NOT_FOUND", grpcCodeReason(t, err))

	// 错误 owner（同租户的其他服务名）：PermissionDenied。
	_, err = repo.Release(context.Background(), releaseInput(res.OperationID, charge.ChargeID, "60000000-0000-4000-8000-000000000005", "other-owner", 1))
	require.Error(t, err)
	require.Equal(t, "QUOTA_RELEASE_DENIED", grpcCodeReason(t, err))
}

// TestQuotaPostgresCancelUnsent FAIL-15 的 repo 侧：QUEUED 撤销退额；已发送后拒绝。
func TestQuotaPostgresCancelUnsent(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_cancel", 8)

	res, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "70000000-0000-4000-8000-000000000001", 2))
	require.NoError(t, err)
	require.Equal(t, int64(2), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))

	require.NoError(t, repo.CancelUnsent(context.Background(), tenantId, res.OperationID))
	require.Equal(t, int64(0), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))

	ctx := appViewer.NewSystemViewerContext(context.Background())
	op, err := c.Client().QuotaOperation.Query().Where(quotaoperation.OperationIDEQ(res.OperationID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, quotaoperation.DispatchStateCanceledUnsent, op.DispatchState)

	// 已尝试发送（DISPATCHING + attempt=1）后：撤销拒绝。
	res2, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "70000000-0000-4000-8000-000000000002", 1))
	require.NoError(t, err)
	_, err = c.Client().QuotaOperation.Update().Where(quotaoperation.OperationIDEQ(res2.OperationID)).
		SetDispatchState(quotaoperation.DispatchStateDispatching).
		AddAttemptCount(1).
		Save(ctx)
	require.NoError(t, err)
	require.Error(t, repo.CancelUnsent(context.Background(), tenantId, res2.OperationID))
	require.Equal(t, int64(1), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))
}

// TestQuotaPostgresCompositeFK DB-04：直接 SQL 跨租户关联被复合 FK 拒绝。
func TestQuotaPostgresCompositeFK(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantA, _ := seedTenantPlan(t, c, "pg_fk_a", 8)
	tenantB, _ := seedTenantPlan(t, c, "pg_fk_b", 8)

	resA, err := repo.Occupy(context.Background(), occupyInput(c, tenantA, "7", "80000000-0000-4000-8000-000000000001", 1))
	require.NoError(t, err)

	// 用 tenant B 的身份插入指向 A 操作的 charge：复合 FK (tenant_id, operation_id) 拒绝。
	op := opByOperationID(t, c, resA.OperationID)
	_, err = c.DB().ExecContext(context.Background(),
		quotaPGSQL("historical_11.sql"),
		tenantB, "55555555-5555-4555-8555-555555555555", op.OperationID, QuotaCodeGpuCount)
	require.Error(t, err, "跨租户 charge 必须被复合 FK 拒绝")

	// 同理：receipt 指向不存在租户归属（tenant FK RESTRICT）。
	_, err = c.DB().ExecContext(context.Background(),
		quotaPGSQL("historical_12.sql"),
		424242, "66666666-6666-4666-8666-666666666666", QuotaCodeOwnerLab, "70000000-0000-4000-8000-000000000099")
	require.Error(t, err, "不存在的租户必须被 tenant FK 拒绝")
}

// TestQuotaPostgresTenantDeleteProtection POL-06：有账本历史的租户删除 409。
func TestQuotaPostgresTenantDeleteProtection(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_del_protect", 8)

	_, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "90000000-0000-4000-8000-000000000001", 1))
	require.NoError(t, err)

	tenantRepo := newTenantRepoForLedgerTest(c)
	err = tenantRepo.Delete(appViewer.NewSystemViewerContext(context.Background()), deleteTenantReq(tenantId))
	require.Error(t, err)
	require.Equal(t, "QUOTA_HISTORY_PRESENT", codeOf(t, err))

	// 数据保留。
	require.Equal(t, int64(1), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))
}

// TestQuotaPostgresInvariants INV-01：恒等式重算。
func TestQuotaPostgresInvariants(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_invariant", 8)

	res, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "a0000000-0000-4000-8000-000000000001", 3))
	require.NoError(t, err)
	charge := chargeOf(t, c, res.OperationID)
	_, err = repo.Release(context.Background(), releaseInput(res.OperationID, charge.ChargeID, "b0000000-0000-4000-8000-000000000001", QuotaCodeOwnerLab, 1))
	require.NoError(t, err)

	rows, err := repo.RecomputeInvariants(context.Background(), tenantId)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Balanced, "occupied=2 应等于 SUM(original-released)=3-1")
	require.Equal(t, int64(2), rows[0].OccupiedUnits)
	require.Equal(t, int64(2), rows[0].ChargeRemainder)
}

// TestQuotaPostgresExpiredTenant POL-04 侧：到期租户新占额立即拒绝。
func TestQuotaPostgresExpiredTenant(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_expired", 8)

	ctx := appViewer.NewSystemViewerContext(context.Background())
	past := time.Now().Add(-time.Hour)
	_, err := c.Client().Tenant.Update().Where(tenant.IDEQ(tenantId)).SetNillableExpiredAt(&past).Save(ctx)
	require.NoError(t, err)

	_, err = repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "c0000000-0000-4000-8000-000000000001", 1))
	require.Error(t, err)
	require.Equal(t, "QUOTA_ADMISSION_DENIED", codeOf(t, err))
}

// ── 小工具 ───────────────────────────────────────────────────

func opByOperationID(t *testing.T, c *entCrud.EntClient[*ent.Client], opID string) *ent.QuotaOperation {
	t.Helper()
	ctx := appViewer.NewSystemViewerContext(context.Background())
	op, err := c.Client().QuotaOperation.Query().Where(quotaoperation.OperationIDEQ(opID)).Only(ctx)
	require.NoError(t, err)
	return op
}

func grpcCodeReason(t *testing.T, err error) string {
	t.Helper()
	// gRPC status 错误：断言其 reason 前缀。
	require.Error(t, err)
	return statusReason(err)
}

func nilSafe(s string) *string { return &s }

func ptrStr(s string) *string { return &s }

// ── 残留补齐：CON-04/05、FAIL-13/15/16/18、EXT-01、POL-01/02/03/05 ──

// TestQuotaPostgresMultiVector CON-04：多配额向量第二项不足 → 第一项也不扣。
func TestQuotaPostgresMultiVector(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, planId := seedTenantPlan(t, c, "pg_multi_vector", 8)

	// 第二个配额项 storage.bytes 上限 1。
	ctx := appViewer.NewSystemViewerContext(context.Background())
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(planId).
		SetQuotaCode(QuotaCodeStorage).SetQuotaValue(1).Exec(ctx))

	in := occupyInput(c, tenantId, "7", "d1000000-0000-4000-8000-000000000001", 1)
	in.Items = append(in.Items, QuotaOccupyItem{QuotaCode: QuotaCodeStorage, Units: 5})
	_, err := repo.Occupy(context.Background(), in)
	require.Error(t, err)
	require.Equal(t, "QUOTA_EXCEEDED", codeOf(t, err))
	// 第一项不能被扣。
	cnt, err := c.Client().QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(tenantId)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, cnt, "no account rows may remain after atomic rollback")
}

// TestQuotaPostgresTwoInstances CON-05：两个 repo 实例（两套连接）并发共享 PG 不超额。
func TestQuotaPostgresTwoInstances(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repoA := newTestRepo(c)
	db2, err := sql.Open("pgx", os.Getenv("ANI_TEST_DATABASE_DSN"))
	require.NoError(t, err)
	defer func() { _ = db2.Close() }()
	drv2 := entsql.OpenDB("postgres", db2)
	defer func() { drv2.Close() }()
	client2 := ent.NewClient(ent.Driver(drv2))
	c2 := entCrud.NewEntClient(client2, drv2)
	repoB := &QuotaLedgerRepo{entClient: c2, log: testLogger()}

	tenantId, _ := seedTenantPlan(t, c, "pg_two_instances", 8)

	var mu sync.Mutex
	accepted := 0
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			in := occupyInput(c, tenantId, fmt.Sprintf("%d", 200+n), fmt.Sprintf("e1000000-0000-4000-8000-%012d", n), 1)
			in.ResourceID = fmt.Sprintf("f1000000-0000-4000-8000-%012d", n)
			var r *QuotaOccupyResult
			var err error
			if n%2 == 0 {
				r, err = repoA.Occupy(context.Background(), in)
			} else {
				r, err = repoB.Occupy(context.Background(), in)
			}
			_ = r
			mu.Lock()
			if err == nil {
				accepted++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	require.Equal(t, 8, accepted, "row locks must serialize across instances")
	require.Equal(t, int64(8), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))
}

// TestQuotaPostgresReleaseBeforeAck FAIL-13：退额先于 ACK 到达 → 合法处理，
// 之后 ACK 不复活占额。
func TestQuotaPostgresReleaseBeforeAck(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_release_before_ack", 8)

	res, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "d2000000-0000-4000-8000-000000000001", 2))
	require.NoError(t, err)
	charge := chargeOf(t, c, res.OperationID)

	// 模拟 worker 已领取（DISPATCHING，未写 ACK）。
	ctx := appViewer.NewSystemViewerContext(context.Background())
	_, err = c.Client().QuotaOperation.Update().Where(quotaoperation.OperationIDEQ(res.OperationID)).
		SetDispatchState(quotaoperation.DispatchStateDispatching).AddAttemptCount(1).
		SetLeaseGeneration(1).Save(ctx)
	require.NoError(t, err)

	// 退额先到：合法处理（退额不检查投递状态）。
	_, err = repo.Release(context.Background(), releaseInput(res.OperationID, charge.ChargeID,
		"d3000000-0000-4000-8000-000000000001", QuotaCodeOwnerLab, 2))
	require.NoError(t, err)
	require.Equal(t, int64(0), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))

	// 迟到 ACK：写回成功但占额保持 0（不复活）。
	ok, err := repo.AckDispatched(context.Background(), tenantId, res.OperationID, 1, `{}`)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(0), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))
}

// TestQuotaPostgresCancelClaimRace FAIL-15：QUEUED 撤销与 worker 领取竞争。
// 只允许两种结果：CANCELED_UNSENT（已全额退额）或已被领取（attempt>=1、占额保持）。
func TestQuotaPostgresCancelClaimRace(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_cancel_claim_race", 8)

	for i := 0; i < 10; i++ {
		res, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7",
			fmt.Sprintf("d4%08d-0000-4000-8000-000000000001", i), 1))
		require.NoError(t, err)

		var wg sync.WaitGroup
		raceErr := make(chan error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); raceErr <- repo.CancelUnsent(context.Background(), tenantId, res.OperationID) }()
		go func() {
			defer wg.Done()
			ops, err := repo.ClaimDispatchable(context.Background(), "w-race", 15*time.Second, 8)
			claimed := false
			for _, op := range ops {
				if op.OperationID == res.OperationID {
					claimed = true
				}
			}
			_ = claimed
			raceErr <- err
		}()
		wg.Wait()
		close(raceErr)
		for err := range raceErr {
			if err != nil {
				// CancelUnsent 在已领取后返回 INVALID_QUOTA_REQUEST：合法结果之一。
				require.Equal(t, "INVALID_QUOTA_REQUEST", codeOf(t, err))
			}
		}

		ctx := appViewer.NewSystemViewerContext(context.Background())
		op, err := c.Client().QuotaOperation.Query().
			Where(quotaoperation.OperationIDEQ(res.OperationID)).Only(ctx)
		require.NoError(t, err)
		occ := accountOccupied(t, c, tenantId, QuotaCodeGpuCount)
		if op.DispatchState == quotaoperation.DispatchStateCanceledUnsent {
			// 撤销成功：charge 必须全额退回（本例 charge=1 已退）。
			ch := chargeOf(t, c, res.OperationID)
			require.Equal(t, ch.OriginalUnits, ch.ReleasedUnits, "cancel must fully refund")
		} else {
			require.GreaterOrEqual(t, op.AttemptCount, 1, "claimed operation must show attempt")
		}
		_ = occ
		// 清理本轮账本供下一轮。
		_, _ = c.DB().ExecContext(ctx, quotaPGSQL("historical_13.sql"))
	}
}

// TestQuotaPostgresLeaseGenerationGuard FAIL-18：租约接管后旧 worker 迟到回写被拒。
func TestQuotaPostgresLeaseGenerationGuard(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_lease_gen", 8)

	res, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "d5000000-0000-4000-8000-000000000001", 1))
	require.NoError(t, err)

	// worker A 领取（generation 1）。
	opsA, err := repo.ClaimDispatchable(context.Background(), "worker-a", time.Nanosecond, 8)
	require.NoError(t, err)
	var genA int64 = -1
	for _, op := range opsA {
		if op.OperationID == res.OperationID {
			genA = op.LeaseGeneration
		}
	}
	require.Equal(t, int64(1), genA)

	// 租约立即到期 → worker B 接管（generation 2）。
	time.Sleep(50 * time.Millisecond)
	opsB, err := repo.ClaimDispatchable(context.Background(), "worker-b", 15*time.Second, 8)
	require.NoError(t, err)
	var genB int64 = -1
	for _, op := range opsB {
		if op.OperationID == res.OperationID {
			genB = op.LeaseGeneration
		}
	}
	require.Equal(t, int64(2), genB, "takeover must increment generation")

	// 旧 worker 迟到回写：被 generation 检查拒绝。
	ok, err := repo.AckDispatched(context.Background(), tenantId, res.OperationID, genA, `{}`)
	require.NoError(t, err)
	require.False(t, ok, "stale generation writeback must be rejected")
	_, err = repo.MarkUnknown(context.Background(), tenantId, res.OperationID, genA, time.Now(), "STALE", false)
	require.NoError(t, err)
	okU, _ := repo.MarkUnknown(context.Background(), tenantId, res.OperationID, genA, time.Now(), "STALE", false)
	require.False(t, okU, "stale unknown writeback must be rejected")

	// 新 worker ACK：成功，且状态不会被旧回写复活。
	ok, err = repo.AckDispatched(context.Background(), tenantId, res.OperationID, genB, `{"accepted":true}`)
	require.NoError(t, err)
	require.True(t, ok)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	op, err := c.Client().QuotaOperation.Query().Where(quotaoperation.OperationIDEQ(res.OperationID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, quotaoperation.DispatchStateAcked, op.DispatchState)
}

// TestQuotaPostgresStorageUnavailable FAIL-16：数据库不可用 → 503，不转发。
func TestQuotaPostgresStorageUnavailable(t *testing.T) {
	// 指向已关闭端口：构造成功（懒连接），Occupy 必须返回 QUOTA_STORAGE_UNAVAILABLE。
	deadDSN := "postgres://postgres@127.0.0.1:1/dead?sslmode=disable&connect_timeout=1"
	db, err := sql.Open("pgx", deadDSN)
	require.NoError(t, err)
	drv := entsql.OpenDB("postgres", db)
	client := ent.NewClient(ent.Driver(drv))
	t.Cleanup(func() { _ = client.Close() })
	ec := entCrud.NewEntClient(client, drv)
	repo := NewQuotaLedgerRepo(testutil.NewBootstrapContext(nil), ec)

	_, err = repo.Occupy(context.Background(), occupyInputForResourceTenant(1, "11111111-1111-4111-8111-111111111111", "7", "d6000000-0000-4000-8000-000000000001", 1))
	require.Error(t, err)
	require.Equal(t, "QUOTA_STORAGE_UNAVAILABLE", codeOf(t, err))
}

// TestQuotaPostgresSyntheticItem EXT-01：测试内注册第二个合成 CONCURRENT 项，
// 走同一账本，无新增资源专用表/核心 switch。
func TestQuotaPostgresSyntheticItem(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, planId := seedTenantPlan(t, c, "pg_synthetic", 8)

	// 合成目录项 + 政策（仅测试事务内，不进生产目录）。
	ctx := appViewer.NewSystemViewerContext(context.Background())
	exist, derr := c.Client().QuotaDefinition.Query().
		Where(quotadefinition.CodeEQ("test.synthetic")).Exist(ctx)
	require.NoError(t, derr)
	if !exist {
		require.NoError(t, c.Client().QuotaDefinition.Create().SetCode("test.synthetic").
			SetDisplayName("Synthetic").SetUnit("thing").
			SetAccountingKind(quotadefinition.AccountingKindConcurrent).Exec(ctx))
	}
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(planId).
		SetQuotaCode("test.synthetic").SetQuotaValue(2).Exec(ctx))

	in := occupyInput(c, tenantId, "7", "d7000000-0000-4000-8000-000000000001", 1)
	in.Items = []QuotaOccupyItem{
		{QuotaCode: QuotaCodeGpuCount, Units: 1},
		{QuotaCode: "test.synthetic", Units: 2},
	}
	res, err := repo.Occupy(context.Background(), in)
	require.NoError(t, err)
	require.Len(t, res.ChargeIDs, 2)
	require.Equal(t, int64(2), accountOccupied(t, c, tenantId, "test.synthetic"))

	// 释放走同一条累计释放路径（无资源专用逻辑）。
	ch, err := c.Client().QuotaCharge.Query().Where(
		quotacharge.TenantIDEQ(tenantId), quotacharge.QuotaCodeEQ("test.synthetic")).Only(ctx)
	require.NoError(t, err)
	_, err = repo.Release(context.Background(), &QuotaReleaseInput{
		OwnerService: QuotaCodeOwnerLab, ReleaseEventID: "d8000000-0000-4000-8000-000000000001",
		OperationID: res.OperationID, Reason: "RESOURCE_RELEASED",
		PayloadHash: "ph", PayloadJSON: `{}`,
		Items: []QuotaReleaseItemInput{{ChargeID: ch.ChargeID, QuotaCode: "test.synthetic", ReleasedTotal: 2}},
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), accountOccupied(t, c, tenantId, "test.synthetic"))
}

// TestQuotaPostgresPolicyChanges POL-01/02/03：降额、并发降额、换套餐/删政策项。
func TestQuotaPostgresPolicyChanges(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, planId := seedTenantPlan(t, c, "pg_policy", 8)

	// 占 6。
	for i := 0; i < 6; i++ {
		in := occupyInput(c, tenantId, "7", fmt.Sprintf("d9%08d-0000-4000-8000-000000000001", i), 1)
		in.ResourceID = fmt.Sprintf("da%08d-0000-4000-8000-000000000001", i)
		_, err := repo.Occupy(context.Background(), in)
		require.NoError(t, err)
	}
	require.Equal(t, int64(6), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))

	// POL-01：上限降到 4 → available=0，新申请拒绝，不自动退额。
	ctx := appViewer.NewSystemViewerContext(context.Background())
	policy, err := c.Client().PlanQuota.Query().Where(planquota.HasPlanWith(plan.IDEQ(planId)), planquota.QuotaCodeEQ(QuotaCodeGpuCount)).Only(ctx)
	require.NoError(t, err)
	policyRepo := newPlanQuotaRepoPostgres(t, c)
	setLimit := func(value uint64) error {
		return policyRepo.Update(ctx, &identityV1.UpdatePlanQuotaRequest{Id: policy.ID, Data: &identityV1.PlanQuota{QuotaValue: &value}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"quota_value"}}})
	}
	err = setLimit(4)
	require.NoError(t, err)
	_, err = repo.Occupy(context.Background(), occupyInput(c, tenantId, "7",
		"db000000-0000-4000-8000-000000000001", 1))
	require.Error(t, err)
	require.Equal(t, "QUOTA_EXCEEDED", codeOf(t, err))
	require.Equal(t, int64(6), accountOccupied(t, c, tenantId, QuotaCodeGpuCount), "no auto eviction")
	adminRepo := &QuotaAdminRepo{entClient: c}
	views, err := adminRepo.ListTenantAccounts(context.Background(), tenantId)
	require.NoError(t, err)
	found := false
	for _, v := range views {
		if v.QuotaCode == QuotaCodeGpuCount {
			found = true
			require.Equal(t, "4", v.Limit)
			require.Equal(t, "6", v.Occupied)
			require.Equal(t, "0", v.Available)
			require.True(t, v.OverLimit)
		}
	}
	require.True(t, found)

	// POL-02：降额与新占额并发 —— 数据库提交顺序串行化，
	// 最终占用必须 ≤ max(旧限,新限) 且恒等式成立。
	var wg sync.WaitGroup
	require.NoError(t, setLimit(8))
	lowered := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		lowered <- setLimit(7)
	}()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			in := occupyInput(c, tenantId, fmt.Sprintf("%d", 300+n), fmt.Sprintf("dc%08d-0000-4000-8000-000000000001", n), 1)
			in.ResourceID = fmt.Sprintf("dd%08d-0000-4000-8000-000000000001", n)
			_, _ = repo.Occupy(context.Background(), in)
		}(i)
	}
	wg.Wait()
	require.NoError(t, <-lowered)
	occ := accountOccupied(t, c, tenantId, QuotaCodeGpuCount)
	require.LessOrEqual(t, occ, int64(8), "no admission beyond any committed limit")
	rows, err := repo.RecomputeInvariants(context.Background(), tenantId)
	require.NoError(t, err)
	for _, r := range rows {
		require.True(t, r.Balanced)
	}

	// POL-03：换套餐（绑定另一套餐）余额保留，按新政策拒绝或放行。
	newPlan, err := c.Client().Plan.Create().SetNillableName(ptrStr("pg_policy_plan2")).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(newPlan.ID).
		SetQuotaCode(QuotaCodeGpuCount).SetQuotaValue(2).Exec(ctx))
	_, err = c.Client().Tenant.Update().Where(tenant.IDEQ(tenantId)).SetNillablePlanID(&newPlan.ID).Save(ctx)
	require.NoError(t, err)
	require.Equal(t, occ, accountOccupied(t, c, tenantId, QuotaCodeGpuCount), "switch plan must not clear balance")
	// occupied 6 > 新限 2：拒绝新增（over-limit 状态下 available=0）。
	_, err = repo.Occupy(context.Background(), occupyInput(c, tenantId, "7",
		"de000000-0000-4000-8000-000000000001", 1))
	require.Error(t, err)
	require.Equal(t, "QUOTA_EXCEEDED", codeOf(t, err))

	// 删除套餐配额项 → QUOTA_NOT_CONFIGURED（缺项=不允许，不是无限）。
	_, err = c.Client().PlanQuota.Delete().Where(
		planquota.HasPlanWith(plan.IDEQ(newPlan.ID)), planquota.QuotaCodeEQ(QuotaCodeGpuCount)).Exec(ctx)
	require.NoError(t, err)
	_, err = repo.Occupy(context.Background(), occupyInput(c, tenantId, "7",
		"df000000-0000-4000-8000-000000000001", 1))
	require.Error(t, err)
	require.Equal(t, "QUOTA_NOT_CONFIGURED", codeOf(t, err))
}

// TestQuotaPostgresReleaseAfterExpiry POL-05：到期/降额后可信 owner 仍能退额。
func TestQuotaPostgresReleaseAfterExpiry(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, _ := seedTenantPlan(t, c, "pg_release_expired", 8)

	res, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "e0000000-0000-4000-8000-000000000001", 2))
	require.NoError(t, err)
	charge := chargeOf(t, c, res.OperationID)

	// 租户到期。
	ctx := appViewer.NewSystemViewerContext(context.Background())
	past := time.Now().Add(-time.Hour)
	_, err = c.Client().Tenant.Update().Where(tenant.IDEQ(tenantId)).SetNillableExpiredAt(&past).Save(ctx)
	require.NoError(t, err)

	// 新占额被拒（到期）。
	_, err = repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "e1000000-0000-4000-8000-000000000001", 1))
	require.Equal(t, "QUOTA_ADMISSION_DENIED", codeOf(t, err))

	// 但内部退额不受到期阻断。
	_, err = repo.Release(context.Background(), releaseInput(res.OperationID, charge.ChargeID,
		"e2000000-0000-4000-8000-000000000001", QuotaCodeOwnerLab, 2))
	require.NoError(t, err)
	require.Equal(t, int64(0), accountOccupied(t, c, tenantId, QuotaCodeGpuCount))
}

// TestQuotaPostgresZeroLimit AUTH-04 侧：额度为 0 拒绝新增，不视为无限。
func TestQuotaPostgresZeroLimit(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenantId, planId := seedTenantPlan(t, c, "pg_zero_limit", 0)

	_, err := repo.Occupy(context.Background(), occupyInput(c, tenantId, "7", "e3000000-0000-4000-8000-000000000001", 1))
	require.Error(t, err)
	require.Equal(t, "QUOTA_EXCEEDED", codeOf(t, err))
	_ = planId
}

//go:embed testdata/quota/*.sql
var quotaPGSources embed.FS

func quotaPGSQL(name string) string {
	contents, err := quotaPGSources.ReadFile("testdata/quota/" + name)
	if err != nil {
		panic(err)
	}
	return string(contents)
}

// CPU commands reuse the durable queue but cannot manufacture GPU quota facts.
func TestQuotaPostgresInstanceCPUCommands(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tid, _ := seedTenantPlan(t, c, "cpu-command", 8)
	input := occupyInput(c, tid, "7", uuid.NewString(), 1)
	business := &res.CreateSpec{InstanceContractVersion: 2, WorkspaceMountPath: "/workspace", Name: "cpu", ImageId: "img_" + strings.Repeat("a", 32), ImageScope: "platform", PresetId: "20000000-0000-4000-8000-000000000001", InstanceKind: "container", ComputeType: "cpu", Scheduling: &res.SchedulingOptions{PodPlacement: res.SchedulingOptions_NONE.Enum()}, ClusterId: uuid.NewString(), Namespace: "tenant-cpu", ImageReference: "registry.invalid/cpu@sha256:" + strings.Repeat("a", 64), Cpu: "1", Memory: "1Gi", ServiceAccount: "workload", StorageClass: "workspace", WorkspaceSize: "1Gi", Shell: "/bin/bash", Uid: 1000, Gid: 1000}

	raw, e := res.CanonicalBusinessPayload(business)
	require.NoError(t, e)
	digest, e := res.BusinessPayloadDigest(business)
	require.NoError(t, e)
	frozen := InstanceCPUCanonical{Schema: InstanceCPUCanonicalSchema, InstanceContractVersion: 2, BusinessPayload: raw, BusinessPayloadDigest: digest}
	canonical, e := MarshalInstanceCPUCanonical(&frozen)
	require.NoError(t, e)
	input.OwnerService, input.Action, input.CanonicalRequest, input.Items = instanceCPUOwner, instanceCPUCreateAction, string(canonical), nil
	input.InstanceContractVersion = CurrentInstanceContractVersion
	first, e := repo.Occupy(context.Background(), input)
	require.NoError(t, e)
	require.Empty(t, first.ChargeIDs)
	replay, e := repo.Occupy(context.Background(), input)
	require.NoError(t, e)
	require.True(t, replay.Replayed)
	require.Equal(t, first.OperationID, replay.OperationID)
	for _, alter := range []func(*QuotaOccupyInput){
		func(i *QuotaOccupyInput) { i.OwnerService = "ani-inference" },
		func(i *QuotaOccupyInput) { i.Action = "CPU_CREATE" },
		func(i *QuotaOccupyInput) { i.Items = []QuotaOccupyItem{{QuotaCode: QuotaCodeGpuCount, Units: 1}} },
		func(i *QuotaOccupyInput) { i.CanonicalRequest = `{"schema":"unknown"}` },
	} {
		bad := *input
		bad.IdempotencyKey = uuid.NewString()
		alter(&bad)
		_, e = repo.Occupy(context.Background(), &bad)
		require.Error(t, e, "zero charges are restricted to compiled CPU instance commands")
	}
	claimed, e := repo.ClaimDispatchable(context.Background(), "cpu-worker", time.Minute, 1)
	require.NoError(t, e)
	require.Len(t, claimed, 1)
	require.Empty(t, claimed[0].Charges)
	acked, e := repo.AckDispatched(context.Background(), tid, first.OperationID, claimed[0].LeaseGeneration, `{"accepted":true}`)
	require.NoError(t, e)
	require.True(t, acked)
	frozen.DeleteExpectedVersion = 4
	deletionJSON, e := MarshalInstanceCPUCanonical(&frozen)
	require.NoError(t, e)
	deletion := &QuotaDeleteInput{TenantID: tid, ActorType: input.ActorType, ActorID: input.ActorID, OwnerService: instanceCPUOwner, Action: instanceCPUDeleteAction, IdempotencyKey: uuid.NewString(), RequestHash: "cpu-delete-v4", CanonicalRequest: string(deletionJSON), ResourceID: first.ResourceID}
	deleted, e := repo.CreateDeleteOperation(context.Background(), deletion)
	require.NoError(t, e)
	require.False(t, deleted.LocalCanceled)
	claimed, e = repo.ClaimDispatchable(context.Background(), "cpu-delete-worker", time.Minute, 1)
	require.NoError(t, e)
	require.Len(t, claimed, 1)
	require.Equal(t, first.OperationID, claimed[0].CreateOperationID)
	require.Equal(t, string(deletionJSON), claimed[0].CanonicalRequest)
	require.Empty(t, claimed[0].Charges)
	again, e := repo.CreateDeleteOperation(context.Background(), deletion)
	require.NoError(t, e)
	require.True(t, again.Replayed)
	require.Equal(t, deleted.OperationID, again.OperationID)
	unsent := *input
	unsent.ResourceID, unsent.IdempotencyKey = uuid.NewString(), uuid.NewString()
	queued, e := repo.Occupy(context.Background(), &unsent)
	require.NoError(t, e)
	canceled := *deletion
	canceled.ResourceID, canceled.IdempotencyKey = queued.ResourceID, uuid.NewString()
	canceled.RequestHash = "cpu-cancel-unsent"
	out, e := repo.CreateDeleteOperation(context.Background(), &canceled)
	require.NoError(t, e)
	require.True(t, out.LocalCanceled)
	require.Empty(t, out.ChargeIDs)
	candidates, e := repo.ScanGpuCreatePage(context.Background(), 0, 100)
	require.NoError(t, e)
	require.Empty(t, candidates)
}
