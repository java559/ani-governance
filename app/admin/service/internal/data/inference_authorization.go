package data

import (
	"context"
	"errors"

	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
	ent "go-wind-admin/app/admin/service/internal/data/ent"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

const (
	InferenceCreatePath         = "/api/v1/inference/services"
	InferenceDeletePath         = "/api/v1/inference/services/{data.resource_id}:delete"
	InferenceUpdatePath         = "/api/v1/inference/services/{data.resource_id}:update"
	InferenceStartPath          = "/api/v1/inference/services/{data.resource_id}:start"
	InferenceStopPath           = "/api/v1/inference/services/{data.resource_id}:stop"
	InferenceRestartPath        = "/api/v1/inference/services/{data.resource_id}:restart"
	InferenceListOperationsPath = "/api/v1/inference/services/{resource_id}/operations"
)

var ErrInferenceAuthorizationDenied = errors.New("inference action authorization denied")
var ErrInferenceAuthorizationUnavailable = errors.New("inference authorization unavailable")

type InferenceAuthorizationRepo struct{ actions *ActionAuthorizationRepo }

func NewInferenceAuthorizationRepo(c *entCrud.EntClient[*ent.Client]) *InferenceAuthorizationRepo {
	return &InferenceAuthorizationRepo{actions: NewActionAuthorizationRepo(c)}
}

// InferenceModuleRegistered is fail-closed until the generated identity registry
// contains the explicitly reserved INFERENCE module.
func InferenceModuleRegistered() bool {
	id, ok := identityv1.Module_value["INFERENCE"]
	return ok && id > 0
}

func (r *InferenceAuthorizationRepo) Authorize(ctx context.Context, tenantID, userID uint32, path string) error {
	if !InferenceModuleRegistered() || !inferenceAuthorizedPath(path) {
		return ErrInferenceAuthorizationDenied
	}
	if r == nil || r.actions == nil {
		return ErrInferenceAuthorizationUnavailable
	}
	err := r.actions.authorizeRoute(ctx, tenantID, userID, path, "INFERENCE", "POST")
	switch {
	case errors.Is(err, ErrActionAuthorizationDenied):
		return ErrInferenceAuthorizationDenied
	case errors.Is(err, ErrActionAuthorizationUnavailable):
		return ErrInferenceAuthorizationUnavailable
	default:
		return err
	}
}

func inferenceAuthorizedPath(path string) bool {
	switch path {
	case InferenceCreatePath, InferenceDeletePath, InferenceUpdatePath, InferenceStartPath, InferenceStopPath, InferenceRestartPath:
		return true
	default:
		return false
	}
}

func (r *InferenceAuthorizationRepo) AuthorizeGpuUse(ctx context.Context, tenant, user uint32) error {
	if r == nil || r.actions == nil {
		return ErrActionAuthorizationUnavailable
	}
	return NewGpuUseAuthorizationRepo(r.actions.entClient).AuthorizeGpuUse(ctx, tenant, user)
}
