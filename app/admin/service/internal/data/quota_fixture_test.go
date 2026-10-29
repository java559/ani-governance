//go:build quota_pg

package data

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/planquota"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"google.golang.org/grpc/status"
)

const QuotaCodeOwnerLab = "ani-gpu-simulator"

// newLedgerPGClient 连接任务隔离 PostgreSQL（已迁移）。
func newLedgerPGClient(t *testing.T) *entCrud.EntClient[*ent.Client] {
	t.Helper()
	dsn := os.Getenv("ANI_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Fatal("ANI_TEST_DATABASE_DSN is required for quota_pg tests (missing DSN must fail, not skip)")
	}
	if os.Getenv("ANI_TEST_DATABASE_EXCLUSIVE") != "1" {
		t.Fatal("ANI_TEST_DATABASE_EXCLUSIVE=1 is required for selected quota_pg tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("connect quota pg: %v", err)
	}
	drv := entsql.OpenDB("postgres", db)
	client := ent.NewClient(ent.Driver(drv))
	t.Cleanup(func() { _ = client.Close() })
	return entCrud.NewEntClient(client, drv)
}

// cleanLedger 清空账本表，保证测试互不干扰。
func cleanLedger(t *testing.T, c *entCrud.EntClient[*ent.Client]) {
	t.Helper()
	if os.Getenv("ANI_TEST_DATABASE_EXCLUSIVE") != "1" {
		t.Fatal("ledger fixture cleanup requires ANI_TEST_DATABASE_EXCLUSIVE=1")
	}
	ctx := context.Background()
	for _, stmt := range []string{
		quotaPGSQL("sandbox_guarded_deletes_cleanup.sql"),
		quotaPGSQL("sandbox_parent_intents_cleanup.sql"),
		quotaPGSQL("sandbox_parent_checks_cleanup.sql"),
		quotaPGSQL("historical_01.sql"),
		quotaPGSQL("historical_02.sql"),
		quotaPGSQL("historical_03.sql"),
		quotaPGSQL("historical_04.sql"),
		quotaPGSQL("historical_05.sql"),
		quotaPGSQL("historical_06.sql"),
		quotaPGSQL("historical_07.sql"),
		quotaPGSQL("historical_08.sql"),
		quotaPGSQL("historical_09.sql"),
		quotaPGSQL("historical_10.sql"),
	} {
		if _, err := c.DB().ExecContext(ctx, stmt); err != nil {
			t.Fatalf("clean ledger %q: %v", stmt, err)
		}
	}
}

// seedTenantPlan 创建租户与套餐并设置 gpu.count 政策，返回 (tenantID, planID)。
func seedTenantPlan(t *testing.T, c *entCrud.EntClient[*ent.Client], name string, gpuLimit uint64) (uint32, uint32) {
	t.Helper()
	ctx := appViewer.NewSystemViewerContext(context.Background())
	plan, err := c.Client().Plan.Create().SetNillableName(nilSafe(name)).Save(ctx)
	require.NoError(t, err)
	tn, err := c.Client().Tenant.Create().
		SetName(name).
		SetCode(name).
		SetNillablePlanID(&plan.ID).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, c.Client().PlanQuota.Create().
		SetPlanID(plan.ID).
		SetQuotaCode(QuotaCodeGpuCount).
		SetQuotaValue(gpuLimit).
		Exec(ctx))
	return tn.ID, plan.ID
}

func newTenantRepoForLedgerTest(c *entCrud.EntClient[*ent.Client]) *TenantRepo {
	return NewTenantRepo(testutil.NewBootstrapContext(nil), c)
}

func testLogger() *bLogger.Helper {
	return bLogger.NewHelper(bLogger.NopLogger())
}

func statusReason(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return ""
	}
	return strings.SplitN(st.Message(), ":", 2)[0]
}

func deleteTenantReq(tenantId uint32) *identityV1.DeleteTenantRequest {
	return &identityV1.DeleteTenantRequest{
		QueryBy: &identityV1.DeleteTenantRequest_Id{Id: tenantId},
	}
}

func occupyInput(c *entCrud.EntClient[*ent.Client], tenantId uint32, actor string, key string, units int64) *QuotaOccupyInput {
	resourceTenantID := c.Client().Tenant.GetX(appViewer.NewSystemViewerContext(context.Background()), tenantId).ResourceTenantID
	return occupyInputForResourceTenant(tenantId, resourceTenantID, actor, key, units)
}

func occupyInputForResourceTenant(tenantId uint32, resourceTenantID, actor, key string, units int64) *QuotaOccupyInput {
	return &QuotaOccupyInput{
		TenantID:         tenantId,
		ResourceTenantID: resourceTenantID,
		ResourceID:       "22222222-2222-4222-8222-" + fmt.Sprintf("%012d", time.Now().UnixNano()%1_000_000_000_000)[:12],
		ActorType:        "user",
		ActorID:          actor,
		OwnerService:     QuotaCodeOwnerLab,
		Action:           "LAB_GPU_CREATE",
		IdempotencyKey:   key,
		RequestHash:      "hash-" + key,
		CanonicalRequest: `{"schema_version":1}`,
		Items:            []QuotaOccupyItem{{QuotaCode: QuotaCodeGpuCount, Units: units}},
	}
}

func newTestRepo(c *entCrud.EntClient[*ent.Client]) *QuotaLedgerRepo {
	return NewQuotaLedgerRepo(testutil.NewBootstrapContext(nil), c)
}

// tenantIDEQ / planquota 直接引用生成的谓词，避免多余封装。
var _ = planquota.FieldQuotaCode

func chargeOf(t *testing.T, c *entCrud.EntClient[*ent.Client], operationID string) *ent.QuotaCharge {
	t.Helper()
	ctx := appViewer.NewSystemViewerContext(context.Background())
	ch, err := c.Client().QuotaCharge.Query().Where(quotacharge.OperationIDEQ(operationID)).Only(ctx)
	require.NoError(t, err)
	return ch
}

func accountOccupied(t *testing.T, c *entCrud.EntClient[*ent.Client], tenantId uint32, code string) int64 {
	t.Helper()
	ctx := appViewer.NewSystemViewerContext(context.Background())
	acc, err := c.Client().QuotaAccount.Query().
		Where(quotaaccount.TenantIDEQ(tenantId), quotaaccount.QuotaCodeEQ(code)).
		Only(ctx)
	require.NoError(t, err)
	return acc.OccupiedUnits
}

func releaseInput(opID, chargeID, eventID, owner string, total int64) *QuotaReleaseInput {
	return &QuotaReleaseInput{
		OwnerService:   owner,
		ReleaseEventID: eventID,
		OperationID:    opID,
		Reason:         "RESOURCE_RELEASED",
		PayloadHash:    "phash-" + eventID,
		PayloadJSON:    `{"items":[...]}`,
		Items:          []QuotaReleaseItemInput{{ChargeID: chargeID, QuotaCode: QuotaCodeGpuCount, ReleasedTotal: total}},
	}
}
