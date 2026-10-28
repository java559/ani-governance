package data

import (
	"context"

	paginationV1 "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planquota"
	"go-wind-admin/app/admin/service/internal/data/ent/quotadefinition"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

type QuotaAdminRepo struct {
	entClient    *entCrud.EntClient[*ent.Client]
	log          *bLogger.Helper
	capabilities QuotaExecutionCapabilities
}

// QuotaExecutionCapabilities is supplied only by controlled owner/action
// composition, before serving requests. Database policies cannot implement it.
type QuotaExecutionCapabilities interface{ EnforcesQuota(code string) bool }

func (r *QuotaAdminRepo) SetExecutionCapabilities(c QuotaExecutionCapabilities) { r.capabilities = c }
func (r *QuotaAdminRepo) enforcement(code string) quotapb.Enforcement {
	if isGPUCode(code) && r.capabilities != nil && r.capabilities.EnforcesQuota(code) {
		return quotapb.Enforcement_ENFORCED
	}
	return DeriveEnforcement(code)
}

func NewQuotaAdminRepo(ctx *bootstrap.Context, c *entCrud.EntClient[*ent.Client]) *QuotaAdminRepo {
	return &QuotaAdminRepo{entClient: c, log: ctx.NewLoggerHelper("quota-admin/repo/admin-service")}
}
func (r *QuotaAdminRepo) ListDefinitions(ctx context.Context, req *paginationV1.PagingRequest) (out *entCrud.PagingResult[quotapb.QuotaDefinition], err error) {
	err = quotaTransaction(ctx, r.entClient.Client(), func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		total, e := tx.QuotaDefinition.Query().Count(ctx)
		if e != nil {
			return e
		}
		out = &entCrud.PagingResult[quotapb.QuotaDefinition]{Total: uint64(total)}
		query := tx.QuotaDefinition.Query().Order(ent.Asc(quotadefinition.FieldCode))
		if req != nil && !req.GetNoPaging() && req.GetPageSize() > 0 {
			page := uint64(req.GetPage())
			if page < 1 {
				page = 1
			}
			offset := min((page-1)*uint64(req.GetPageSize()), uint64(total))
			if offset == uint64(total) {
				return nil
			}
			query.Offset(int(offset)).Limit(int(req.GetPageSize()))
		} else if req != nil && !req.GetNoPaging() && req.Offset != nil && req.Limit != nil {
			offset := min(req.GetOffset(), uint64(total))
			if offset == uint64(total) || req.GetLimit() == 0 {
				return nil
			}
			query.Offset(int(offset)).Limit(int(req.GetLimit()))
		}
		defs, e := query.All(ctx)
		if e != nil {
			return e
		}
		for _, d := range defs {
			out.Items = append(out.Items, &quotapb.QuotaDefinition{Code: d.Code, DisplayName: d.DisplayName, Unit: d.Unit, AccountingKind: mapAccountingKind(string(d.AccountingKind)), Enforcement: r.enforcement(d.Code)})
		}
		return nil
	})
	return
}
func (r *QuotaAdminRepo) ListTenantAccounts(ctx context.Context, tid uint32) (out []*quotapb.QuotaAccountView, err error) {
	if tid == 0 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "tenant required")
	}
	err = quotaReadSnapshot(ctx, r.entClient.Client(), func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		t, e := tx.Tenant.Query().Where(tenant.IDEQ(tid), tenant.IDGT(0)).Only(ctx)
		if e != nil {
			return e
		}
		defs, e := quotaSummaryDefinitions(ctx, tx, "")
		if e != nil {
			return e
		}
		summaries, e := r.quotaAccountSummaries(ctx, tx, []*ent.Tenant{t}, defs)
		if e != nil {
			return e
		}
		out = summaries[0].Accounts
		return nil
	})
	return
}
func (r *QuotaAdminRepo) HasPlanQuotaPolicy(ctx context.Context, id uint32, code string) (ok bool, err error) {
	err = quotaTransaction(ctx, r.entClient.Client(), func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		var e error
		ok, e = tx.PlanQuota.Query().Where(planquota.HasPlanWith(plan.IDEQ(id)), planquota.QuotaCodeEQ(code)).Exist(ctx)
		return e
	})
	return
}
func mapAccountingKind(kind string) quotapb.AccountingKind {
	switch kind {
	case "CONCURRENT":
		return quotapb.AccountingKind_CONCURRENT
	case "COUNTER":
		return quotapb.AccountingKind_COUNTER
	default:
		return quotapb.AccountingKind_ACCOUNTING_KIND_UNSPECIFIED
	}
}

// Execution capability is supplied by controlled owner/action assembly; a
// directory row, plan policy, or client request can never enable enforcement.
func DeriveEnforcement(code string) quotapb.Enforcement {
	if IsLegacyQuotaCode(code) {
		return quotapb.Enforcement_LEGACY_CONFIG_ONLY
	}
	if code == "gpu.count" {
		return quotapb.Enforcement_LAB_ONLY
	}
	return quotapb.Enforcement_NOT_ENABLED
}
