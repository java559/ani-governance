package service

import (
	"context"
	stderrors "errors"
	"sort"
	"strconv"
	"strings"
	"time"

	errors "github.com/go-kratos/kratos/v2/errors"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	gpu "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/gpu/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	catalog "go-wind-admin/api/gen/go/catalog/service/v1"
	view "go-wind-admin/api/gen/go/gpu/service/v1"
	inference "go-wind-admin/api/gen/go/inference/service/v1"
	instance "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type GpuMonitoringReader interface {
	QueryGpuLatest(context.Context, bool, string, string, *gpu.QueryGpuLatestRequest) (*gpu.QueryGpuResponse, error)
	QueryGpuRange(context.Context, bool, string, string, *gpu.QueryGpuRangeRequest) (*gpu.QueryGpuResponse, error)
}

type GpuResourceReadAuthorizer interface {
	AuthorizeGpuResourceRead(context.Context, uint32, uint32, string) error
}

type GpuMonitoringService struct {
	admin.UnimplementedGpuAdminServiceServer
	admin.UnimplementedGpuServiceServer
	accelerator           *AcceleratorService
	monitoring            GpuMonitoringReader
	instances             *InstanceService
	inference             *InferenceService
	resourceAuthorization GpuResourceReadAuthorizer
}

func NewGpuMonitoringService(a *AcceleratorService, m GpuMonitoringReader, i *InstanceService, f *InferenceService, read ...GpuResourceReadAuthorizer) *GpuMonitoringService {
	s := &GpuMonitoringService{accelerator: a, monitoring: m, instances: i, inference: f}
	if len(read) == 1 {
		s.resourceAuthorization = read[0]
	}
	return s
}
func gpuInvalid() error {
	return errors.BadRequest("GPU_INVALID_QUERY", "invalid GPU monitoring query")
}
func gpuBudget() error {
	return errors.New(429, "GPU_QUERY_BUDGET_EXCEEDED", "reduce GPU sources, metrics or query duration")
}
func gpuDependency(err error) error {
	if err == nil {
		return nil
	}
	c := status.Code(err)
	switch c {
	case codes.InvalidArgument:
		return gpuInvalid()
	case codes.ResourceExhausted:
		return gpuBudget()
	case codes.DeadlineExceeded:
		return errors.GatewayTimeout("GPU_QUERY_TIMEOUT", "GPU query deadline exceeded")
	case codes.NotFound:
		return errors.NotFound("GPU_NOT_FOUND", "GPU target not found")
	case codes.Aborted:
		return errors.Conflict("GPU_SCOPE_CHANGED", "GPU scope changed; retry the query")
	}
	return errors.ServiceUnavailable("GPU_SOURCE_UNAVAILABLE", "GPU monitoring dependency unavailable")
}
func gpuWindow(d *view.GpuQueryData) (*timestamppb.Timestamp, *timestamppb.Timestamp, error) {
	if d == nil {
		return nil, nil, gpuInvalid()
	}
	now := time.Now().UTC().Truncate(time.Second)
	if d.Start != nil || d.End != nil {
		if d.Window != "" || !metricTimestamp(d.Start) || !metricTimestamp(d.End) {
			return nil, nil, gpuInvalid()
		}
		a, b := d.Start.AsTime(), d.End.AsTime()
		if !b.After(a) || b.Sub(a) > 30*24*time.Hour || a.Before(now.Add(-30*24*time.Hour)) || b.After(now) {
			return nil, nil, gpuInvalid()
		}
		return d.Start, d.End, nil
	}
	windows := map[string]time.Duration{"": 24 * time.Hour, "1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}
	span, ok := windows[d.Window]
	if !ok {
		return nil, nil, gpuInvalid()
	}
	return timestamppb.New(now.Add(-span)), timestamppb.New(now), nil
}
func gpuCodes(in []string, physical bool) ([]string, error) {
	if len(in) == 0 {
		if physical {
			return []string{"gpu.physical.utilization_ratio", "gpu.physical.memory_used_bytes", "gpu.physical.memory_total_bytes", "gpu.physical.temperature_celsius"}, nil
		}
		return []string{"gpu.resource.memory_used_bytes", "gpu.resource.memory_limit_bytes"}, nil
	}
	if len(in) > 4 {
		return nil, gpuBudget()
	}
	seen := map[string]bool{}
	for _, c := range in {
		if seen[c] || physical != strings.HasPrefix(c, "gpu.physical.") {
			return nil, gpuInvalid()
		}
		seen[c] = true
		if !physical && c != "gpu.resource.memory_used_bytes" && c != "gpu.resource.memory_limit_bytes" {
			return nil, gpuInvalid()
		}
	}
	return in, nil
}
func gpuActor(p *auth.Principal) string {
	return "governance:user:" + strconv.FormatUint(uint64(p.ID), 10)
}

func (s *GpuMonitoringService) physicalDevices(ctx context.Context, cluster, model string) ([]*acc.PhysicalDevice, *auth.Principal, error) {
	p, e := acceleratorPrincipal(ctx, true)
	if e != nil {
		return nil, nil, e
	}
	if !metricUUID(cluster) {
		return nil, nil, gpuInvalid()
	}
	if s.accelerator == nil || s.accelerator.client == nil {
		return nil, nil, acceleratorUnavailable()
	}
	c, e := s.accelerator.adminContext(ctx)
	if e != nil {
		return nil, nil, e
	}
	var out []*acc.PhysicalDevice
	token := ""
	seen := map[string]bool{}
	tokens := map[string]bool{"": true}
	pages := 0
	for {
		pages++
		if pages > 64 {
			return nil, nil, gpuBudget()
		}
		r, e := s.accelerator.client.Admin.ListDevices(ctx, &acc.ListDevicesRequest{Context: c, ClusterId: cluster, Page: &acc.Page{Size: 200, Token: token}})
		if e != nil {
			return nil, nil, mapAcceleratorError(e)
		}
		if r == nil {
			return nil, nil, acceleratorInvalidResponse()
		}
		for _, d := range r.Items {
			if d == nil || d.ClusterId != cluster || d.Node == nil || !metricUUID(d.DeviceId) || d.Node.Name == "" || d.Node.Uid == "" || d.VendorDeviceId == "" || seen[d.DeviceId] {
				return nil, nil, acceleratorInvalidResponse()
			}
			seen[d.DeviceId] = true
			if model == "" || d.ModelKey == model {
				out = append(out, d)
			}
		}
		if len(seen) > 10000 {
			return nil, nil, gpuBudget()
		}
		if r.NextToken == "" {
			break
		}
		if tokens[r.NextToken] {
			return nil, nil, acceleratorInvalidResponse()
		}
		tokens[r.NextToken] = true
		token = r.NextToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceId < out[j].DeviceId })
	return out, p, nil
}
func (s *GpuMonitoringService) ListGpuDevices(ctx context.Context, r *view.ListGpuDevicesRequest) (*view.ListGpuDevicesResponse, error) {
	if r == nil {
		return nil, gpuInvalid()
	}
	page, size := uint32(1), uint32(20)
	if r.Page != nil {
		page = *r.Page
	}
	if r.PageSize != nil {
		size = *r.PageSize
	}
	if page == 0 || size == 0 || size > 100 {
		return nil, gpuInvalid()
	}
	ds, _, e := s.physicalDevices(ctx, r.ClusterId, r.ModelKey)
	if e != nil {
		return nil, e
	}
	out := &view.ListGpuDevicesResponse{Items: []*catalog.AcceleratorDevice{}, Total: uint64(len(ds))}
	offset := uint64(page-1) * uint64(size)
	for n := offset; n < uint64(len(ds)) && n < offset+uint64(size); n++ {
		d := ds[n]
		out.Items = append(out.Items, &catalog.AcceleratorDevice{DeviceId: d.DeviceId, ClusterId: d.ClusterId, GroupId: d.GroupId, Node: &catalog.AcceleratorNode{Uid: d.Node.Uid, Name: d.Node.Name}, VendorDeviceId: d.VendorDeviceId, ModelKey: d.ModelKey, PhysicalMemoryBytes: d.PhysicalMemoryBytes, Health: d.Health, Observation: wireAcceleratorObservation(d.Observation)})
	}
	return out, nil
}
func (s *GpuMonitoringService) physicalTarget(ctx context.Context, cluster, model, id string, codes []string) (*gpu.GpuTarget, string, error) {
	ds, p, e := s.physicalDevices(ctx, cluster, model)
	if e != nil {
		return nil, "", e
	}
	t := &gpu.GpuTarget{Kind: "PHYSICAL", IndexComplete: true}
	models := map[string]bool{}
	for _, d := range ds {
		if id != "" && id != d.DeviceId {
			continue
		}
		models[d.ModelKey] = true
		t.Scopes = append(t.Scopes, &gpu.GpuSourceScope{ClusterId: d.ClusterId, NodeId: d.Node.Name, NodeUid: d.Node.Uid, CandidateComplete: true, GpuUuid: d.VendorDeviceId, DeviceId: d.DeviceId, OwnershipState: "VERIFIED"})
	}
	if id != "" && len(t.Scopes) == 0 {
		return nil, "", errors.NotFound("GPU_NOT_FOUND", "GPU target not found")
	}
	if len(t.Scopes) == 0 {
		return nil, "", errors.NotFound("GPU_NOT_FOUND", "no registered GPU target")
	}
	if len(t.Scopes) > 128 {
		return nil, "", gpuBudget()
	}
	for _, c := range codes {
		if c == "gpu.physical.utilization_ratio" && len(models) > 1 {
			return nil, "", errors.BadRequest("GPU_MODEL_FILTER_REQUIRED", "select one model for GPU utilization aggregation")
		}
	}
	return t, gpuActor(p), nil
}
func (s *GpuMonitoringService) physicalQuery(ctx context.Context, id string, d *view.GpuQueryData, latest bool) (*view.GpuQueryResponse, error) {
	if d == nil {
		return nil, gpuInvalid()
	}
	cs, e := gpuCodes(d.MetricCodes, true)
	if e != nil {
		return nil, e
	}
	t, actor, e := s.physicalTarget(ctx, d.ClusterId, d.ModelKey, id, cs)
	if e != nil {
		return nil, e
	}
	return s.execute(ctx, t, true, "", actor, cs, d, latest)
}
func (s *GpuMonitoringService) GetPhysicalGpuLatest(ctx context.Context, r *view.GpuPhysicalLatestRequest) (*view.GpuQueryResponse, error) {
	if r == nil || !metricUUID(r.DeviceId) {
		return nil, gpuInvalid()
	}
	return s.physicalQuery(ctx, r.DeviceId, &view.GpuQueryData{ClusterId: r.ClusterId, ModelKey: r.ModelKey, MetricCodes: r.MetricCodes}, true)
}
func (s *GpuMonitoringService) QueryPhysicalGpu(ctx context.Context, r *view.GpuPhysicalQueryRequest) (*view.GpuQueryResponse, error) {
	if r == nil || !metricUUID(r.DeviceId) {
		return nil, gpuInvalid()
	}
	return s.physicalQuery(ctx, r.DeviceId, r.Data, false)
}
func (s *GpuMonitoringService) GetPlatformGpuLatest(ctx context.Context, r *view.GpuPhysicalLatestRequest) (*view.GpuQueryResponse, error) {
	if r == nil || r.DeviceId != "" {
		return nil, gpuInvalid()
	}
	return s.physicalQuery(ctx, "", &view.GpuQueryData{ClusterId: r.ClusterId, ModelKey: r.ModelKey, MetricCodes: r.MetricCodes}, true)
}
func (s *GpuMonitoringService) QueryPlatformGpu(ctx context.Context, r *view.GpuPhysicalQueryRequest) (*view.GpuQueryResponse, error) {
	if r == nil || r.DeviceId != "" {
		return nil, gpuInvalid()
	}
	return s.physicalQuery(ctx, "", r.Data, false)
}

func gpuScopeTime(v string) (*timestamppb.Timestamp, error) {
	if v == "" {
		return nil, nil
	}
	t, e := time.Parse(time.RFC3339Nano, v)
	if e != nil {
		return nil, acceleratorInvalidResponse()
	}
	return timestamppb.New(t), nil
}
func (s *GpuMonitoringService) tenantTarget(ctx context.Context, tenantID uint32, kind, id string, start, end *timestamppb.Timestamp, platform bool) (*gpu.GpuTarget, string, string, error) {
	p, e := acceleratorPrincipal(ctx, platform)
	if e != nil {
		return nil, "", "", e
	}
	if s.accelerator == nil || s.accelerator.client == nil || s.accelerator.tenants == nil {
		return nil, "", "", acceleratorUnavailable()
	}
	if !platform {
		if tenantID != 0 {
			return nil, "", "", gpuInvalid()
		}
		tenantID = p.TenantID
	}
	if tenantID == 0 {
		return nil, "", "", gpuInvalid()
	}
	tenant, e := gpuTenantMonitoringIdentity(ctx, s.accelerator.tenants, tenantID)
	if e != nil {
		return nil, "", "", e
	}
	if !metricUUID(tenant) {
		return nil, "", "", acceleratorInvalidResponse()
	}
	actor := gpuActor(p)
	var ref *acc.GpuUsageRef
	if kind != "" || id != "" {
		if kind != "container" && kind != "inference" {
			return nil, "", "", errors.BadRequest("GPU_RESOURCE_KIND_UNSUPPORTED", "GPU resource kind is not enabled")
		}
		if s.resourceAuthorization == nil {
			return nil, "", "", errors.Forbidden("GPU_RESOURCE_READ_DENIED", "resource read permission required")
		}
		err := s.resourceAuthorization.AuthorizeGpuResourceRead(ctx, tenantID, p.ID, kind)
		if stderrors.Is(err, data.ErrActionAuthorizationDenied) {
			return nil, "", "", errors.Forbidden("GPU_RESOURCE_READ_DENIED", "resource read permission required")
		}
		if stderrors.Is(err, data.ErrActionAuthorizationUnavailable) {
			return nil, "", "", errors.ServiceUnavailable("GPU_AUTHORIZATION_UNAVAILABLE", "resource authorization unavailable")
		}
		if err != nil {
			return nil, "", "", err
		}
	}
	if kind != "" || id != "" {
		if platform || !metricUUID(id) {
			return nil, "", "", gpuInvalid()
		}
		owner := ""
		switch kind {
		case "container":
			owner = InstanceOwner
			if s.instances == nil {
				return nil, "", "", acceleratorUnavailable()
			}
			_, e = s.instances.GetInstance(ctx, &instance.GetInstanceRequest{InstanceId: id})
		case "inference":
			owner = "ani-inference"
			if s.inference == nil {
				return nil, "", "", acceleratorUnavailable()
			}
			_, e = s.inference.GetInference(ctx, &inference.GetInferenceRequest{ResourceId: id})
		default:
			return nil, "", "", errors.BadRequest("GPU_RESOURCE_KIND_UNSUPPORTED", "GPU resource kind is not enabled")
		}
		if e != nil {
			return nil, "", "", e
		}
		if s.accelerator.refs == nil {
			return nil, "", "", acceleratorUnavailable()
		}
		ref, e = s.accelerator.refs.ResolveGpuUsageRef(ctx, tenantID, owner, id)
		if e != nil {
			return nil, "", "", e
		}
		if ref == nil || ref.TenantId != tenant || ref.OwnerService != owner || ref.ResourceId != id || !metricUUID(ref.CreateOperationId) {
			return nil, "", "", acceleratorInvalidResponse()
		}
	}
	t := &gpu.GpuTarget{Kind: "TENANT", TenantId: tenant, ResourceKind: kind, ResourceId: id, IndexComplete: true}
	if ref != nil {
		t.Kind = "RESOURCE"
	}
	token, snapshot := "", ""
	scopes := 0
	pages := 0
	tokens := map[string]bool{"": true}
	sources := map[string]bool{}
	kinds := map[string]bool{}
	uncovered := map[string]bool{}
	for {
		pages++
		if pages > 64 {
			return nil, "", "", gpuBudget()
		}
		var reply *acc.GpuMetricScopesResponse
		if platform {
			c, err := s.accelerator.adminContext(ctx)
			if err != nil {
				return nil, "", "", err
			}
			reply, e = s.accelerator.client.Admin.ListAdminGpuMetricScopes(ctx, &acc.AdminGpuMetricScopesRequest{Context: c, TenantId: tenant, Ref: ref, Start: start.AsTime().Format(time.RFC3339Nano), End: end.AsTime().Format(time.RFC3339Nano), Page: &acc.Page{Size: 200, Token: token}, SnapshotId: snapshot})
		} else {
			c, _, err := s.accelerator.tenantContext(ctx)
			if err != nil {
				return nil, "", "", err
			}
			reply, e = s.accelerator.client.Usage.ListTenantGpuMetricScopes(ctx, &acc.TenantGpuMetricScopesRequest{Context: c, Ref: ref, Start: start.AsTime().Format(time.RFC3339Nano), End: end.AsTime().Format(time.RFC3339Nano), Page: &acc.Page{Size: 200, Token: token}, SnapshotId: snapshot})
		}
		if e != nil {
			return nil, "", "", mapAcceleratorError(e)
		}
		if reply == nil || reply.SnapshotId == "" || snapshot != "" && snapshot != reply.SnapshotId {
			return nil, "", "", acceleratorInvalidResponse()
		}
		snapshot = reply.SnapshotId
		t.ScopeWatermark = snapshot
		t.IndexComplete = t.IndexComplete && reply.IndexComplete
		if t.HistoryAvailableFrom == nil {
			t.HistoryAvailableFrom, e = gpuScopeTime(reply.IndexStartedAt)
			if e != nil {
				return nil, "", "", e
			}
		}
		for _, x := range reply.Items {
			scopes++
			if scopes > 4096 {
				return nil, "", "", gpuBudget()
			}
			if x == nil || x.Ref == nil || x.Ref.TenantId != tenant || ref != nil && (x.Ref.OwnerService != ref.OwnerService || x.Ref.ResourceId != id || x.Ref.CreateOperationId != ref.CreateOperationId) || x.Node == nil {
				return nil, "", "", acceleratorInvalidResponse()
			}
			// ACC names the owner's container scope container_instance. The public
			// adapter names this business resource container; original owner/ref
			// identity and runtime selectors remain unchanged.
			scopeKind := x.ResourceKind
			if x.Ref.OwnerService == InstanceOwner && scopeKind == "container_instance" {
				scopeKind = "container"
			}
			if ref != nil && scopeKind != kind {
				return nil, "", "", acceleratorInvalidResponse()
			}
			from, err := gpuScopeTime(x.ValidFrom)
			if err != nil {
				return nil, "", "", err
			}
			to, err := gpuScopeTime(x.ValidTo)
			if err != nil {
				return nil, "", "", err
			}
			evidence, err := gpuScopeTime(x.EvidenceAt)
			if err != nil {
				return nil, "", "", err
			}
			state := x.OwnershipState
			if !x.CandidateComplete {
				t.IndexComplete = false
				if state == "VERIFIED" {
					state = "OWNERSHIP_UNKNOWN"
				}
				uncovered[scopeKind] = true
			}
			if state != "VERIFIED" {
				t.IndexComplete = false
			}
			kinds[scopeKind] = true
			source := strings.Join([]string{x.ClusterId, x.Node.Name, x.Node.Uid, x.Namespace, x.PodName, x.ContainerName, strings.ToLower(strings.TrimPrefix(strings.ToLower(x.GpuUuid), "gpu-")), x.VdeviceId}, "\x00")
			sources[source] = true
			if len(sources) > 128 {
				return nil, "", "", gpuBudget()
			}
			t.Scopes = append(t.Scopes, &gpu.GpuSourceScope{ClusterId: x.ClusterId, NodeId: x.Node.Name, NodeUid: x.Node.Uid, CandidateComplete: x.CandidateComplete, Reasons: x.Reasons, GpuUuid: x.GpuUuid, Namespace: x.Namespace, PodName: x.PodName, ContainerName: x.ContainerName, VdeviceId: x.VdeviceId, UsageRef: &gpu.UsageRef{TenantId: x.Ref.TenantId, OwnerService: x.Ref.OwnerService, ResourceId: x.Ref.ResourceId, CreateOperationId: x.Ref.CreateOperationId}, ValidFrom: from, ValidTo: to, EvidenceAt: evidence, OwnershipState: state, ResourceKind: scopeKind, DeviceId: x.DeviceId})
		}
		if reply.NextToken == "" {
			break
		}
		if tokens[reply.NextToken] {
			return nil, "", "", acceleratorInvalidResponse()
		}
		tokens[reply.NextToken] = true
		token = reply.NextToken
	}
	for k := range kinds {
		if k != "" {
			t.IncludedResourceKinds = append(t.IncludedResourceKinds, k)
		}
	}
	for k := range uncovered {
		if k != "" {
			t.UncoveredResourceKinds = append(t.UncoveredResourceKinds, k)
		}
	}
	sort.Strings(t.IncludedResourceKinds)
	sort.Strings(t.UncoveredResourceKinds)
	return t, tenant, actor, nil
}
func (s *GpuMonitoringService) businessQuery(ctx context.Context, tenantID uint32, kind, id string, d *view.GpuQueryData, latest, platform bool) (*view.GpuQueryResponse, error) {
	if d == nil || d.ClusterId != "" || d.ModelKey != "" {
		return nil, gpuInvalid()
	}
	cs, e := gpuCodes(d.MetricCodes, false)
	if e != nil {
		return nil, e
	}
	start, end, e := gpuWindow(d)
	if e != nil {
		return nil, e
	}
	if latest {
		now := time.Now().UTC()
		start = timestamppb.New(now.Add(-90 * time.Second))
		end = timestamppb.New(now)
	}
	t, tenant, actor, e := s.tenantTarget(ctx, tenantID, kind, id, start, end, platform)
	if e != nil {
		return nil, e
	}
	out, e := s.execute(ctx, t, platform, tenant, actor, cs, &view.GpuQueryData{Start: start, End: end}, latest)
	if e == nil {
		out.ResourceKind = kind
		out.ResourceId = id
		if platform {
			out.TenantId = &tenantID
		}
	}
	return out, e
}
func (s *GpuMonitoringService) GetTenantGpuLatest(ctx context.Context, r *view.GpuTenantLatestRequest) (*view.GpuQueryResponse, error) {
	if r == nil {
		return nil, gpuInvalid()
	}
	return s.businessQuery(ctx, r.TenantId, "", "", &view.GpuQueryData{MetricCodes: r.MetricCodes}, true, true)
}
func (s *GpuMonitoringService) QueryTenantGpu(ctx context.Context, r *view.GpuTenantQueryRequest) (*view.GpuQueryResponse, error) {
	if r == nil {
		return nil, gpuInvalid()
	}
	return s.businessQuery(ctx, r.TenantId, "", "", r.Data, false, true)
}
func (s *GpuMonitoringService) GetMyGpuLatest(ctx context.Context, r *view.GpuTenantLatestRequest) (*view.GpuQueryResponse, error) {
	if r == nil {
		return nil, gpuInvalid()
	}
	return s.businessQuery(ctx, r.TenantId, "", "", &view.GpuQueryData{MetricCodes: r.MetricCodes}, true, false)
}
func (s *GpuMonitoringService) QueryMyGpu(ctx context.Context, r *view.GpuTenantQueryRequest) (*view.GpuQueryResponse, error) {
	if r == nil {
		return nil, gpuInvalid()
	}
	return s.businessQuery(ctx, r.TenantId, "", "", r.Data, false, false)
}
func (s *GpuMonitoringService) GetResourceGpuLatest(ctx context.Context, r *view.GpuResourceLatestRequest) (*view.GpuQueryResponse, error) {
	if r == nil {
		return nil, gpuInvalid()
	}
	return s.businessQuery(ctx, 0, r.ResourceKind, r.ResourceId, &view.GpuQueryData{MetricCodes: r.MetricCodes}, true, false)
}
func (s *GpuMonitoringService) QueryResourceGpu(ctx context.Context, r *view.GpuResourceQueryRequest) (*view.GpuQueryResponse, error) {
	if r == nil {
		return nil, gpuInvalid()
	}
	return s.businessQuery(ctx, 0, r.ResourceKind, r.ResourceId, r.Data, false, false)
}

func (s *GpuMonitoringService) execute(ctx context.Context, t *gpu.GpuTarget, platform bool, tenant, actor string, cs []string, d *view.GpuQueryData, latest bool) (*view.GpuQueryResponse, error) {
	if s.monitoring == nil {
		return nil, errors.ServiceUnavailable("GPU_SOURCE_UNAVAILABLE", "GPU monitoring unavailable")
	}
	if err := gpuTenantListReserve(ctx, t, len(cs)); err != nil {
		return nil, err
	}
	var r *gpu.QueryGpuResponse
	var e error
	transportTenant := tenant
	if platform {
		transportTenant = ""
	}
	if latest {
		r, e = s.monitoring.QueryGpuLatest(ctx, platform, transportTenant, actor, &gpu.QueryGpuLatestRequest{Target: t, MetricCodes: cs})
	} else {
		start, end := d.Start, d.End
		if start == nil || end == nil {
			var err error
			start, end, err = gpuWindow(d)
			if err != nil {
				return nil, err
			}
		}
		r, e = s.monitoring.QueryGpuRange(ctx, platform, transportTenant, actor, &gpu.QueryGpuRangeRequest{Target: t, MetricCodes: cs, Start: start, End: end})
	}
	if e != nil {
		return nil, gpuDependency(e)
	}
	if r == nil || len(r.Series) < len(cs) || len(r.Series) > 12 || r.QueryTime == nil {
		return nil, errors.ServiceUnavailable("GPU_INVALID_RESPONSE", "invalid GPU monitoring response")
	}
	// Only the public DTO is projected. Internal scope watermark and runtime
	// identities never enter tenant JSON, including on partially known scopes.
	out := &view.GpuQueryResponse{QueryTime: r.QueryTime, Start: r.Start, End: r.End, EffectiveStepSeconds: r.EffectiveStepSeconds, IndexComplete: r.IndexComplete, IncludedResourceKinds: r.IncludedResourceKinds, UncoveredResourceKinds: r.UncoveredResourceKinds}
	for _, series := range r.Series {
		if series == nil {
			return nil, metricResponseInvalid()
		}
		raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(series)
		if err != nil {
			return nil, metricResponseInvalid()
		}
		item := new(view.GpuSeries)
		if err = protojson.Unmarshal(raw, item); err != nil {
			return nil, metricResponseInvalid()
		}
		out.Series = append(out.Series, item)
	}
	raw, e := protojson.Marshal(out)
	if e != nil {
		return nil, metricResponseInvalid()
	}
	if len(raw) > 8<<20 {
		return nil, gpuBudget()
	}
	return out, nil
}
