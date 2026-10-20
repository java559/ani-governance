package service

import (
	"context"

	"github.com/go-kratos/kratos/v2/errors"
	"go-wind-admin/pkg/middleware/auth"
)

// computePlatformPrincipal authorizes the platform catalog independently of
// tenant instance capabilities. A platform role never supplies a missing grant.
func computePlatformPrincipal(ctx context.Context, permission string) (*auth.Principal, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
	}
	if p.Type != auth.SubjectUser || p.TenantID != 0 || p.Claims == nil || p.Claims.GetTenantId() != 0 || p.ID != p.Claims.GetUserId() || !hasPlatformRole(p.Roles) || !hasPermission(p.Claims, permission) {
		return nil, errors.Forbidden("PLATFORM_COMPUTE_DENIED", "platform compute permission required")
	}
	return p, nil
}
