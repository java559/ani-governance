//go:build quota_pg

package data

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planquota"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

// The observer wraps the real PostgreSQL driver, preserving transaction/query
// execution. It records SQL and injects one concurrent writer after COUNT so the
// actual production repository must keep page and associations on that snapshot.
type quotaSummaryObservedDriver struct {
	dialect.Driver
	queries    []string
	options    []sql.TxOptions
	afterCount func() error
}

func (d *quotaSummaryObservedDriver) BeginTx(ctx context.Context, options *sql.TxOptions) (dialect.Tx, error) {
	d.options = append(d.options, *options)
	tx, err := d.Driver.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	}).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &quotaSummaryObservedTx{Tx: tx, driver: d}, nil
}

type quotaSummaryObservedTx struct {
	dialect.Tx
	driver *quotaSummaryObservedDriver
}

func (tx *quotaSummaryObservedTx) Query(ctx context.Context, query string, args, value any) error {
	tx.driver.queries = append(tx.driver.queries, query)
	if err := tx.Tx.Query(ctx, query, args, value); err != nil {
		return err
	}
	if tx.driver.afterCount != nil && strings.Contains(query, "COUNT(") && strings.Contains(query, `"sys_tenants"`) {
		write := tx.driver.afterCount
		tx.driver.afterCount = nil
		return write()
	}
	return nil
}

func TestQuotaAccountSummariesConsistentReadAndBoundedQueries(t *testing.T) {
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	sys := appViewer.NewSystemViewerContext(context.Background())
	p, err := c.Client().Plan.Create().SetName("snapshot-plan").Save(sys)
	require.NoError(t, err)
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(p.ID).SetQuotaCode(commandcontract.GpuPhysicalQuotaCode).SetQuotaValue(10).Exec(sys))
	a, err := c.Client().Tenant.Create().SetName("snapshot-a").SetCode("snapshot-a").SetPlanID(p.ID).Save(sys)
	require.NoError(t, err)
	_, err = c.Client().Tenant.Create().SetName("snapshot-b").SetCode("snapshot-b").SetPlanID(p.ID).Save(sys)
	require.NoError(t, err)
	require.NoError(t, c.Client().QuotaAccount.Create().SetTenantID(a.ID).SetQuotaCode(commandcontract.GpuPhysicalQuotaCode).SetOccupiedUnits(2).Exec(sys))
	sqlDriver := entsql.OpenDB("postgres", c.DB())
	driver := &quotaSummaryObservedDriver{Driver: sqlDriver}
	client := ent.NewClient(ent.Driver(driver))
	repo := NewQuotaAdminRepo(testutil.NewBootstrapContext(nil), entCrud.NewEntClient(client, sqlDriver))
	driver.afterCount = func() error {
		return quotaTransaction(sys, c.Client(), func(tx *ent.Tx) error {
			if err := tx.Tenant.Update().Where(tenant.IDEQ(a.ID)).SetName("changed-a").Exec(sys); err != nil {
				return err
			}
			if err := tx.Plan.Update().Where(plan.IDEQ(p.ID)).SetName("changed-plan").Exec(sys); err != nil {
				return err
			}
			if err := tx.PlanQuota.Update().Where(planquota.HasPlanWith(plan.IDEQ(p.ID)), planquota.QuotaCodeEQ(commandcontract.GpuPhysicalQuotaCode)).SetQuotaValue(20).Exec(sys); err != nil {
				return err
			}
			if err := tx.QuotaAccount.Update().Where(quotaaccount.TenantIDEQ(a.ID), quotaaccount.QuotaCodeEQ(commandcontract.GpuPhysicalQuotaCode)).SetOccupiedUnits(7).Exec(sys); err != nil {
				return err
			}
			return tx.Tenant.Create().SetName("new-after-count").SetCode("new-after-count").SetPlanID(p.ID).Exec(sys)
		})
	}
	filter := TenantQuotaAccountSummaryFilter{Page: 1, PageSize: 100, QuotaCode: commandcontract.GpuPhysicalQuotaCode, IncludeTotals: true}
	page, err := repo.ListTenantAccountSummaryPage(sys, filter)
	require.NoError(t, err)
	items, total := page.Items, page.Total
	require.Nil(t, driver.afterCount, "concurrent writer ran after tenant count")
	require.EqualValues(t, 2, total)
	require.Len(t, items, 2, "new tenant must not enter the preexisting page snapshot")
	require.Equal(t, "snapshot-a", items[0].TenantName)
	require.Equal(t, "snapshot-plan", items[0].PlanName)
	require.Equal(t, "10", items[0].Accounts[0].Limit)
	require.Equal(t, "2", items[0].Accounts[0].Occupied)
	require.Equal(t, "8", items[0].Accounts[0].Available)
	require.Equal(t, "2", page.Summary.ConfiguredTenantCount)
	require.Equal(t, "20", page.Summary.Accounts[0].Limit)
	require.Equal(t, "2", page.Summary.Accounts[0].Occupied)
	require.Equal(t, "18", page.Summary.Accounts[0].Available, "aggregate cannot see the concurrent policy/account writes")
	require.Len(t, driver.queries, 7, "seven batched queries, independent of tenant page size")
	require.Equal(t, []sql.TxOptions{{Isolation: sql.LevelRepeatableRead, ReadOnly: true}}, driver.options)

	driver.queries = nil
	page, err = repo.ListTenantAccountSummaryPage(sys, filter)
	require.NoError(t, err)
	items, total = page.Items, page.Total
	require.EqualValues(t, 3, total)
	require.Len(t, items, 3)
	require.Equal(t, "changed-a", items[0].TenantName)
	require.Equal(t, "changed-plan", items[0].PlanName)
	require.Equal(t, "20", items[0].Accounts[0].Limit)
	require.Equal(t, "7", items[0].Accounts[0].Occupied)
	require.Equal(t, "60", page.Summary.Accounts[0].Limit)
	require.Equal(t, "7", page.Summary.Accounts[0].Occupied)
	require.Len(t, driver.queries, 7)
	for i := 0; i < 37; i++ {
		require.NoError(t, c.Client().Tenant.Create().SetName(fmt.Sprintf("batch-%02d", i)).SetCode(fmt.Sprintf("batch-%02d", i)).SetPlanID(p.ID).Exec(sys))
	}
	driver.queries = nil
	page, err = repo.ListTenantAccountSummaryPage(sys, filter)
	require.NoError(t, err)
	require.EqualValues(t, 40, page.Total)
	require.Len(t, page.Items, 40)
	require.Len(t, page.ResourceTenantIDs, 40, "runtime mappings use the same tenant page without extra queries")
	for _, item := range page.Items {
		require.NotEmpty(t, page.ResourceTenantIDs[item.TenantId])
	}
	require.Equal(t, "40", page.Summary.ConfiguredTenantCount)
	require.Equal(t, "800", page.Summary.Accounts[0].Limit)
	require.Len(t, driver.queries, 7, "larger tenant pages must not introduce N+1 reads")

	driver.queries = nil
	filter.IncludeTotals = false
	page, err = repo.ListTenantAccountSummaryPage(sys, filter)
	require.NoError(t, err)
	require.Nil(t, page.Summary)
	require.Len(t, driver.queries, 6, "GPU tenant monitoring callers do not request or compute full-scope totals")
}

func TestQuotaReadSnapshotPostgresRejectsWrites(t *testing.T) {
	c := testutil.NewQuotaPGClient(t)
	err := quotaReadSnapshot(context.Background(), c.Client(), func(tx *ent.Tx) error {
		return tx.Plan.Create().SetName("readonly-must-reject").Exec(appViewer.NewSystemViewerContext(context.Background()))
	})
	require.ErrorContains(t, err, "read-only transaction")
	_, err = c.Client().Plan.Query().Where(plan.NameEQ("readonly-must-reject")).Only(appViewer.NewSystemViewerContext(context.Background()))
	require.True(t, ent.IsNotFound(err), "a rejected readonly transaction must not persist a row")
}
