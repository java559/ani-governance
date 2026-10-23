package service

import (
	"context"

	"github.com/go-kratos/kratos/v2/errors"
	"go-wind-admin/pkg/localdeps/kratos-authz/engine"
)

type AcceleratorActionAuthorizer interface {
	IsAuthorized(context.Context, engine.Subject, engine.Action, engine.Resource, engine.Project) (bool, error)
}

var acceleratorInitializationActions = map[string]struct{ method, path string }{
	"accelerator:initialization:read":        {"GET", "/api/v1/admin/accelerator/initialization"},
	"accelerator:initialization:create":      {"POST", "/api/v1/admin/accelerator/initializations"},
	"accelerator:initialization:retry":       {"POST", "/api/v1/admin/accelerator/initializations/{operation_id}:retry"},
	"accelerator:discovery:list":             {"GET", "/api/v1/admin/accelerator/discovered-nodes"},
	"accelerator:cluster:register":           {"POST", "/api/v1/admin/accelerator/clusters"},
	"accelerator:pool:create":                {"POST", "/api/v1/admin/accelerator/pools"},
	"accelerator:supply-group:adopt":         {"POST", "/api/v1/admin/accelerator/supply-groups:adopt"},
	"accelerator:profile:publish":            {"POST", "/api/v1/admin/accelerator/profiles"},
	"accelerator:supply-group:set-admission": {"POST", "/api/v1/admin/accelerator/supply-groups/{group_id}:set-admission"},
}

func (s *AcceleratorService) authorizeInitializationActions(ctx context.Context, actions ...string) error {
	if _, err := acceleratorPrincipal(ctx, true); err != nil {
		return err
	}
	claims, ok := engine.AuthClaimsFromContext(ctx)
	if !ok || claims == nil || claims.Project == nil || *claims.Project != "0" || s.actionAuthorizer == nil {
		return errors.Forbidden("ACCELERATOR_INITIALIZATION_FORBIDDEN", "platform permission required")
	}
	var subjects []engine.Subject
	if claims.Subject != nil && *claims.Subject != "" {
		subjects = append(subjects, *claims.Subject)
	} else if claims.Subjects != nil {
		for _, subject := range *claims.Subjects {
			if subject != "" {
				subjects = append(subjects, engine.Subject(subject))
			}
		}
	}
	if len(subjects) == 0 {
		return errors.Forbidden("ACCELERATOR_INITIALIZATION_FORBIDDEN", "platform permission required")
	}
	for _, action := range actions {
		route, known := acceleratorInitializationActions[action]
		if !known {
			return errors.Forbidden("ACCELERATOR_INITIALIZATION_FORBIDDEN", "unsupported initialization permission")
		}
		allowed := false
		for _, subject := range subjects {
			granted, err := s.actionAuthorizer.IsAuthorized(ctx, subject, engine.Action(route.method), engine.Resource(route.path), "0")
			if err != nil {
				return errors.ServiceUnavailable("ACCELERATOR_AUTHORIZATION_UNAVAILABLE", "platform authorization unavailable")
			}
			if granted {
				allowed = true
				break
			}
		}
		if !allowed {
			return errors.Forbidden("ACCELERATOR_INITIALIZATION_FORBIDDEN", "platform permission required")
		}
	}
	return nil
}
