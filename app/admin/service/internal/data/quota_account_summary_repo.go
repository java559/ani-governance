package data

import (
	"context"
	"fmt"
	"strconv"

	entsql "entgo.io/ent/dialect/sql"
	adminpb "go-wind-admin/api/gen/go/admin/service/v1"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planquota"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/quotadefinition"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

type TenantQuotaAccountSummaryFilter struct {
	Page, PageSize uint32
	TenantID       *uint32
	QuotaCode      string
	IncludeTotals  bool
}

// TenantQuotaAccountSummaryPage keeps runtime identity mappings internal to
// Gov. The public quota messages never expose these cross-service identities.
type TenantQuotaAccountSummaryPage struct {
	Items             []*quotapb.TenantQuotaAccountSummary
	Total             uint64
	ResourceTenantIDs map[uint32]string
	Summary           *adminpb.TenantQuotaAccountTotals
}

// ListTenantAccountSummaries is a platform-only read projection. Authorization
// precedes this cross-tenant repository. Query count is bounded independently of
// page size: definitions, count, tenant page, plans, policies and accounts.
func (r *QuotaAdminRepo) ListTenantAccountSummaries(ctx context.Context, filter TenantQuotaAccountSummaryFilter) ([]*quotapb.TenantQuotaAccountSummary, uint64, error) {
	page, err := r.ListTenantAccountSummaryPage(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return page.Items, page.Total, nil
}

func (r *QuotaAdminRepo) ListTenantAccountSummaryPage(ctx context.Context, filter TenantQuotaAccountSummaryFilter) (page *TenantQuotaAccountSummaryPage, err error) {
	if filter.Page == 0 || filter.PageSize == 0 || filter.PageSize > 100 || (filter.TenantID != nil && *filter.TenantID == 0) {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid quota account page or tenant")
	}
	page = &TenantQuotaAccountSummaryPage{Items: []*quotapb.TenantQuotaAccountSummary{}, ResourceTenantIDs: map[uint32]string{}}
	err = quotaReadSnapshot(ctx, r.entClient.Client(), func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		defs, e := quotaSummaryDefinitions(ctx, tx, filter.QuotaCode)
		if e != nil {
			return e
		}
		query := tx.Tenant.Query().Where(tenant.IDGT(0), tenant.DeletedAtIsNil())
		if filter.TenantID != nil {
			query.Where(tenant.IDEQ(*filter.TenantID))
		}
		count, e := query.Clone().Count(ctx)
		if e != nil {
			return e
		}
		page.Total = uint64(count)
		if filter.IncludeTotals {
			page.Summary, e = quotaAccountTotals(ctx, tx, filter)
			if e != nil {
				return e
			}
		}
		offset := (uint64(filter.Page) - 1) * uint64(filter.PageSize)
		if offset >= page.Total {
			return nil
		}
		tenants, e := query.Order(ent.Asc(tenant.FieldID)).Offset(int(offset)).Limit(int(filter.PageSize)).All(ctx)
		if e != nil {
			return e
		}
		for _, tenant := range tenants {
			page.ResourceTenantIDs[tenant.ID] = tenant.ResourceTenantID
		}
		page.Items, e = r.quotaAccountSummaries(ctx, tx, tenants, defs)
		return e
	})
	return
}

// quotaAccountTotals aggregates in PostgreSQL rather than materializing every
// tenant's projection. Numeric sums avoid bigint/uint64 overflow, and the CTE
// shares the same read-only snapshot as the count and current tenant page.
func quotaAccountTotals(ctx context.Context, tx *ent.Tx, filter TenantQuotaAccountSummaryFilter) (*adminpb.TenantQuotaAccountTotals, error) {
	var tenantID any
	if filter.TenantID != nil {
		tenantID = *filter.TenantID
	}
	var rows []struct {
		QuotaCode             string `json:"quota_code"`
		Unit                  string `json:"unit"`
		ConfiguredTenantCount string `json:"configured_tenant_count"`
		Limit                 string `json:"quota_limit"`
		Occupied              string `json:"occupied"`
		Available             string `json:"available"`
		ConfiguredTotal       string `json:"configured_total"`
	}
	err := tx.QuotaDefinition.Query().Modify(func(s *entsql.Selector) {
		// Ent's modifier keeps this PostgreSQL-specific aggregate on the real
		// transaction driver, including cancellation and query observers.
		*s = *entsql.Dialect(s.Dialect()).SelectExpr(entsql.Expr(`* FROM (
WITH selected_tenants AS (
  SELECT id, plan_id FROM sys_tenants
  WHERE id > 0 AND deleted_at IS NULL AND ($1::bigint IS NULL OR id = $1)
), projection AS (
  SELECT d.code AS quota_code, d.unit, t.id AS tenant_id,
         pq.quota_value::numeric AS quota_limit,
         COALESCE(a.occupied_units, 0)::numeric AS occupied
  FROM sys_quota_definitions AS d
  LEFT JOIN selected_tenants AS t ON TRUE
  LEFT JOIN sys_plans AS p ON p.id = t.plan_id
  LEFT JOIN sys_plan_quotas AS pq ON pq.plan_id = p.id AND pq.quota_code = d.code
  LEFT JOIN sys_quota_accounts AS a ON a.tenant_id = t.id AND a.quota_code = d.code
  WHERE ($2::text = '' OR d.code = $2)
)
SELECT quota_code, unit,
       (COUNT(DISTINCT tenant_id) FILTER (WHERE quota_limit IS NOT NULL))::text AS configured_tenant_count,
       COALESCE(SUM(quota_limit), 0)::text AS quota_limit,
       COALESCE(SUM(occupied), 0)::text AS occupied,
       COALESCE(SUM(GREATEST(COALESCE(quota_limit, 0) - occupied, 0)), 0)::text AS available,
       (SELECT COUNT(DISTINCT tenant_id) FROM projection WHERE quota_limit IS NOT NULL)::text AS configured_total
FROM projection GROUP BY quota_code, unit ORDER BY quota_code
) AS quota_totals`, tenantID, filter.QuotaCode))
	}).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	summary := &adminpb.TenantQuotaAccountTotals{ConfiguredTenantCount: "0", Accounts: []*adminpb.QuotaAccountTotals{}}
	for _, row := range rows {
		summary.ConfiguredTenantCount = row.ConfiguredTotal
		summary.Accounts = append(summary.Accounts, &adminpb.QuotaAccountTotals{
			QuotaCode: row.QuotaCode, Unit: row.Unit, ConfiguredTenantCount: row.ConfiguredTenantCount,
			Limit: row.Limit, Occupied: row.Occupied, Available: row.Available,
		})
	}
	return summary, nil
}

func quotaSummaryDefinitions(ctx context.Context, tx *ent.Tx, code string) ([]*ent.QuotaDefinition, error) {
	query := tx.QuotaDefinition.Query().Order(ent.Asc(quotadefinition.FieldCode))
	if code != "" {
		query.Where(quotadefinition.CodeEQ(code))
	}
	defs, err := query.All(ctx)
	if err == nil && code != "" && len(defs) == 0 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "unknown quota code")
	}
	return defs, err
}

// quotaAccountSummaries is also used by the existing single-tenant routes so
// configured, balance and enforcement cannot drift between public projections.
func (r *QuotaAdminRepo) quotaAccountSummaries(ctx context.Context, tx *ent.Tx, tenants []*ent.Tenant, defs []*ent.QuotaDefinition) ([]*quotapb.TenantQuotaAccountSummary, error) {
	items := make([]*quotapb.TenantQuotaAccountSummary, 0, len(tenants))
	if len(tenants) == 0 {
		return items, nil
	}
	tenantIDs := make([]uint32, 0, len(tenants))
	planIDs := make([]uint32, 0, len(tenants))
	codes := make([]string, 0, len(defs))
	for _, d := range defs {
		codes = append(codes, d.Code)
	}
	seenPlan := map[uint32]bool{}
	for _, t := range tenants {
		tenantIDs = append(tenantIDs, t.ID)
		if t.PlanID != nil && !seenPlan[*t.PlanID] {
			planIDs = append(planIDs, *t.PlanID)
			seenPlan[*t.PlanID] = true
		}
	}
	plans := map[uint32]*ent.Plan{}
	if len(planIDs) != 0 {
		rows, err := tx.Plan.Query().Where(plan.IDIn(planIDs...)).WithQuotas(func(q *ent.PlanQuotaQuery) {
			q.Where(planquota.QuotaCodeIn(codes...))
		}).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range rows {
			plans[p.ID] = p
		}
	}
	rows, err := tx.QuotaAccount.Query().Where(quotaaccount.TenantIDIn(tenantIDs...), quotaaccount.QuotaCodeIn(codes...)).All(ctx)
	if err != nil {
		return nil, err
	}
	occupied := map[uint32]map[string]uint64{}
	for _, a := range rows {
		if a.TenantID == nil || a.OccupiedUnits < 0 {
			return nil, fmt.Errorf("invalid quota account numeric projection")
		}
		if occupied[*a.TenantID] == nil {
			occupied[*a.TenantID] = map[string]uint64{}
		}
		occupied[*a.TenantID][a.QuotaCode] = uint64(a.OccupiedUnits)
	}
	for _, t := range tenants {
		summary := &quotapb.TenantQuotaAccountSummary{TenantId: t.ID, PlanId: t.PlanID, Accounts: []*quotapb.QuotaAccountView{}}
		if t.Name != nil {
			summary.TenantName = *t.Name
		}
		if t.Code != nil {
			summary.TenantCode = *t.Code
		}
		limits := map[string]uint64{}
		if t.PlanID != nil && plans[*t.PlanID] != nil {
			p := plans[*t.PlanID]
			if p.Name != nil {
				summary.PlanName = *p.Name
			}
			for _, policy := range p.Edges.Quotas {
				if policy.QuotaValue == nil {
					return nil, fmt.Errorf("invalid quota policy numeric projection")
				}
				limits[policy.QuotaCode] = *policy.QuotaValue
			}
		}
		for _, d := range defs {
			limit, configured := limits[d.Code]
			occ := occupied[t.ID][d.Code]
			available := uint64(0)
			if limit > occ {
				available = limit - occ
			}
			summary.Accounts = append(summary.Accounts, &quotapb.QuotaAccountView{
				QuotaCode: d.Code, Unit: d.Unit, Limit: strconv.FormatUint(limit, 10),
				Occupied: strconv.FormatUint(occ, 10), Available: strconv.FormatUint(available, 10),
				OverLimit: occ > limit, Enforcement: r.enforcement(d.Code), Configured: configured,
			})
		}
		items = append(items, summary)
	}
	return items, nil
}
