package service

import (
	"context"
	"fmt"

	"github.com/go-kratos/kratos/v2/errors"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"k8s.io/apimachinery/pkg/util/validation"
)

type InstanceSchedulingStore interface {
	ReadInstanceSchedulingSettings(context.Context, string) ([]byte, error)
	SaveInstanceSchedulingSettings(context.Context, string, uint32, []byte) error
}
type InstanceSchedulingTenants interface {
	IsExist(context.Context, uint32) (bool, error)
	ResourceTenantID(context.Context, uint32) (string, error)
}

// The policy is evaluated only after the original idempotency lookup. A single
// settings snapshot supplies defaults and any authorization actually needed.
type InstanceSchedulingPolicy struct {
	store     InstanceSchedulingStore
	tenants   InstanceSchedulingTenants
	cluster   InstanceSchedulingCluster
	clusterID string
}

func NewInstanceSchedulingPolicy(store InstanceSchedulingStore, tenants InstanceSchedulingTenants, cluster InstanceSchedulingCluster, clusterID string) *InstanceSchedulingPolicy {
	return &InstanceSchedulingPolicy{store: store, tenants: tenants, cluster: cluster, clusterID: clusterID}
}
func priorityClassNameValid(name string) bool {
	return name != "" && len(validation.IsDNS1123Subdomain(name)) == 0
}
func validateInstanceSettings(in *view.InstanceSchedulingSettings) error {
	invalid := errors.BadRequest("INVALID_SCHEDULING_OPTIONS", "invalid instance scheduling settings")
	if in == nil {
		return invalid
	}
	if in.Defaults != nil {
		d := in.Defaults
		if d.PodPlacement != nil && (*d.PodPlacement < view.SchedulingOptions_NONE || *d.PodPlacement > view.SchedulingOptions_SPREAD_PREFERRED) {
			return invalid
		}
		if d.PriorityClassName != nil && !priorityClassNameValid(*d.PriorityClassName) {
			return invalid
		}
	}
	seen := map[uint32]bool{}
	for _, row := range in.PriorityClassAllowlists {
		if row == nil || row.TenantId == 0 || seen[row.TenantId] {
			return invalid
		}
		seen[row.TenantId] = true
		names := map[string]bool{}
		for _, name := range row.PriorityClassNames {
			if !priorityClassNameValid(name) || names[name] {
				return invalid
			}
			names[name] = true
		}
	}
	return nil
}
func (p *InstanceSchedulingPolicy) read(ctx context.Context) (*view.InstanceSchedulingSettings, error) {
	if p == nil || p.store == nil || !instanceCatalogUUID(p.clusterID) {
		return nil, errors.ServiceUnavailable("SCHEDULING_SETTINGS_UNAVAILABLE", "instance scheduling settings unavailable")
	}
	raw, err := p.store.ReadInstanceSchedulingSettings(ctx, p.clusterID)
	if err != nil {
		return nil, errors.ServiceUnavailable("SCHEDULING_SETTINGS_UNAVAILABLE", "instance scheduling settings unavailable").WithCause(err)
	}
	out := &view.InstanceSchedulingSettings{Defaults: &view.SchedulingOptions{}, PriorityClassAllowlists: []*view.PriorityClassAllowlist{}}
	if len(raw) == 0 {
		return out, nil
	}
	if err = protojson.Unmarshal(raw, out); err != nil {
		return nil, errors.ServiceUnavailable("SCHEDULING_SETTINGS_UNAVAILABLE", "invalid managed instance scheduling settings").WithCause(err)
	}
	if err = validateInstanceSettings(out); err != nil {
		return nil, errors.ServiceUnavailable("SCHEDULING_SETTINGS_UNAVAILABLE", "invalid managed instance scheduling settings").WithCause(err)
	}
	if out.Defaults == nil {
		out.Defaults = &view.SchedulingOptions{}
	}
	return out, nil
}
func instanceSchedulingDefaults(settings *view.InstanceSchedulingSettings) *res.SchedulingOptions {
	if settings == nil || settings.Defaults == nil {
		return nil
	}
	d := settings.Defaults
	if d.PodPlacement == nil && d.PriorityClassName == nil {
		return nil
	}
	out := &res.SchedulingOptions{PriorityClassName: d.PriorityClassName}
	if d.PodPlacement != nil {
		out.PodPlacement = res.SchedulingOptions_PodPlacement(*d.PodPlacement).Enum()
	}
	return out
}
func (p *InstanceSchedulingPolicy) authorizePriority(ctx context.Context, principal *auth.Principal, resourceTenant string, settings *view.InstanceSchedulingSettings, business *res.CreateSpec) error {
	// No reference means no tenant allowlist/mapping/cluster read dependency.
	if business == nil || business.Scheduling == nil {
		return errors.BadRequest("INVALID_SCHEDULING_OPTIONS", "effective scheduling is required")
	}
	name := business.Scheduling.PriorityClassName
	if name == nil {
		return nil
	}
	if !priorityClassNameValid(*name) {
		return errors.BadRequest("INVALID_SCHEDULING_OPTIONS", "invalid priority class reference")
	}
	if p == nil || p.tenants == nil || p.cluster == nil || settings == nil {
		return errors.ServiceUnavailable("SCHEDULING_OPTION_UNAVAILABLE", "priority class authorization unavailable")
	}
	if principal == nil || principal.Type != auth.SubjectUser || principal.TenantID == 0 {
		return errors.Forbidden("SCHEDULING_OPTION_DENIED", "priority class reference denied")
	}
	allowed := false
	for _, row := range settings.PriorityClassAllowlists {
		if row.TenantId == principal.TenantID {
			for _, candidate := range row.PriorityClassNames {
				if candidate == *name {
					allowed = true
				}
			}
		}
	}
	if !allowed {
		return errors.Forbidden("SCHEDULING_OPTION_DENIED", "priority class reference denied")
	}
	mapped, err := p.tenants.ResourceTenantID(ctx, principal.TenantID)
	if err != nil {
		return errors.ServiceUnavailable("SCHEDULING_OPTION_UNAVAILABLE", "priority class authorization unavailable").WithCause(err)
	}
	if mapped != resourceTenant {
		return errors.Forbidden("SCHEDULING_OPTION_DENIED", "priority class reference denied")
	}
	if business.ClusterId != p.clusterID {
		return errors.BadRequest("SCHEDULING_FIELD_CONFLICT", "instance cluster differs from configured cluster")
	}
	return p.cluster.PriorityClassExists(ctx, *name)
}
func (s *InstanceService) GetInstanceSchedulingSettings(ctx context.Context, r *view.InstanceSchedulingSettingsRequest) (*view.InstanceSchedulingSettings, error) {
	if _, err := instancePlatformPrincipal(ctx, "instance:scheduling:read"); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.BadRequest("INVALID_SCHEDULING_OPTIONS", "request required")
	}
	return s.catalog.Settings.read(ctx)
}
func (s *InstanceService) UpdateInstanceSchedulingSettings(ctx context.Context, r *view.UpdateInstanceSchedulingSettingsRequest) (*view.InstanceSchedulingSettings, error) {
	actor, err := instancePlatformPrincipal(ctx, "instance:scheduling:defaults:update")
	if err != nil {
		return nil, err
	}
	if _, err = instancePlatformPrincipal(ctx, "instance:scheduling:allowlists:update"); err != nil {
		return nil, err
	}
	if r == nil || r.Data == nil {
		return nil, errors.BadRequest("INVALID_SCHEDULING_OPTIONS", "settings data required")
	}
	if err = validateInstanceSettings(r.Data); err != nil {
		return nil, err
	}
	p := s.catalog.Settings
	if p == nil || p.store == nil || p.tenants == nil || !instanceCatalogUUID(p.clusterID) {
		return nil, errors.ServiceUnavailable("SCHEDULING_SETTINGS_UNAVAILABLE", "instance scheduling settings unavailable")
	}
	for _, row := range r.Data.PriorityClassAllowlists {
		exists, err := p.tenants.IsExist(ctx, row.TenantId)
		if err != nil {
			return nil, errors.ServiceUnavailable("SCHEDULING_SETTINGS_UNAVAILABLE", "tenant validation unavailable").WithCause(err)
		}
		if !exists {
			return nil, errors.BadRequest("INVALID_SCHEDULING_OPTIONS", "allowlist tenant does not exist")
		}
	}
	out := proto.Clone(r.Data).(*view.InstanceSchedulingSettings)
	if out.Defaults == nil {
		out.Defaults = &view.SchedulingOptions{}
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("marshal instance scheduling settings: %w", err)
	}
	if err = p.store.SaveInstanceSchedulingSettings(ctx, p.clusterID, actor.ID, raw); err != nil {
		return nil, errors.ServiceUnavailable("SCHEDULING_SETTINGS_UNAVAILABLE", "instance scheduling settings save failed").WithCause(err)
	}
	return out, nil
}

func (b *InstanceGpuBinding) ValidateResolvedInstancePlan(ctx context.Context, business *res.CreateSpec, plan *acc.ResolvedGpuRequest) error {
	if business == nil || business.Scheduling == nil {
		return errors.BadRequest("INVALID_SCHEDULING_OPTIONS", "effective scheduling required")
	}
	name := business.Scheduling.PriorityClassName
	if name == nil {
		return nil
	}
	if plan == nil || plan.Execution == nil || plan.Execution.SchedulerName != "volcano" {
		return errors.BadRequest("SCHEDULING_OPTION_UNSUPPORTED", "GPU runtime does not support the priority reference")
	}
	for key, value := range plan.Execution.PodAnnotations {
		if key != "scheduling.k8s.io/group-name" || value == "" {
			continue
		}
		if b == nil || b.scheduling == nil {
			return errors.ServiceUnavailable("SCHEDULING_OPTION_UNAVAILABLE", "GPU priority validation unavailable")
		}
		checker, ok := b.scheduling.cluster.(interface {
			PriorityClassRuntimeCompatible(context.Context, string, string, string) error
		})
		if !ok {
			return errors.BadRequest("SCHEDULING_OPTION_UNSUPPORTED", "GPU runtime priority path is unavailable")
		}
		if err := checker.PriorityClassRuntimeCompatible(ctx, business.Namespace, value, *name); err != nil {
			return err
		}
	}
	return nil
}
