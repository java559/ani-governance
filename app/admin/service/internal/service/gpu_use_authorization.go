package service

import (
	"context"
	"errors"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
)

// GpuUseEligibility checks the current ACCELERATOR entitlement. The business
// action is authorized separately; Profile catalog access is not a use grant.
type GpuUseEligibility interface {
	AuthorizeGpuUse(context.Context, uint32, uint32) error
}

// A grant exists only for this request after its current business action passed.
// It is neither persisted nor reused by a worker or original-key replay.
type gpuBusinessGrant struct {
	tenantID, userID uint32
	usage            compute.Usage
	action           string
}

func authorizeGpuBusiness(ctx context.Context, p *auth.Principal, usage compute.Usage, action string, authorize func() error) (gpuBusinessGrant, error) {
	if err := ctx.Err(); err != nil {
		return gpuBusinessGrant{}, err
	}
	if !gpuUsePurpose(usage) || action == "" || authorize == nil || !gpuUsePrincipalMatches(ctx, p) {
		return gpuBusinessGrant{}, gpuUseDenied()
	}
	if err := authorize(); err != nil {
		return gpuBusinessGrant{}, err
	}
	return gpuBusinessGrant{tenantID: p.TenantID, userID: p.ID, usage: usage, action: action}, nil
}

// TRAINING shares this rule for ordinary training and fine-tuning. Only the
// container adapter is wired; the helper does not register another consumer.
func gpuUsePurpose(usage compute.Usage) bool {
	return usage == compute.Usage_CONTAINER_INSTANCE || usage == compute.Usage_TRAINING
}

func gpuUsePrincipalMatches(ctx context.Context, p *auth.Principal) bool {
	current, err := auth.PrincipalFromContext(ctx)
	return err == nil && p != nil && current != nil && p.Type == auth.SubjectUser && current.Type == auth.SubjectUser && p.ID != 0 && p.TenantID != 0 && current.ID == p.ID && current.TenantID == p.TenantID
}

func authorizeNewGpuUse(ctx context.Context, grant gpuBusinessGrant, eligibility GpuUseEligibility) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || !gpuUsePrincipalMatches(ctx, p) || !gpuUsePurpose(grant.usage) || grant.action == "" || grant.tenantID != p.TenantID || grant.userID != p.ID {
		return gpuUseDenied()
	}
	if eligibility == nil {
		return gpuUseUnavailable()
	}
	err = eligibility.AuthorizeGpuUse(ctx, grant.tenantID, grant.userID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, data.ErrActionAuthorizationDenied):
		return gpuUseDenied()
	default:
		return gpuUseUnavailable()
	}
}

func gpuUseDenied() error {
	return kratoserrors.Forbidden("GPU_USE_DENIED", "GPU use is not authorized")
}

func gpuUseUnavailable() error {
	return kratoserrors.ServiceUnavailable("GPU_USE_AUTHORIZATION_UNAVAILABLE", "GPU use authorization unavailable")
}
