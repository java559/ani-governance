package service

import (
	"context"
	"errors"
	"fmt"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	computeview "go-wind-admin/api/gen/go/compute/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/permcode"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"strconv"
)

type ComputeCatalogAuthorizer interface {
	AuthorizeComputeCatalog(context.Context, uint32, uint32, string) error
	AuthorizeCatalog(context.Context, uint32, uint32) error
}

func (s *ComputePresetService) catalogScope(ctx context.Context, usage string) (string, string, error) {
	p, e := gpuPrincipal(ctx)
	if e != nil {
		return "", "", e
	}
	if s.catalog.Authorization == nil {
		return "", "", kratoserrors.ServiceUnavailable("COMPUTE_AUTHORIZATION_UNAVAILABLE", "compute authorization unavailable")
	}
	if e = s.catalog.Authorization.AuthorizeComputeCatalog(ctx, p.TenantID, p.ID, usage); e != nil {
		if errors.Is(e, data.ErrActionAuthorizationDenied) {
			return "", "", kratoserrors.Forbidden("FORBIDDEN", "compute action denied")
		}
		if errors.Is(e, data.ErrActionAuthorizationUnavailable) {
			return "", "", kratoserrors.ServiceUnavailable("COMPUTE_AUTHORIZATION_UNAVAILABLE", "compute authorization unavailable")
		}
		return "", "", e
	}
	if s.catalog.Resolver == nil {
		return "", "", kratoserrors.ServiceUnavailable("COMPUTE_ADAPTER_UNAVAILABLE", "tenant resolver unavailable")
	}
	tenant, e := s.catalog.Resolver.ResourceTenantID(ctx, p.TenantID)
	if e != nil {
		return "", "", e
	}
	if !canonicalCatalogUUID(tenant) {
		return "", "", kratoserrors.ServiceUnavailable("COMPUTE_ADAPTER_UNAVAILABLE", "invalid tenant identity")
	}
	return tenant, "governance:user:" + strconv.FormatUint(uint64(p.ID), 10), nil
}
func (s *ComputePresetService) ListPresets(ctx context.Context, r *computeview.ListPresetsRequest) (*computeview.ListPresetsResponse, error) {
	if r == nil {
		return nil, kratoserrors.BadRequest("INVALID_COMPUTE_REQUEST", "request required")
	}
	if _, _, ok := permcode.ComputeCatalogGrant(r.Usage); !ok {
		return nil, kratoserrors.BadRequest("INVALID_COMPUTE_REQUEST", "valid usage required")
	}
	computeType := r.ComputeType
	if r.Usage == "SANDBOX" {
		if (computeType != "" && computeType != "cpu") || r.GpuMode != "" {
			return nil, kratoserrors.BadRequest("INVALID_COMPUTE_REQUEST", "Sandbox supports CPU presets only")
		}
		computeType = "cpu"
	}
	tenant, actor, e := s.catalogScope(ctx, r.Usage)
	if e != nil {
		return nil, e
	}
	if s.catalog.Compute == nil {
		return nil, kratoserrors.ServiceUnavailable("COMPUTE_ADAPTER_UNAVAILABLE", "compute catalog unavailable")
	}
	page, size, e := resolveComputePage(r.Page, r.PageSize)
	if e != nil {
		return nil, e
	}
	items, e := s.catalog.Compute.ListPresets(ctx, tenant, actor, &compute.ListPresetsRequest{Usage: compute.Usage(compute.Usage_value[r.Usage]), TenantId: tenant, Page: page, PageSize: size, Name: r.Name, Code: r.Code, ComputeType: computeType, GpuMode: r.GpuMode})
	if e != nil {
		return nil, e
	}
	if items == nil {
		return nil, kratoserrors.ServiceUnavailable("COMPUTE_INVALID_RESPONSE", "invalid owner response")
	}
	catalogStatus, reason := "OK", ""
	var profileStates map[string]acc.ProfileState
	if items.HasGpuCandidates && computeType != "cpu" {
		allowed, states, err := s.allowedPresetGPU(ctx, tenant)
		profileStates = states
		if err != nil {
			catalogStatus, reason = "UNAVAILABLE", "GPU_CATALOG_UNAVAILABLE"
			if errors.Is(err, data.ErrActionAuthorizationDenied) {
				catalogStatus, reason = "NO_PERMISSION", "GPU_CATALOG_NO_PERMISSION"
			}
		} else {
			items, e = s.catalog.Compute.ListPresets(ctx, tenant, actor, &compute.ListPresetsRequest{Usage: compute.Usage(compute.Usage_value[r.Usage]), TenantId: tenant, Page: page, PageSize: size, Name: r.Name, Code: r.Code, ComputeType: computeType, GpuMode: r.GpuMode, AllowedGpuProfiles: allowed})
			if e != nil {
				return nil, e
			}
			if items == nil {
				return nil, kratoserrors.ServiceUnavailable("COMPUTE_INVALID_RESPONSE", "invalid owner response")
			}
		}
	}
	out := &computeview.ListPresetsResponse{Items: []*computeview.Preset{}, Total: items.Total, GpuCatalogStatus: catalogStatus, GpuCatalogReason: reason}
	for _, p := range items.Items {
		item := wireComputePreset(p)
		if item != nil && p.ComputeType == "gpu" && p.Gpu != nil && item.Enabled {
			switch profileStates[fmt.Sprintf("%s/%d", p.Gpu.ProfileId, p.Gpu.ProfileVersion)] {
			case acc.ProfileState_PROFILE_DISABLED:
				item.Selectable = false
				item.UnavailableReason = "GPU_PROFILE_DISABLED"
			case acc.ProfileState_PROFILE_DELETED:
				item.Selectable = false
				item.UnavailableReason = "GPU_PROFILE_DELETED"
			case acc.ProfileState_PROFILE_ACTIVE:
				item.UnavailableReason = s.presetGPUAvailability(ctx, tenant, p.Gpu)
				item.Selectable = item.UnavailableReason == ""
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
func (s *ComputePresetService) allowedPresetGPU(ctx context.Context, tenant string) ([]*compute.GpuPresetReference, map[string]acc.ProfileState, error) {
	p, err := gpuPrincipal(ctx)
	if err != nil {
		return nil, nil, err
	}
	gate := s.catalog.Authorization
	if gate == nil {
		return nil, nil, data.ErrActionAuthorizationDenied
	}
	if err = gate.AuthorizeCatalog(ctx, p.TenantID, p.ID); err != nil {
		return nil, nil, err
	}
	if s.catalog.Catalog == nil || s.catalog.ClusterID == "" {
		return nil, nil, acceleratorUnavailable()
	}
	delegation := &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant, Actor: &acc.Actor{Type: "user", Id: strconv.FormatUint(uint64(p.ID), 10)}}
	var allowed []*compute.GpuPresetReference
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
			if !canonicalCatalogUUID(scope.ProfileId) || !canonicalCatalogUUID(scope.PoolId) {
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
			allowed = append(allowed, &compute.GpuPresetReference{ProfileId: scope.ProfileId, ProfileVersion: scope.ProfileVersion, SpecDigest: scope.SpecDigest, ClusterId: scope.ClusterId, PoolId: scope.PoolId})
		}
		// Leave headroom under the existing gRPC message limit. A larger catalog is
		// unavailable, never silently truncated into an authorization decision.
		if proto.Size(&compute.ListPresetsRequest{AllowedGpuProfiles: allowed}) > 3*1024*1024 {
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

// Availability is a current catalog hint, never an admission lease or a replay
// condition. Only already authorized, tenant-scoped page items reach this call.
func (s *ComputePresetService) presetGPUAvailability(ctx context.Context, tenant string, g *compute.GpuPresetReference) string {
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
		switch kratoserrors.FromError(mapAcceleratorError(err)).Reason {
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

type ComputeCatalog interface {
	ListPresets(context.Context, string, string, *compute.ListPresetsRequest) (*compute.ListPresetsResponse, error)
}

type ComputeGPUCatalog interface {
	ListProfiles(context.Context, *acc.ListProfilesRequest, ...grpc.CallOption) (*acc.ListProfilesResponse, error)
	CheckGpuFit(context.Context, *acc.CheckGpuFitRequest, ...grpc.CallOption) (*acc.GpuFit, error)
	GetCapacity(context.Context, *acc.GetCapacityRequest, ...grpc.CallOption) (*acc.CapacityView, error)
}
