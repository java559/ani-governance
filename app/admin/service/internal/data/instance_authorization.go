package data

import (
	"context"
	identity "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

const InstanceCreatePath = "/api/v1/instances"
const InstanceDeletePath = "/api/v1/instances/{data.instance_id}"
const InstanceStartPath = "/api/v1/instances/{data.instance_id}:start"
const InstanceStopPath = "/api/v1/instances/{data.instance_id}:stop"
const InstanceGetPath = "/api/v1/instances/{instance_id}"
const InstanceOperationPath = "/api/v1/instance-operations/{operation_id}"
const InstancePresetPath = "/api/v1/instance-presets"
const InstanceExecPath = "/api/v1/instances/{data.instance_id}/exec"

func InstanceModuleRegistered() bool {
	id, ok := identity.Module_value["INSTANCE"]
	return ok && id > 0
}

type InstanceAuthorizationRepo struct{ current *InferenceAuthorizationRepo }

func NewInstanceAuthorizationRepo(c *entCrud.EntClient[*ent.Client]) *InstanceAuthorizationRepo {
	return &InstanceAuthorizationRepo{NewInferenceAuthorizationRepo(c)}
}
func (r *InstanceAuthorizationRepo) Authorize(ctx context.Context, tenant, user uint32, path, method string) error {
	if !InstanceModuleRegistered() {
		return ErrInferenceAuthorizationDenied
	}
	allowed := false
	switch path {
	case InstanceCreatePath:
		allowed = method == "POST" || method == "GET"
	case InstanceDeletePath:
		allowed = method == "DELETE"
	case InstanceStartPath, InstanceStopPath, InstanceExecPath:
		allowed = method == "POST"
	case InstanceGetPath, InstanceOperationPath, InstancePresetPath:
		allowed = method == "GET"
	}
	if !allowed || r == nil || r.current == nil {
		return ErrInferenceAuthorizationDenied
	}
	return r.current.authorizeRoute(ctx, tenant, user, path, "INSTANCE", method)
}
