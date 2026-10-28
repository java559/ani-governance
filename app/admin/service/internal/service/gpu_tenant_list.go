package service

import (
	"context"
	stderrors "errors"
	"strconv"
	"strings"
	"sync"
	"time"

	errors "github.com/go-kratos/kratos/v2/errors"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	gpu "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/gpu/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/gpu/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
)

type GpuTenantAdminService struct {
	admin.UnimplementedGpuTenantAdminServiceServer
	quotas      *data.QuotaAdminRepo
	monitoring  *GpuMonitoringService
	accelerator *AcceleratorService
}

func NewGpuTenantAdminService(quotas *data.QuotaAdminRepo, monitoring *GpuMonitoringService, accelerator *AcceleratorService) *GpuTenantAdminService {
	return &GpuTenantAdminService{quotas: quotas, monitoring: monitoring, accelerator: accelerator}
}

type gpuTenantListBudgetKey struct{}
type gpuTenantListIdentityKey struct{}
type gpuTenantListBudget struct {
	mu      sync.Mutex
	sources map[string]bool
	curves  int
}

func gpuTenantMonitoringIdentity(ctx context.Context, resolver ResourceTenantResolver, tenantID uint32) (string, error) {
	if mappings, ok := ctx.Value(gpuTenantListIdentityKey{}).(map[uint32]string); ok {
		value, found := mappings[tenantID]
		if !found || !metricUUID(value) {
			return "", errors.NotFound("GPU_NOT_FOUND", "GPU tenant mapping unavailable")
		}
		return value, nil
	}
	return resolver.ResourceTenantID(ctx, tenantID)
}

// gpuTenantListReserve runs at the common execution boundary, after trusted
// ACC resolution and before Obs. Other GPU routes retain their existing budget.
func gpuTenantListReserve(ctx context.Context, target *gpu.GpuTarget, curves int) error {
	b, ok := ctx.Value(gpuTenantListBudgetKey{}).(*gpuTenantListBudget)
	if !ok {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if target == nil || curves < 1 || b.curves+curves > 12 {
		return gpuBudget()
	}
	for _, scope := range target.Scopes {
		if scope == nil {
			return acceleratorInvalidResponse()
		}
		key := strings.Join([]string{scope.ClusterId, scope.NodeId, scope.NodeUid, scope.Namespace, scope.PodName, scope.ContainerName, strings.TrimPrefix(strings.ToLower(scope.GpuUuid), "gpu-"), scope.VdeviceId}, "\x00")
		b.sources[key] = true
		if len(b.sources) > 128 {
			return gpuBudget()
		}
	}
	b.curves += curves
	return nil
}

func (s *GpuTenantAdminService) ListGpuTenants(ctx context.Context, req *view.ListGpuTenantsRequest) (*view.ListGpuTenantsResponse, error) {
	if _, err := acceleratorPrincipal(ctx, true); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, gpuInvalid()
	}
	page, size := uint32(1), uint32(6)
	if req.Page != nil {
		page = *req.Page
	}
	if req.PageSize != nil {
		size = *req.PageSize
	}
	if page == 0 || size == 0 || size > 6 || req.TenantId != nil && *req.TenantId == 0 {
		return nil, gpuInvalid()
	}
	if s.quotas == nil || s.monitoring == nil {
		return nil, errors.ServiceUnavailable("GPU_SOURCE_UNAVAILABLE", "GPU tenant summary unavailable")
	}
	batch, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	batch = context.WithValue(batch, gpuTenantListBudgetKey{}, &gpuTenantListBudget{sources: map[string]bool{}})
	quotaPage, err := s.quotas.ListTenantAccountSummaryPage(batch, data.TenantQuotaAccountSummaryFilter{Page: page, PageSize: size, TenantID: req.TenantId})
	if err != nil {
		return nil, err
	}
	batch = context.WithValue(batch, gpuTenantListIdentityKey{}, quotaPage.ResourceTenantIDs)
	quotas := quotaPage.Items
	out := &view.ListGpuTenantsResponse{Items: make([]*view.GpuTenantCurrentSummary, len(quotas)), Total: quotaPage.Total}
	if len(quotas) == 0 {
		return out, nil
	}
	allocations, err := s.currentAllocations(batch, quotaPage.ResourceTenantIDs)
	if err != nil {
		if errors.Code(err) == 429 {
			return nil, gpuBudget()
		}
		if errors.Code(err) == 504 || batch.Err() != nil {
			return nil, errors.GatewayTimeout("GPU_QUERY_TIMEOUT", "GPU tenant page deadline exceeded")
		}
		allocations = map[uint32]*view.GpuTenantCurrentAllocation{}
		for _, quota := range quotas {
			allocations[quota.TenantId] = &view.GpuTenantCurrentAllocation{Status: "UNAVAILABLE", Reason: "GPU_ALLOCATION_UNAVAILABLE"}
		}
	}
	for i, quota := range quotas {
		out.Items[i] = &view.GpuTenantCurrentSummary{Quota: quota, Allocation: allocations[quota.TenantId]}
	}
	jobs := make(chan int, len(quotas))
	for i := range quotas {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	var failure error
	var failureOnce sync.Once
	fail := func(err error) { failureOnce.Do(func() { failure = err; cancel() }) }
	for n := 0; n < min(2, len(quotas)); n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if batch.Err() != nil {
					return
				}
				usage, err := s.monitoring.GetTenantGpuLatest(batch, &view.GpuTenantLatestRequest{TenantId: quotas[i].TenantId, MetricCodes: []string{"gpu.resource.memory_used_bytes", "gpu.resource.memory_limit_bytes"}})
				if err != nil {
					code := errors.Code(err)
					if code == 429 {
						fail(gpuBudget())
						return
					}
					if code == 504 || stderrors.Is(err, context.DeadlineExceeded) || batch.Err() != nil {
						fail(errors.GatewayTimeout("GPU_QUERY_TIMEOUT", "GPU tenant page deadline exceeded"))
						return
					}
					out.Items[i].UsageStatus = "UNAVAILABLE"
					out.Items[i].UsageReason = gpuTenantUsageReason(err)
					continue
				}
				out.Items[i].UsageStatus, out.Items[i].UsageReason = gpuTenantUsageStatus(usage)
				if out.Items[i].UsageStatus != "UNAVAILABLE" {
					out.Items[i].Usage = usage
				}
			}
		}()
	}
	workers.Wait()
	if failure != nil {
		return nil, failure
	}
	if err := batch.Err(); err != nil {
		return nil, errors.GatewayTimeout("GPU_QUERY_TIMEOUT", "GPU tenant page deadline exceeded")
	}
	return out, nil
}

// currentAllocations scans the platform inventory once, independently of the
// tenant page size. Unknown or unassociated allocations invalidate a complete
// per-tenant total, while verified commitments remain available as known parts.
func (s *GpuTenantAdminService) currentAllocations(ctx context.Context, tenantIDs map[uint32]string) (map[uint32]*view.GpuTenantCurrentAllocation, error) {
	if s.accelerator == nil || s.accelerator.client == nil || s.accelerator.client.Admin == nil {
		return nil, acceleratorUnavailable()
	}
	adminContext, err := s.accelerator.adminContext(ctx)
	if err != nil {
		return nil, err
	}
	byResource := map[string]uint32{}
	for id, resource := range tenantIDs {
		if metricUUID(resource) {
			byResource[resource] = id
		}
	}
	memory, count, incomplete := map[uint32]uint64{}, map[uint32]uint64{}, map[uint32]bool{}
	allComplete := true
	clusters, clusterTokens := map[string]bool{}, map[string]bool{"": true}
	bindings := map[string]bool{}
	sources := map[string]bool{}
	token := ""
	for page := 0; ; page++ {
		if page >= 64 {
			return nil, gpuBudget()
		}
		reply, err := s.accelerator.client.Admin.ListClusters(ctx, &acc.ListClustersRequest{Context: adminContext, Page: &acc.Page{Size: 100, Token: token}})
		if err != nil {
			return nil, mapAcceleratorError(err)
		}
		if reply == nil {
			return nil, acceleratorInvalidResponse()
		}
		for _, cluster := range reply.Items {
			if cluster == nil || !metricUUID(cluster.ClusterId) || clusters[cluster.ClusterId] {
				return nil, acceleratorInvalidResponse()
			}
			clusters[cluster.ClusterId] = true
			if len(clusters) > 16 {
				return nil, gpuBudget()
			}
			bindingToken, snapshot := "", ""
			bindingTokens := map[string]bool{"": true}
			for bindingPage := 0; ; bindingPage++ {
				if bindingPage >= 64 {
					return nil, gpuBudget()
				}
				r, err := s.accelerator.client.Admin.AdminListBindings(ctx, &acc.AdminListBindingsRequest{Context: adminContext, ClusterId: cluster.ClusterId, Page: &acc.Page{Size: 200, Token: bindingToken}})
				if err != nil {
					return nil, mapAcceleratorError(err)
				}
				if r == nil || r.Observation == nil {
					return nil, acceleratorInvalidResponse()
				}
				if !r.Observation.Complete {
					allComplete = false
				}
				if snapshot != "" && r.Observation.SnapshotRef != snapshot {
					return nil, errors.Conflict("GPU_SCOPE_CHANGED", "GPU allocation snapshot changed")
				}
				snapshot = r.Observation.SnapshotRef
				for _, binding := range r.Items {
					if binding == nil || binding.BindingId == "" || bindings[binding.BindingId] {
						return nil, acceleratorInvalidResponse()
					}
					bindings[binding.BindingId] = true
					if len(bindings) > 4096 {
						return nil, gpuBudget()
					}
					if binding.State == acc.AllocationState_RELEASE_CONFIRMED {
						continue
					}
					if binding.Pod == nil || binding.Pod.ClusterId != cluster.ClusterId || binding.MemoryMib < 0 || !metricUUID(binding.PhysicalDeviceId) || binding.Pod.Uid == "" || binding.Pod.ContainerName == "" {
						return nil, acceleratorInvalidResponse()
					}
					source := strings.Join([]string{cluster.ClusterId, binding.PhysicalDeviceId, binding.Pod.Uid, binding.Pod.ContainerName}, "\x00")
					if sources[source] {
						return nil, acceleratorInvalidResponse()
					}
					sources[source] = true
					if len(sources) > 128 {
						return nil, gpuBudget()
					}
					if binding.VerifiedUsageRef == nil || !metricUUID(binding.VerifiedUsageRef.TenantId) {
						allComplete = false
						continue
					}
					id, belongs := byResource[binding.VerifiedUsageRef.TenantId]
					if !belongs {
						continue
					}
					if binding.State != acc.AllocationState_ACTIVE || binding.Observation == nil || !binding.Observation.Complete {
						incomplete[id] = true
						continue
					}
					if ^uint64(0)-memory[id] < uint64(binding.MemoryMib) {
						return nil, acceleratorInvalidResponse()
					}
					memory[id] += uint64(binding.MemoryMib)
					count[id]++
				}
				if r.NextToken == "" {
					break
				}
				if bindingTokens[r.NextToken] {
					return nil, acceleratorInvalidResponse()
				}
				bindingTokens[r.NextToken], bindingToken = true, r.NextToken
			}
		}
		if reply.NextToken == "" {
			break
		}
		if clusterTokens[reply.NextToken] {
			return nil, acceleratorInvalidResponse()
		}
		clusterTokens[reply.NextToken], token = true, reply.NextToken
	}
	out := map[uint32]*view.GpuTenantCurrentAllocation{}
	for id, resource := range tenantIDs {
		value, number := strconv.FormatUint(memory[id], 10), count[id]
		row := &view.GpuTenantCurrentAllocation{KnownMemoryMib: &value, KnownBindingCount: &number, Status: "PARTIAL", Reason: "GPU_ALLOCATION_INCOMPLETE"}
		if count[id] == 0 && (!allComplete || incomplete[id]) {
			row.KnownMemoryMib, row.KnownBindingCount = nil, nil
		}
		if !metricUUID(resource) {
			row.Status, row.Reason, row.KnownMemoryMib, row.KnownBindingCount = "UNAVAILABLE", "GPU_TENANT_MAPPING_UNAVAILABLE", nil, nil
		} else if allComplete && !incomplete[id] {
			row.MemoryMib, row.BindingCount, row.Status, row.Reason = &value, &number, "AVAILABLE", ""
		}
		out[id] = row
	}
	return out, nil
}

func gpuTenantUsageReason(err error) string {
	switch errors.Reason(err) {
	case "GPU_SCOPE_CHANGED":
		return "GPU_SCOPE_CHANGED"
	case "GPU_NOT_FOUND", "TENANT_NOT_FOUND", "QUOTA_NOT_FOUND":
		return "GPU_TENANT_MAPPING_UNAVAILABLE"
	case "GPU_INVALID_RESPONSE", "ACCELERATOR_INVALID_RESPONSE":
		return "GPU_INVALID_RESPONSE"
	default:
		return "GPU_SOURCE_UNAVAILABLE"
	}
}

func gpuTenantUsageStatus(usage *view.GpuQueryResponse) (string, string) {
	if usage == nil || len(usage.Series) != 2 {
		return "UNAVAILABLE", "GPU_INVALID_RESPONSE"
	}
	allOK, noData := usage.IndexComplete, true
	codes := map[string]bool{}
	for _, series := range usage.Series {
		if series == nil || series.Quality == nil || len(series.Points) > 1 || codes[series.MetricCode] || series.MetricCode != "gpu.resource.memory_used_bytes" && series.MetricCode != "gpu.resource.memory_limit_bytes" {
			return "UNAVAILABLE", "GPU_INVALID_RESPONSE"
		}
		codes[series.MetricCode] = true
		allOK = allOK && series.Quality.Status == "OK"
		noData = noData && series.Quality.Status == "NO_DATA"
	}
	if allOK {
		return "AVAILABLE", ""
	}
	if noData {
		return "NO_DATA", "GPU_NO_DATA"
	}
	return "PARTIAL", "GPU_PARTIAL_DATA"
}
