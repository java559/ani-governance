package data

import (
	"context"
	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
)

// A single page's real Resource owner UUIDs, resolved only after platform BFF
// authorization. No tenant enumeration or cross-page frontend join is needed.
func (r *TenantRepo) PlatformEIPTenants(ctx context.Context, ids []string) (map[string]*identityv1.Tenant, error) {
	out := map[string]*identityv1.Tenant{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.entClient.Client().Tenant.Query().Where(tenant.ResourceTenantIDIn(ids...)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ResourceTenantID] = r.mapper.ToDTO(row)
	}
	return out, nil
}
