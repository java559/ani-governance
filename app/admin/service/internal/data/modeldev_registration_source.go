package data

import (
	"context"
	"errors"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
)

// FindRegistrationSource reads the original frozen acceptance after current
// authorization. A recovery caller may be another manager in the same tenant.
func (r *ModelDevAcceptanceRepo) FindRegistrationSource(ctx context.Context, tenantID uint32, resourceTenantID, executionID string) (*ModelDevAcceptance, error) {
	if ctx == nil || tenantID == 0 || !modelDevCanonicalUUID(resourceTenantID) || !modelDevCanonicalUUID(executionID) {
		return nil, cpup01.ErrInvalidArgument
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.entClient == nil || r.entClient.Client() == nil {
		return nil, errors.New("modeldev registration source unavailable")
	}
	mapped, err := r.entClient.Client().Tenant.Query().Where(tenant.IDEQ(tenantID), tenant.ResourceTenantIDEQ(resourceTenantID)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !mapped {
		return nil, ErrModelDevAcceptanceNotFound
	}
	row, err := r.entClient.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(tenantID), modeldevacceptance.ResourceTenantIDEQ(resourceTenantID), modeldevacceptance.ExecutionIDEQ(executionID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrModelDevAcceptanceNotFound
	}
	if err != nil {
		return nil, err
	}
	return modelDevAcceptanceFromRow(row)
}
