package service

import (
	"context"
	stderrors "errors"
	"fmt"
	"strconv"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type InstancePresetOwner interface {
	AdminListPresets(context.Context, string, *res.AdminListPresetsRequest) (*res.ListPresetsResponse, error)
	AdminGetPreset(context.Context, string, *res.AdminGetPresetRequest) (*res.Preset, error)
	AdminCreatePreset(context.Context, string, *res.AdminCreatePresetRequest) (*res.Preset, error)
	AdminUpdatePreset(context.Context, string, *res.AdminUpdatePresetRequest) (*res.Preset, error)
	AdminSetPresetEnabled(context.Context, string, *res.AdminSetPresetEnabledRequest) (*res.Preset, error)
	AdminDeletePreset(context.Context, string, *res.AdminDeletePresetRequest) (*emptypb.Empty, error)
}
type InstanceGPUAdmin interface {
	AdminListProfiles(context.Context, *acc.AdminListProfilesRequest, ...grpc.CallOption) (*acc.ListProfilesResponse, error)
	ListPools(context.Context, *acc.ListPoolsRequest, ...grpc.CallOption) (*acc.ListPoolsResponse, error)
	ListSupplyGroups(context.Context, *acc.ListSupplyGroupsRequest, ...grpc.CallOption) (*acc.ListSupplyGroupsResponse, error)
}
type InstanceGPUCatalog interface {
	ListProfiles(context.Context, *acc.ListProfilesRequest, ...grpc.CallOption) (*acc.ListProfilesResponse, error)
	CheckGpuFit(context.Context, *acc.CheckGpuFitRequest, ...grpc.CallOption) (*acc.GpuFit, error)
	GetCapacity(context.Context, *acc.GetCapacityRequest, ...grpc.CallOption) (*acc.CapacityView, error)
}
type InstanceCatalogConfig struct {
	ClusterID string
	Admin     InstanceGPUAdmin
	Catalog   InstanceGPUCatalog
	Cluster   InstanceSchedulingCluster
	Settings  *InstanceSchedulingPolicy
}

// InstanceSchedulingCluster accesses the one deployment-managed cluster.
type InstanceSchedulingCluster interface {
	ReadSchedulerConfig(context.Context) (string, string, error)
	WriteSchedulerConfig(context.Context, string, string) (string, string, error)
	PriorityClassExists(context.Context, string) error
}

func instanceBusinessError(err error) error {
	reason := "INVALID_INSTANCE_REQUEST"
	if stderrors.Is(err, res.ErrInvalidScheduling) {
		reason = "INVALID_SCHEDULING_OPTIONS"
	}
	if stderrors.Is(err, res.ErrSchedulingConflict) {
		reason = "SCHEDULING_FIELD_CONFLICT"
	}
	return errors.BadRequest(reason, "invalid frozen instance business")
}

func instanceCatalogUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func (s *InstanceService) allowedPresetGPU(ctx context.Context, tenant string) ([]*res.GpuPresetReference, map[string]acc.ProfileState, error) {
	p, err := gpuPrincipal(ctx)
	if err != nil {
		return nil, nil, err
	}
	gate, ok := s.binding.authorization.(interface {
		AuthorizeCatalog(context.Context, uint32, uint32) error
	})
	if !ok {
		return nil, nil, data.ErrActionAuthorizationDenied
	}
	if err = gate.AuthorizeCatalog(ctx, p.TenantID, p.ID); err != nil {
		return nil, nil, err
	}
	if s.catalog.Catalog == nil || s.catalog.ClusterID == "" {
		return nil, nil, acceleratorUnavailable()
	}
	delegation := &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant, Actor: &acc.Actor{Type: "user", Id: strconv.FormatUint(uint64(p.ID), 10)}}
	var allowed []*res.GpuPresetReference
	states := map[string]acc.ProfileState{}
	token := ""
	tokens := map[string]bool{}
	profiles := map[string]bool{}
	for {
		reply, err := s.catalog.Catalog.ListProfiles(ctx, &acc.ListProfilesRequest{Context: delegation, ClusterId: s.catalog.ClusterID, Page: &acc.Page{Size: 200, Token: token}})
		if err != nil {
			return nil, nil, err
		}
		if reply == nil || len(reply.Scopes) != len(reply.Items) {
			return nil, nil, acceleratorInvalidResponse()
		}
		scopes := map[string]*acc.ProfileScope{}
		for _, scope := range reply.Scopes {
			if scope == nil || scope.ProfileId == "" || scope.ProfileVersion == 0 || scope.SpecDigest == "" || scope.ClusterId != s.catalog.ClusterID {
				return nil, nil, acceleratorInvalidResponse()
			}
			if !instanceCatalogUUID(scope.ProfileId) || !instanceCatalogUUID(scope.PoolId) {
				return nil, nil, acceleratorInvalidResponse()
			}
			key := fmt.Sprintf("%s/%d", scope.ProfileId, scope.ProfileVersion)
			if scopes[key] != nil {
				return nil, nil, acceleratorInvalidResponse()
			}
			scopes[key] = scope
		}
		for _, profile := range reply.Items {
			if profile == nil || (profile.State != acc.ProfileState_PROFILE_ACTIVE && profile.State != acc.ProfileState_PROFILE_DISABLED && profile.State != acc.ProfileState_PROFILE_DELETED) || profile.Spec == nil {
				return nil, nil, acceleratorInvalidResponse()
			}
			digest, err := gpuSpecDigest(profile)
			if err != nil || digest != profile.SpecDigest {
				return nil, nil, acceleratorInvalidResponse()
			}
			key := fmt.Sprintf("%s/%d", profile.ProfileId, profile.ProfileVersion)
			scope := scopes[key]
			if profiles[key] || scope == nil || scope.SpecDigest != profile.SpecDigest {
				return nil, nil, acceleratorInvalidResponse()
			}
			profiles[key] = true
			states[key] = profile.State
			allowed = append(allowed, &res.GpuPresetReference{ProfileId: scope.ProfileId, ProfileVersion: scope.ProfileVersion, SpecDigest: scope.SpecDigest, ClusterId: scope.ClusterId, PoolId: scope.PoolId})
		}
		// Leave headroom under the existing gRPC message limit. A larger catalog is
		// unavailable, never silently truncated into an authorization decision.
		if proto.Size(&res.ListPresetsRequest{AllowedGpuProfiles: allowed}) > 3*1024*1024 {
			return nil, nil, acceleratorUnavailable()
		}
		token = reply.NextToken
		if token == "" {
			return allowed, states, nil
		}
		if tokens[token] {
			return nil, nil, acceleratorInvalidResponse()
		}
		tokens[token] = true
	}
}

func resolveInstancePage(p, s *uint32) (uint32, uint32, error) {
	page, size := uint32(1), uint32(20)
	if p != nil {
		page = *p
	}
	if s != nil {
		size = *s
	}
	if page == 0 || size == 0 || size > 100 {
		return 0, 0, errors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid page or pageSize")
	}
	return page, size, nil
}
func wireInstancePreset(p *res.Preset) *view.Preset {
	if p == nil {
		return nil
	}
	out := &view.Preset{Id: p.Id, Code: p.Code, Name: p.Name, Description: p.Description, ComputeType: p.ComputeType, CpuMillis: p.CpuMillis, MemoryMib: p.MemoryMib, Enabled: p.Enabled, Selectable: p.Selectable, UnavailableReason: p.UnavailableReason}
	if p.CreatedAt != nil {
		out.CreatedAt = p.CreatedAt.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if p.UpdatedAt != nil {
		out.UpdatedAt = p.UpdatedAt.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if g := p.Gpu; g != nil {
		out.Gpu = &view.GpuSummary{ProfileId: g.ProfileId, ProfileVersion: g.ProfileVersion, Mode: g.Mode.String(), ModelKey: g.ModelKey, DevicesPerInstance: g.DevicesPerInstance, CoreLimitPercent: g.CoreLimitPercent}
		out.Gpu.MemoryMibPerDevice = g.MemoryMibPerDevice
		out.Gpu.MemoryMibTotal = g.MemoryMibPerDevice * int64(g.DevicesPerInstance)

	}
	return out
}
func (s *InstanceService) presetAdmin(ctx context.Context, permission string) (InstancePresetOwner, string, error) {
	p, err := instancePlatformPrincipal(ctx, permission)
	if err != nil {
		return nil, "", err
	}
	if s.binding == nil {
		return nil, "", errors.ServiceUnavailable("INSTANCE_ADAPTER_UNAVAILABLE", "instance owner unavailable")
	}
	owner, ok := s.binding.client.(InstancePresetOwner)
	if !ok {
		return nil, "", errors.ServiceUnavailable("INSTANCE_ADAPTER_UNAVAILABLE", "instance owner unavailable")
	}
	return owner, "governance:user:" + strconv.FormatUint(uint64(p.ID), 10), nil
}
func (s *InstanceService) AdminListPresets(ctx context.Context, r *view.AdminListPresetsRequest) (*view.AdminListPresetsResponse, error) {
	owner, actor, err := s.presetAdmin(ctx, "instance:preset:admin:list")
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "request required")
	}
	page, size, err := resolveInstancePage(r.Page, r.PageSize)
	if err != nil {
		return nil, err
	}
	reply, err := owner.AdminListPresets(ctx, actor, &res.AdminListPresetsRequest{Page: page, PageSize: size, Name: r.Name, Code: r.Code, ComputeType: r.ComputeType, GpuMode: r.GpuMode, Enabled: r.Enabled})
	if err != nil {
		return nil, err
	}
	if reply == nil {
		return nil, errors.ServiceUnavailable("INSTANCE_INVALID_RESPONSE", "invalid owner response")
	}
	out := &view.AdminListPresetsResponse{Items: []*view.Preset{}, Total: reply.Total}
	for _, p := range reply.Items {
		out.Items = append(out.Items, wireInstancePreset(p))
	}
	return out, nil
}
func (s *InstanceService) AdminGetPreset(ctx context.Context, r *view.PresetRequest) (*view.Preset, error) {
	owner, actor, err := s.presetAdmin(ctx, "instance:preset:admin:read")
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "request required")
	}
	reply, err := owner.AdminGetPreset(ctx, actor, &res.AdminGetPresetRequest{Id: r.Id})
	if err != nil {
		return nil, err
	}
	return wireInstancePreset(reply), nil
}
func (s *InstanceService) presetData(ctx context.Context, actor string, in *view.PresetData, replaceGPU bool) (*res.PresetData, error) {
	if in == nil {
		return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "preset data required")
	}
	out := &res.PresetData{Code: in.Code, Name: in.Name, Description: in.Description, ComputeType: in.ComputeType, CpuMillis: in.CpuMillis, MemoryMib: in.MemoryMib, Enabled: in.Enabled}
	if replaceGPU && in.Gpu != nil {
		if in.ComputeType == "cpu" {
			return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "CPU preset cannot carry GPU")
		}
		g, err := s.resolvePresetGPU(ctx, actor, in.Gpu)
		if err != nil {
			return nil, err
		}
		out.Gpu = g
	}
	return out, nil
}
func (s *InstanceService) AdminCreatePreset(ctx context.Context, r *view.CreatePresetRequest) (*view.Preset, error) {
	owner, actor, err := s.presetAdmin(ctx, "instance:preset:admin:create")
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "request required")
	}
	d, err := s.presetData(ctx, actor, r.Data, true)
	if err != nil {
		return nil, err
	}
	reply, err := owner.AdminCreatePreset(ctx, actor, &res.AdminCreatePresetRequest{Data: d})
	if err != nil {
		return nil, err
	}
	return wireInstancePreset(reply), nil
}
func (s *InstanceService) AdminUpdatePreset(ctx context.Context, r *view.UpdatePresetRequest) (*view.Preset, error) {
	owner, actor, err := s.presetAdmin(ctx, "instance:preset:admin:update")
	if err != nil {
		return nil, err
	}
	if r == nil || r.Data == nil || r.UpdateMask == nil || len(r.UpdateMask.Paths) == 0 {
		return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "data and update_mask required")
	}
	if r.Data.Code != "" {
		return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "code is immutable")
	}
	seen := map[string]bool{}
	gpu := false
	for _, p := range r.UpdateMask.Paths {
		if seen[p] {
			return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "duplicate update path")
		}
		seen[p] = true
		switch p {
		case "name", "description", "compute_type", "cpu_millis", "memory_mib", "enabled":
		case "gpu":
			gpu = true
		default:
			return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid update path")
		}
	}
	if seen["enabled"] && r.Data.Enabled == nil {
		return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "enabled must be present")
	}
	d, err := s.presetData(ctx, actor, r.Data, gpu)
	if err != nil {
		return nil, err
	}
	reply, err := owner.AdminUpdatePreset(ctx, actor, &res.AdminUpdatePresetRequest{Id: r.Id, Data: d, UpdateMask: r.UpdateMask})
	if err != nil {
		return nil, err
	}
	return wireInstancePreset(reply), nil
}
func (s *InstanceService) AdminSetPresetEnabled(ctx context.Context, r *view.SetPresetEnabledRequest) (*view.Preset, error) {
	owner, actor, err := s.presetAdmin(ctx, "instance:preset:admin:update")
	if err != nil {
		return nil, err
	}
	if r == nil || r.Data == nil || r.Data.Enabled == nil {
		return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "enabled must be present")
	}
	reply, err := owner.AdminSetPresetEnabled(ctx, actor, &res.AdminSetPresetEnabledRequest{Id: r.Id, Enabled: r.Data.Enabled})
	if err != nil {
		return nil, err
	}
	return wireInstancePreset(reply), nil
}
func (s *InstanceService) AdminDeletePreset(ctx context.Context, r *view.PresetRequest) (*view.Empty, error) {
	owner, actor, err := s.presetAdmin(ctx, "instance:preset:admin:delete")
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "request required")
	}
	_, err = owner.AdminDeletePreset(ctx, actor, &res.AdminDeletePresetRequest{Id: r.Id})
	if err != nil {
		return nil, err
	}
	return &view.Empty{}, nil
}

// Admin catalog reads establish managed facts; saving a preset never publishes
// an ACC Profile. The cluster comes from deployment configuration.
func (s *InstanceService) resolvePresetGPU(ctx context.Context, actor string, in *view.PresetGpuInput) (*res.GpuPresetReference, error) {
	invalid := errors.BadRequest("INVALID_GPU_PRESET_REFERENCE", "invalid GPU preset reference")
	if in == nil || in.ProfileVersion < 1 || in.DevicesPerInstance < 1 {
		return nil, invalid
	}
	if !instanceCatalogUUID(in.ProfileId) {
		return nil, invalid
	}
	c := s.catalog
	if c.Admin == nil || c.ClusterID == "" {
		return nil, acceleratorUnavailable()
	}
	admin := &acc.AdminRead{RequestId: uuid.NewString(), Actor: &acc.Actor{Type: "user", Id: actor[len("governance:user:"):]}}
	var profile *acc.GpuProfile
	token := ""
	seen := map[string]bool{}
	for {
		reply, err := c.Admin.AdminListProfiles(ctx, &acc.AdminListProfilesRequest{Context: admin, ClusterId: c.ClusterID, Page: &acc.Page{Size: 200, Token: token}})
		if err != nil {
			return nil, mapAcceleratorError(err)
		}
		if reply == nil {
			return nil, acceleratorInvalidResponse()
		}
		for _, p := range reply.Items {
			if p != nil && p.ProfileId == in.ProfileId && p.ProfileVersion == uint64(in.ProfileVersion) {
				if profile != nil {
					return nil, acceleratorInvalidResponse()
				}
				profile = p
			}
		}
		token = reply.NextToken
		if token == "" {
			break
		}
		if seen[token] {
			return nil, acceleratorInvalidResponse()
		}
		seen[token] = true
	}
	if profile == nil || profile.Spec == nil || profile.State != acc.ProfileState_PROFILE_ACTIVE || profile.SpecDigest == "" || profile.Spec.MaxDevicesPerReplica < in.DevicesPerInstance {
		return nil, invalid
	}
	digest, err := gpuSpecDigest(profile)
	if err != nil || digest != profile.SpecDigest {
		return nil, invalid
	}
	spec := profile.Spec
	if spec.Mode != acc.SupplyMode_WHOLE_EXCLUSIVE && spec.Mode != acc.SupplyMode_SHARED_FIXED || spec.Mode == acc.SupplyMode_SHARED_FIXED && (in.DevicesPerInstance != 1 || spec.MaxDevicesPerReplica != 1) {
		return nil, invalid
	}
	if spec.Mode == acc.SupplyMode_WHOLE_EXCLUSIVE && (spec.MemoryMibPerDevice <= 0 || spec.CoreLimitPercent != 100) || spec.Mode == acc.SupplyMode_SHARED_FIXED && (spec.MemoryMibPerDevice <= 0 || spec.CoreLimitPercent < 1 || spec.CoreLimitPercent > 99) {
		return nil, invalid
	}
	pools, err := s.instancePools(ctx, admin)
	if err != nil {
		return nil, err
	}
	var group *acc.SupplyGroup
	for _, pool := range pools {
		token = ""
		seen = map[string]bool{}
		for {
			reply, err := c.Admin.ListSupplyGroups(ctx, &acc.ListSupplyGroupsRequest{Context: admin, PoolId: pool.PoolId, Page: &acc.Page{Size: 200, Token: token}})
			if err != nil {
				return nil, mapAcceleratorError(err)
			}
			if reply == nil {
				return nil, acceleratorInvalidResponse()
			}
			for _, g := range reply.Items {
				if g != nil && g.GroupId == spec.GroupId {
					if group != nil || g.ClusterId != c.ClusterID || g.PoolId != pool.PoolId || g.Baseline == nil || g.Baseline.BaselineId != profile.BaselineId || g.Mode != spec.Mode || g.ModelKey != spec.ModelKey {
						return nil, invalid
					}
					group = g
				}
			}
			token = reply.NextToken
			if token == "" {
				break
			}
			if seen[token] {
				return nil, acceleratorInvalidResponse()
			}
			seen[token] = true
		}
	}
	if group == nil {
		return nil, invalid
	}
	out := &res.GpuPresetReference{ProfileId: profile.ProfileId, ProfileVersion: in.ProfileVersion, SpecDigest: profile.SpecDigest, DevicesPerInstance: in.DevicesPerInstance, ClusterId: group.ClusterId, PoolId: group.PoolId, Mode: spec.Mode, ModelKey: spec.ModelKey, CoreLimitPercent: spec.CoreLimitPercent}
	out.MemoryMibPerDevice = spec.MemoryMibPerDevice

	return out, nil
}
func (s *InstanceService) instancePools(ctx context.Context, admin *acc.AdminRead) ([]*acc.Pool, error) {
	var items []*acc.Pool
	token := ""
	seen := map[string]bool{}
	for {
		reply, err := s.catalog.Admin.ListPools(ctx, &acc.ListPoolsRequest{Context: admin, ClusterId: s.catalog.ClusterID, Page: &acc.Page{Size: 200, Token: token}})
		if err != nil {
			return nil, mapAcceleratorError(err)
		}
		if reply == nil {
			return nil, acceleratorInvalidResponse()
		}
		for _, p := range reply.Items {
			if p == nil || p.ClusterId != s.catalog.ClusterID {
				return nil, acceleratorInvalidResponse()
			}
			items = append(items, p)
		}
		token = reply.NextToken
		if token == "" {
			return items, nil
		}
		if seen[token] {
			return nil, acceleratorInvalidResponse()
		}
		seen[token] = true
	}
}

// Availability is a current catalog hint, never an admission lease or a replay
// condition. Only already authorized, tenant-scoped page items reach this call.
func (s *InstanceService) presetGPUAvailability(ctx context.Context, tenant string, g *res.GpuPresetReference) string {
	p, err := gpuPrincipal(ctx)
	if err != nil || s.catalog.Catalog == nil || g == nil {
		return "GPU_CATALOG_UNAVAILABLE"
	}
	delegation := &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant, Actor: &acc.Actor{Type: "user", Id: strconv.FormatUint(uint64(p.ID), 10)}}
	if g.DevicesPerInstance > 1 {
		capacity, err := s.catalog.Catalog.GetCapacity(ctx, &acc.GetCapacityRequest{Context: delegation, ProfileId: g.ProfileId, ProfileVersion: g.ProfileVersion})
		if err != nil {
			return "GPU_CATALOG_UNAVAILABLE"
		}
		if capacity != nil && capacity.Observation != nil {
			for _, issue := range capacity.Observation.Issues {
				if issue == "ADMISSION_CLOSED" {
					return "GPU_SUPPLY_PAUSED"
				}
			}
		}
		if capacity == nil || capacity.ProfileId != g.ProfileId || capacity.ProfileVersion != g.ProfileVersion || capacity.Observation == nil || !capacity.Observation.Complete {
			return "GPU_CAPACITY_UNKNOWN"
		}
		if capacity.KnownFreeUnits < int64(g.DevicesPerInstance) {
			return "GPU_TEMPORARILY_FULL"
		}
		return ""
	}
	fit, err := s.catalog.Catalog.CheckGpuFit(ctx, &acc.CheckGpuFitRequest{Context: delegation, Gpu: &acc.GpuRequest{ClusterId: g.ClusterId, PoolId: g.PoolId, ProfileId: g.ProfileId, ProfileVersion: g.ProfileVersion, Replicas: 1, DevicesPerReplica: g.DevicesPerInstance, ContainerName: "workspace"}})
	if err != nil {
		switch errors.FromError(mapAcceleratorError(err)).Reason {
		case "ADMISSION_CLOSED":
			return "GPU_SUPPLY_PAUSED"
		case "PROFILE_DISABLED":
			return "GPU_PROFILE_DISABLED"
		case "PROFILE_DELETED":
			return "GPU_PROFILE_DELETED"
		case "SOURCE_UNKNOWN", "SOURCE_STALE", "DEVICE_CAPACITY_UNKNOWN":
			return "GPU_CAPACITY_UNKNOWN"
		case "CONFIG_DRIFT", "NODE_LABEL_MISMATCH", "DEVICE_MEMORY_MISMATCH", "CAPACITY_INVARIANT_VIOLATION":
			return "GPU_SUPPLY_CONFIG_MISMATCH"
		case "PROFILE_NOT_ACTIVE":
			return "GPU_PROFILE_NOT_ACTIVE"
		default:
			return "GPU_CATALOG_UNAVAILABLE"
		}
	}
	if fit == nil || fit.Observation == nil || !fit.Observation.Complete || !fit.EstimateKnown || fit.Status == acc.FitStatus_FIT_UNKNOWN {
		return "GPU_CAPACITY_UNKNOWN"
	}
	if fit.Status == acc.FitStatus_WAIT_FOR_CAPACITY {
		return "GPU_TEMPORARILY_FULL"
	}
	if fit.Status != acc.FitStatus_FIT_ESTIMATE {
		return "GPU_CAPACITY_UNKNOWN"
	}
	return ""
}
