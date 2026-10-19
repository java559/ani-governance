package service

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/pkg/middleware/auth"
)

type schedulerConfigBoundary struct {
	reads, writes             int
	config, version, expected string
}

func (c *schedulerConfigBoundary) ReadSchedulerConfig(context.Context) (string, string, error) {
	c.reads++
	return c.config, c.version, nil
}
func (c *schedulerConfigBoundary) WriteSchedulerConfig(_ context.Context, config, expected string) (string, string, error) {
	c.writes++
	c.config = config
	c.expected = expected
	return config, "new-rv", nil
}
func (c *schedulerConfigBoundary) PriorityClassExists(context.Context, string) error { return nil }

func TestInstanceSchedulerPlatformIsolation(t *testing.T) {
	cluster := &schedulerConfigBoundary{config: "native: yaml\n", version: "editor-rv"}
	s := NewInstanceService(nil, nil, nil, nil, nil, InstanceCatalogConfig{Cluster: cluster})
	claims := &authv1.UserTokenPayload{UserId: 9, Roles: []string{"platform:scheduler"}, Permissions: []string{"instance:scheduler:read"}}
	ctx := auth.NewContext(context.Background(), claims)
	result, err := s.GetSchedulerConfig(ctx, &view.SchedulerConfigRequest{})
	if err != nil || result.Config != cluster.config || result.ResourceVersion != "editor-rv" {
		t.Fatalf("platform read: %+v %v", result, err)
	}
	update := &view.UpdateSchedulerConfigRequest{Data: &view.SchedulerConfigData{Config: "new: yaml\n", ExpectedResourceVersion: "editor-rv"}}
	if _, err = s.UpdateSchedulerConfig(ctx, update); errors.Code(err) != 403 {
		t.Fatalf("read permission enabled write: %v", err)
	}
	writer := &authv1.UserTokenPayload{UserId: 9, Roles: claims.Roles, Permissions: []string{"instance:scheduler:update"}}
	if result, err = s.UpdateSchedulerConfig(auth.NewContext(context.Background(), writer), update); err != nil || result.ResourceVersion != "new-rv" || cluster.expected != "editor-rv" {
		t.Fatalf("editor conditional write: %+v %v", result, err)
	}
	if _, err = s.GetSchedulerConfig(auth.NewContext(context.Background(), writer), &view.SchedulerConfigRequest{}); errors.Code(err) != 403 {
		t.Fatalf("write permission enabled read: %v", err)
	}
	for name, p := range map[string]*auth.Principal{
		"tenant":           {Type: auth.SubjectUser, ID: 9, TenantID: 7, Claims: claims, Roles: claims.Roles},
		"api_key":          {Type: auth.SubjectAPIKey, ID: 9, Claims: claims, Roles: claims.Roles},
		"no_platform_role": {Type: auth.SubjectUser, ID: 9, Claims: claims, Roles: []string{"tenant:admin"}},
		"mismatched_actor": {Type: auth.SubjectUser, ID: 10, Claims: claims, Roles: claims.Roles},
	} {
		t.Run(name, func(t *testing.T) {
			before := cluster.reads
			if _, err = s.GetSchedulerConfig(auth.NewPrincipalContext(context.Background(), p), &view.SchedulerConfigRequest{}); errors.Code(err) != 403 || before != cluster.reads {
				t.Fatalf("untrusted platform reached cluster: %v", err)
			}
		})
	}
	if cluster.writes != 1 {
		t.Fatalf("unapproved writes %d", cluster.writes)
	}
}
