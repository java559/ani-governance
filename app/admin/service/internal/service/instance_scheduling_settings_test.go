package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type instanceSettingsStoreProbe struct {
	raw              []byte
	readErr, saveErr error
	reads, saves     int
	cluster          string
	actor            uint32
}

func (p *instanceSettingsStoreProbe) ReadInstanceSchedulingSettings(_ context.Context, cluster string) ([]byte, error) {
	p.reads++
	p.cluster = cluster
	return append([]byte(nil), p.raw...), p.readErr
}
func (p *instanceSettingsStoreProbe) SaveInstanceSchedulingSettings(_ context.Context, cluster string, actor uint32, raw []byte) error {
	p.saves++
	p.cluster, p.actor = cluster, actor
	if p.saveErr != nil {
		return p.saveErr
	}
	p.raw = append([]byte(nil), raw...)
	return nil
}

type instanceSettingsTenantsProbe struct {
	exists               map[uint32]bool
	existErr, mapErr     error
	existCalls, mapCalls int
	resource             string
}

func (p *instanceSettingsTenantsProbe) IsExist(_ context.Context, id uint32) (bool, error) {
	p.existCalls++
	return p.exists[id], p.existErr
}
func (p *instanceSettingsTenantsProbe) ResourceTenantID(_ context.Context, _ uint32) (string, error) {
	p.mapCalls++
	return p.resource, p.mapErr
}

type instanceSettingsClusterProbe struct {
	names                         []string
	priorityErr, compatibilityErr error
	compatibility                 [][3]string
}

func (*instanceSettingsClusterProbe) ReadSchedulerConfig(context.Context) (string, string, error) {
	return "", "", errors.New("unexpected scheduler configuration read")
}
func (*instanceSettingsClusterProbe) WriteSchedulerConfig(context.Context, string, string) (string, string, error) {
	return "", "", errors.New("unexpected scheduler configuration write")
}
func (p *instanceSettingsClusterProbe) PriorityClassExists(_ context.Context, name string) error {
	p.names = append(p.names, name)
	return p.priorityErr
}
func (p *instanceSettingsClusterProbe) PriorityClassRuntimeCompatible(_ context.Context, namespace, group, name string) error {
	p.compatibility = append(p.compatibility, [3]string{namespace, group, name})
	return p.compatibilityErr
}

type instanceSettingsResolveProbe struct {
	InstanceOwnerClient
	template *res.CreateSpec
	defaults *res.SchedulingOptions
	calls    int
}

func (p *instanceSettingsResolveProbe) Resolve(_ context.Context, tenant, actor string, in *res.CreateInstanceData, defaults *res.SchedulingOptions) (*res.CreateSpec, error) {
	p.calls++
	if tenant != presetTenant || actor != "governance:user:9" {
		return nil, errors.New("untrusted resolution scope")
	}
	if defaults != nil {
		p.defaults = proto.Clone(defaults).(*res.SchedulingOptions)
	}
	// The Resource boundary uses the existing official pure resolver rather than
	// an alternative implementation of scheduling merge behavior.
	actual, err := res.ResolveSchedulingOptions(in.Scheduling, defaults)
	if err != nil {
		return nil, err
	}
	out := proto.Clone(p.template).(*res.CreateSpec)
	out.Name = in.Name
	out.ImageId = in.ImageId
	out.ImageScope = in.ImageScope
	out.PresetId = in.PresetId
	out.Scheduling = actual
	return out, nil
}
func instanceSettingsBusiness(t *testing.T) *res.CreateSpec {
	t.Helper()
	return &res.CreateSpec{Name: "workspace", ImageId: "img_10000000000040008000000000000001", ImageScope: "platform", PresetId: "10000000-0000-4000-8000-000000000002", ImageReference: "registry.invalid/image@sha256:" + strings.Repeat("a", 64), Cpu: "1000m", Memory: "2048Mi", ClusterId: "10000000-0000-4000-8000-000000000001", Namespace: "workspaces", ServiceAccount: "workspace", StorageClass: "cephfs", WorkspaceSize: "1Gi", Shell: "/bin/sh", Uid: 1000, Gid: 1000, InstanceKind: "container", ComputeType: "cpu", Scheduling: &res.SchedulingOptions{PodPlacement: res.SchedulingOptions_NONE.Enum()}}
}
func instanceSettingsData(s *res.CreateSpec) *res.CreateInstanceData {
	return &res.CreateInstanceData{Name: s.Name, ImageId: s.ImageId, ImageScope: s.ImageScope, PresetId: s.PresetId}
}
func instanceSettingsRaw(t *testing.T, settings *view.InstanceSchedulingSettings) []byte {
	t.Helper()
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func instanceSettingsPlatform(permissions ...string) context.Context {
	return auth.NewContext(context.Background(), &authv1.UserTokenPayload{UserId: 9, Roles: []string{"platform:instance-settings"}, Permissions: permissions})
}
func instanceSettingsAllow(name string) *view.PriorityClassAllowlist {
	return &view.PriorityClassAllowlist{TenantId: 7, PriorityClassNames: []string{name}}
}

func TestInstanceSchedulingDefaultsRespectOriginalPresence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		original *res.SchedulingOptions
		defaults *view.SchedulingOptions
		expected res.SchedulingOptions_PodPlacement
	}{
		{name: "both omitted", expected: res.SchedulingOptions_NONE},
		{name: "default only", defaults: &view.SchedulingOptions{PodPlacement: view.SchedulingOptions_SPREAD_PREFERRED.Enum()}, expected: res.SchedulingOptions_SPREAD_PREFERRED},
		{name: "explicit NONE overrides default", original: &res.SchedulingOptions{PodPlacement: res.SchedulingOptions_NONE.Enum()}, defaults: &view.SchedulingOptions{PodPlacement: view.SchedulingOptions_SPREAD_PREFERRED.Enum()}, expected: res.SchedulingOptions_NONE},
		{name: "user placement overrides default", original: &res.SchedulingOptions{PodPlacement: res.SchedulingOptions_COLOCATE_PREFERRED.Enum()}, defaults: &view.SchedulingOptions{PodPlacement: view.SchedulingOptions_SPREAD_PREFERRED.Enum()}, expected: res.SchedulingOptions_COLOCATE_PREFERRED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			business := instanceSettingsBusiness(t)
			store := &instanceSettingsStoreProbe{raw: instanceSettingsRaw(t, &view.InstanceSchedulingSettings{Defaults: tc.defaults})}
			tenants := &instanceSettingsTenantsProbe{mapErr: errors.New("unused mapping dependency")}
			cluster := &instanceSettingsClusterProbe{priorityErr: errors.New("unused cluster dependency")}
			policy := NewInstanceSchedulingPolicy(store, tenants, cluster, business.ClusterId)
			owner := &instanceSettingsResolveProbe{template: business}
			binding := NewInstanceGpuBinding(owner, nil, business.ClusterId, policy)
			original := proto.Clone(instanceSettingsData(business)).(*res.CreateInstanceData)
			original.Scheduling = tc.original
			principal := &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}
			_, resolved, err := binding.ResolveGpuBusiness(instancePresetContext(7), principal, presetTenant, original)
			if err != nil {
				t.Fatal(err)
			}
			got := resolved.(*res.CreateSpec)
			if got.Scheduling == nil || got.Scheduling.PodPlacement == nil || got.Scheduling.GetPodPlacement() != tc.expected {
				t.Fatalf("raw/effective presence lost: %v", got)
			}
			if (original.Scheduling == nil) != (tc.original == nil) {
				t.Fatal("original object presence changed")
			}
			if tenants.mapCalls != 0 || len(cluster.names) != 0 || tenants.existCalls != 0 {
				t.Fatal("no selected priority started allowlist/mapping/cluster validation")
			}
			if store.reads != 1 || owner.calls != 1 {
				t.Fatal("settings or resolution repeated")
			}
		})
	}
}

func TestInstanceSchedulingAuthorizesOnlyEffectivePriority(t *testing.T) {
	normal, unused := "normal", "unused-default"
	for _, tc := range []struct {
		name               string
		raw, defaultName   *string
		allowlists         []*view.PriorityClassAllowlist
		mapErr, clusterErr error
		mapped             string
		code               int
		wantChecks         []string
		wantMapping        int
	}{
		{name: "selected default allowed", defaultName: &normal, allowlists: []*view.PriorityClassAllowlist{instanceSettingsAllow(normal)}, mapped: presetTenant, wantChecks: []string{normal}, wantMapping: 1},
		{name: "user B ignores unauthorized default A", raw: &normal, defaultName: &unused, allowlists: []*view.PriorityClassAllowlist{instanceSettingsAllow(normal)}, mapped: presetTenant, wantChecks: []string{normal}, wantMapping: 1},
		{name: "unconfigured list denies", raw: &normal, code: 403},
		{name: "empty list denies", raw: &normal, allowlists: []*view.PriorityClassAllowlist{{TenantId: 7}}, code: 403},
		{name: "different tenant list denies", raw: &normal, allowlists: []*view.PriorityClassAllowlist{{TenantId: 8, PriorityClassNames: []string{normal}}}, code: 403},
		{name: "default reference also denied", defaultName: &normal, code: 403},
		{name: "mapping failure", raw: &normal, allowlists: []*view.PriorityClassAllowlist{instanceSettingsAllow(normal)}, mapErr: errors.New("database secret"), code: 503, wantMapping: 1},
		{name: "different resource tenant", raw: &normal, allowlists: []*view.PriorityClassAllowlist{instanceSettingsAllow(normal)}, mapped: presetOtherTenant, code: 403, wantMapping: 1},
		{name: "cluster dependency failure", raw: &normal, allowlists: []*view.PriorityClassAllowlist{instanceSettingsAllow(normal)}, mapped: presetTenant, clusterErr: kratoserrors.ServiceUnavailable("SCHEDULING_OPTION_UNAVAILABLE", "dependency unavailable"), code: 503, wantChecks: []string{normal}, wantMapping: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			business := instanceSettingsBusiness(t)
			store := &instanceSettingsStoreProbe{raw: instanceSettingsRaw(t, &view.InstanceSchedulingSettings{Defaults: &view.SchedulingOptions{PriorityClassName: tc.defaultName}, PriorityClassAllowlists: tc.allowlists})}
			tenants := &instanceSettingsTenantsProbe{resource: tc.mapped, mapErr: tc.mapErr}
			cluster := &instanceSettingsClusterProbe{priorityErr: tc.clusterErr}
			policy := NewInstanceSchedulingPolicy(store, tenants, cluster, business.ClusterId)
			owner := &instanceSettingsResolveProbe{template: business}
			binding := NewInstanceGpuBinding(owner, nil, business.ClusterId, policy)
			input := proto.Clone(instanceSettingsData(business)).(*res.CreateInstanceData)
			if tc.raw != nil {
				input.Scheduling = &res.SchedulingOptions{PriorityClassName: tc.raw}
			}
			_, resolved, err := binding.ResolveGpuBusiness(instancePresetContext(7), &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}, presetTenant, input)
			if tc.code != 0 {
				if kratoserrors.Code(err) != tc.code {
					t.Fatalf("priority failure: %v", err)
				}
			} else if err != nil || resolved.(*res.CreateSpec).Scheduling.GetPriorityClassName() != normal {
				t.Fatalf("effective priority: %v %v", resolved, err)
			}
			if tenants.mapCalls != tc.wantMapping || len(cluster.names) != len(tc.wantChecks) {
				t.Fatalf("checked unused reference: map=%d cluster=%v", tenants.mapCalls, cluster.names)
			}
			for index, name := range tc.wantChecks {
				if cluster.names[index] != name {
					t.Fatal("wrong effective priority checked")
				}
			}
		})
	}
}

func TestInstanceSchedulingSettingsReadFailuresAre503(t *testing.T) {
	business := instanceSettingsBusiness(t)
	for _, store := range []*instanceSettingsStoreProbe{{readErr: errors.New("private database address")}, {raw: []byte(`{"defaults":{"pod_placement":"UNSPECIFIED"}}`)}, {raw: []byte(`{"undeclared":true}`)}} {
		owner := &instanceSettingsResolveProbe{template: business}
		policy := NewInstanceSchedulingPolicy(store, nil, nil, business.ClusterId)
		binding := NewInstanceGpuBinding(owner, nil, business.ClusterId, policy)
		if _, _, err := binding.ResolveGpuBusiness(instancePresetContext(7), &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}, presetTenant, instanceSettingsData(business)); kratoserrors.Code(err) != 503 || owner.calls != 0 {
			t.Fatalf("settings failure reached resolution: %v", err)
		}
	}
}

func TestInstanceSchedulingSettingsPlatformPermissionsAndAtomicValidation(t *testing.T) {
	business := instanceSettingsBusiness(t)
	store := &instanceSettingsStoreProbe{}
	tenants := &instanceSettingsTenantsProbe{exists: map[uint32]bool{7: true}, resource: presetTenant}
	policy := NewInstanceSchedulingPolicy(store, tenants, &instanceSettingsClusterProbe{}, business.ClusterId)
	s := NewInstanceService(nil, NewInstanceGpuBinding(nil, nil, business.ClusterId, policy), tenants, nil, nil, InstanceCatalogConfig{Settings: policy})
	read := "instance:scheduling:read"
	defaults := "instance:scheduling:defaults:update"
	allowlists := "instance:scheduling:allowlists:update"
	result, err := s.GetInstanceSchedulingSettings(instanceSettingsPlatform(read), &view.InstanceSchedulingSettingsRequest{})
	if err != nil || result.Defaults == nil || result.PriorityClassAllowlists == nil {
		t.Fatalf("empty settings shape: %v %v", result, err)
	}
	update := &view.UpdateInstanceSchedulingSettingsRequest{Data: &view.InstanceSchedulingSettings{Defaults: &view.SchedulingOptions{}, PriorityClassAllowlists: []*view.PriorityClassAllowlist{instanceSettingsAllow("normal")}}}
	for _, permissions := range [][]string{{read}, {defaults}, {allowlists}} {
		if _, err = s.UpdateInstanceSchedulingSettings(instanceSettingsPlatform(permissions...), update); kratoserrors.Code(err) != 403 || store.saves != 0 {
			t.Fatalf("incomplete write permissions accepted: %v", err)
		}
	}
	tenantClaims := &authv1.UserTokenPayload{UserId: 9, TenantId: proto.Uint32(7), Roles: []string{"platform:instance-settings"}, Permissions: []string{read, defaults, allowlists}}
	tenantContext := auth.NewContext(context.Background(), tenantClaims)
	beforeReads := store.reads
	if _, err = s.GetInstanceSchedulingSettings(tenantContext, &view.InstanceSchedulingSettingsRequest{}); kratoserrors.Code(err) != 403 || store.reads != beforeReads {
		t.Fatalf("tenant reached platform read: %v", err)
	}
	if _, err = s.UpdateInstanceSchedulingSettings(tenantContext, update); kratoserrors.Code(err) != 403 || store.saves != 0 {
		t.Fatalf("tenant permission forged platform update: %v", err)
	}
	writer := instanceSettingsPlatform(defaults, allowlists)
	invalid := proto.Clone(update).(*view.UpdateInstanceSchedulingSettingsRequest)
	invalid.Data.PriorityClassAllowlists = append(invalid.Data.PriorityClassAllowlists, &view.PriorityClassAllowlist{TenantId: 8, PriorityClassNames: []string{"normal"}})
	if _, err = s.UpdateInstanceSchedulingSettings(writer, invalid); kratoserrors.Code(err) != 400 || store.saves != 0 || len(store.raw) != 0 {
		t.Fatalf("nonexistent tenant caused partial save: %v", err)
	}
	tenants.existErr = errors.New("private lookup failure")
	if _, err = s.UpdateInstanceSchedulingSettings(writer, update); kratoserrors.Code(err) != 503 || store.saves != 0 {
		t.Fatalf("failed tenant lookup caused save: %v", err)
	}
	tenants.existErr = nil
	if result, err = s.UpdateInstanceSchedulingSettings(writer, update); err != nil || result.Defaults == nil || store.saves != 1 || store.actor != 9 || store.cluster != business.ClusterId {
		t.Fatalf("managed save: %v %v", result, err)
	}
	stored := new(view.InstanceSchedulingSettings)
	if err = protojson.Unmarshal(store.raw, stored); err != nil || !proto.Equal(stored, result) {
		t.Fatalf("saved settings lost presence: %v", err)
	}
	if _, err = s.GetInstanceSchedulingSettings(writer, &view.InstanceSchedulingSettingsRequest{}); kratoserrors.Code(err) != 403 {
		t.Fatalf("write permission enabled read: %v", err)
	}
}

func TestInstanceSchedulingFixedPodGroupConflictGuard(t *testing.T) {
	business := instanceSettingsBusiness(t)
	priority := "normal"
	business.Scheduling.PriorityClassName = &priority
	cluster := &instanceSettingsClusterProbe{compatibilityErr: kratoserrors.Conflict("SCHEDULING_FIELD_CONFLICT", "fixed group priority conflicts")}
	policy := NewInstanceSchedulingPolicy(&instanceSettingsStoreProbe{}, nil, cluster, business.ClusterId)
	binding := NewInstanceGpuBinding(nil, nil, business.ClusterId, policy)
	// This targets only the fixed-group compatibility guard. Complete ACC plan
	// validation precedes it in admission and has independent builder vectors.
	plan := &acc.ResolvedGpuRequest{Execution: &acc.GpuExecution{SchedulerName: "volcano", PodAnnotations: map[string]string{"scheduling.k8s.io/group-name": "fixed-group"}}}
	if err := binding.ValidateResolvedInstancePlan(context.Background(), business, plan); kratoserrors.Code(err) != 409 || len(cluster.compatibility) != 1 || cluster.compatibility[0] != [3]string{business.Namespace, "fixed-group", priority} {
		t.Fatalf("fixed group conflict: %v", err)
	}
	cluster.compatibilityErr = nil
	if err := binding.ValidateResolvedInstancePlan(context.Background(), business, plan); err != nil {
		t.Fatal(err)
	}
	plan.Execution.SchedulerName = "unsupported"
	if err := binding.ValidateResolvedInstancePlan(context.Background(), business, plan); kratoserrors.Reason(err) != "SCHEDULING_OPTION_UNSUPPORTED" {
		t.Fatalf("unsupported runtime silently accepted: %v", err)
	}
	business.Scheduling.PriorityClassName = nil
	if err := binding.ValidateResolvedInstancePlan(context.Background(), business, nil); err != nil {
		t.Fatalf("no priority changed original runtime path: %v", err)
	}
}
