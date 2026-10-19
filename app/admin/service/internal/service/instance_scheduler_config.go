package service

import (
	"context"

	"github.com/go-kratos/kratos/v2/errors"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/pkg/middleware/auth"
)

func instancePlatformPrincipal(ctx context.Context, permission string) (*auth.Principal, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
	}
	if p.Type != auth.SubjectUser || p.TenantID != 0 || p.Claims == nil || p.Claims.GetTenantId() != 0 || p.ID != p.Claims.GetUserId() || !hasPlatformRole(p.Roles) || !hasPermission(p.Claims, permission) {
		return nil, errors.Forbidden("PLATFORM_INSTANCE_DENIED", "platform instance permission required")
	}
	return p, nil
}
func (s *InstanceService) GetSchedulerConfig(ctx context.Context, r *view.SchedulerConfigRequest) (*view.SchedulerConfig, error) {
	if _, err := instancePlatformPrincipal(ctx, "instance:scheduler:read"); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.BadRequest("INVALID_SCHEDULER_CONFIG", "request required")
	}
	if s.catalog.Cluster == nil {
		return nil, errors.ServiceUnavailable("SCHEDULER_CONFIG_UNAVAILABLE", "scheduler configuration unavailable")
	}
	config, version, err := s.catalog.Cluster.ReadSchedulerConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &view.SchedulerConfig{Config: config, ResourceVersion: version}, nil
}
func (s *InstanceService) UpdateSchedulerConfig(ctx context.Context, r *view.UpdateSchedulerConfigRequest) (*view.SchedulerConfig, error) {
	if _, err := instancePlatformPrincipal(ctx, "instance:scheduler:update"); err != nil {
		return nil, err
	}
	if r == nil || r.Data == nil || r.Data.ExpectedResourceVersion == "" {
		return nil, errors.BadRequest("INVALID_SCHEDULER_CONFIG", "data and expected_resource_version required")
	}
	if s.catalog.Cluster == nil {
		return nil, errors.ServiceUnavailable("SCHEDULER_CONFIG_UNAVAILABLE", "scheduler configuration unavailable")
	}
	config, version, err := s.catalog.Cluster.WriteSchedulerConfig(ctx, r.Data.Config, r.Data.ExpectedResourceVersion)
	if err != nil {
		return nil, err
	}
	return &view.SchedulerConfig{Config: config, ResourceVersion: version}, nil
}
