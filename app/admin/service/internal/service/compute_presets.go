package service

import (
	"context"
	"strconv"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/compute/service/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type ComputeCatalogConfig struct {
	ClusterID string
	Admin     ComputeGPUAdmin
}

type ComputePresetService struct {
	admin.UnimplementedComputePresetServiceServer
	client  ComputePresetOwner
	catalog ComputeCatalogConfig
}

func NewComputePresetService(client ComputePresetOwner, catalog ComputeCatalogConfig) *ComputePresetService {
	return &ComputePresetService{client: client, catalog: catalog}
}

func canonicalCatalogUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

type ComputePresetOwner interface {
	AdminListPresets(context.Context, string, *res.AdminListPresetsRequest) (*res.ListPresetsResponse, error)
	AdminGetPreset(context.Context, string, *res.AdminGetPresetRequest) (*res.Preset, error)
	AdminCreatePreset(context.Context, string, *res.AdminCreatePresetRequest) (*res.Preset, error)
	AdminUpdatePreset(context.Context, string, *res.AdminUpdatePresetRequest) (*res.Preset, error)
	AdminSetPresetEnabled(context.Context, string, *res.AdminSetPresetEnabledRequest) (*res.Preset, error)
	AdminDeletePreset(context.Context, string, *res.AdminDeletePresetRequest) (*emptypb.Empty, error)
}
type ComputeGPUAdmin interface {
	AdminListProfiles(context.Context, *acc.AdminListProfilesRequest, ...grpc.CallOption) (*acc.ListProfilesResponse, error)
	ListPools(context.Context, *acc.ListPoolsRequest, ...grpc.CallOption) (*acc.ListPoolsResponse, error)
	ListSupplyGroups(context.Context, *acc.ListSupplyGroupsRequest, ...grpc.CallOption) (*acc.ListSupplyGroupsResponse, error)
}

func resolveComputePage(p, s *uint32) (uint32, uint32, error) {
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
func wireComputePreset(p *res.Preset) *view.Preset {
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
func (s *ComputePresetService) presetAdmin(ctx context.Context, permission string) (ComputePresetOwner, string, error) {
	p, err := computePlatformPrincipal(ctx, permission)
	if err != nil {
		return nil, "", err
	}
	if s.client == nil {
		return nil, "", errors.ServiceUnavailable("COMPUTE_ADAPTER_UNAVAILABLE", "compute owner unavailable")
	}
	return s.client, "governance:user:" + strconv.FormatUint(uint64(p.ID), 10), nil
}
func (s *ComputePresetService) AdminListPresets(ctx context.Context, r *view.AdminListPresetsRequest) (*view.AdminListPresetsResponse, error) {
	owner, actor, err := s.presetAdmin(ctx, "compute:preset:admin:list")
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.BadRequest("INVALID_INSTANCE_REQUEST", "request required")
	}
	page, size, err := resolveComputePage(r.Page, r.PageSize)
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
		out.Items = append(out.Items, wireComputePreset(p))
	}
	return out, nil
}
func (s *ComputePresetService) AdminGetPreset(ctx context.Context, r *view.PresetRequest) (*view.Preset, error) {
	owner, actor, err := s.presetAdmin(ctx, "compute:preset:admin:read")
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
	return wireComputePreset(reply), nil
}
func (s *ComputePresetService) presetData(ctx context.Context, actor string, in *view.PresetData, replaceGPU bool) (*res.PresetData, error) {
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
func (s *ComputePresetService) AdminCreatePreset(ctx context.Context, r *view.CreatePresetRequest) (*view.Preset, error) {
	owner, actor, err := s.presetAdmin(ctx, "compute:preset:admin:create")
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
	return wireComputePreset(reply), nil
}
func (s *ComputePresetService) AdminUpdatePreset(ctx context.Context, r *view.UpdatePresetRequest) (*view.Preset, error) {
	owner, actor, err := s.presetAdmin(ctx, "compute:preset:admin:update")
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
	return wireComputePreset(reply), nil
}
func (s *ComputePresetService) AdminSetPresetEnabled(ctx context.Context, r *view.SetPresetEnabledRequest) (*view.Preset, error) {
	owner, actor, err := s.presetAdmin(ctx, "compute:preset:admin:update")
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
	return wireComputePreset(reply), nil
}
func (s *ComputePresetService) AdminDeletePreset(ctx context.Context, r *view.PresetRequest) (*view.Empty, error) {
	owner, actor, err := s.presetAdmin(ctx, "compute:preset:admin:delete")
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
func (s *ComputePresetService) resolvePresetGPU(ctx context.Context, actor string, in *view.PresetGpuInput) (*res.GpuPresetReference, error) {
	invalid := errors.BadRequest("INVALID_GPU_PRESET_REFERENCE", "invalid GPU preset reference")
	if in == nil || in.ProfileVersion < 1 || in.DevicesPerInstance < 1 {
		return nil, invalid
	}
	if !canonicalCatalogUUID(in.ProfileId) {
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
	pools, err := s.computePools(ctx, admin)
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
func (s *ComputePresetService) computePools(ctx context.Context, admin *acc.AdminRead) ([]*acc.Pool, error) {
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
