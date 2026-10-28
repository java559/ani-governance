package data

import (
	"context"
	"go-wind-admin/app/admin/service/internal/data/ent"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

// A GPU read capability does not grant read access to every owner type. Reuse
// the owner's existing current role, API status and plan/module checks.
type GpuResourceReadAuthorizationRepo struct{ actions *ActionAuthorizationRepo }

func NewGpuResourceReadAuthorizationRepo(c *entCrud.EntClient[*ent.Client]) *GpuResourceReadAuthorizationRepo {
	return &GpuResourceReadAuthorizationRepo{actions: NewActionAuthorizationRepo(c)}
}
func (r *GpuResourceReadAuthorizationRepo) AuthorizeGpuResourceRead(ctx context.Context, tenant, user uint32, kind string) error {
	if r == nil || r.actions == nil {
		return ErrActionAuthorizationUnavailable
	}
	switch kind {
	case "container":
		return r.actions.authorizeRoute(ctx, tenant, user, InstanceGetPath, "INSTANCE", "GET")
	case "inference":
		return r.actions.authorizeRoute(ctx, tenant, user, "/api/v1/inference/services/{resource_id}", "INFERENCE", "GET")
	default:
		return ErrActionAuthorizationDenied
	}
}
