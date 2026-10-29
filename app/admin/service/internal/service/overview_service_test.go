package service

import (
	"context"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	inferencev1 "go-wind-admin/api/gen/go/inference/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/types/known/emptypb"
)

type overviewInstancesFake struct {
	totals map[string]int64
	err    error
	calls  int
}

func (f *overviewInstancesFake) ListInstances(_ context.Context, r *view.ListInstancesRequest) (*view.ListInstancesResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &view.ListInstancesResponse{Total: f.totals[r.GetObservedState()]}, nil
}

type overviewInferenceFake struct {
	items []*inferencev1.InferenceView
	total int64
	err   error
	calls int
}

func (f *overviewInferenceFake) ListInference(_ context.Context, r *inferencev1.ListInferenceRequest) (*inferencev1.ListInferenceResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	start := int((r.GetPage() - 1) * r.GetPageSize())
	end := start + int(r.GetPageSize())
	if start > len(f.items) {
		start = len(f.items)
	}
	if end > len(f.items) {
		end = len(f.items)
	}
	return &inferencev1.ListInferenceResponse{Items: f.items[start:end], Total: f.total}, nil
}

type overviewModelsFake struct {
	totals     map[string]int64
	unfiltered int64
	err        error
	calls      int
}

func (f *overviewModelsFake) ListModels(_ context.Context, in *catalogv1.ListModelsRequest) (*catalogv1.ListModelsResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if in.GetStatus() == "" {
		return &catalogv1.ListModelsResponse{Total: f.unfiltered}, nil
	}
	return &catalogv1.ListModelsResponse{Total: f.totals[in.GetStatus()]}, nil
}

type overviewAuthFake struct {
	entry, models, inference error
	entryCalls, sourceCalls  int
}

func (a *overviewAuthFake) Authorize(_ context.Context, _, _ uint32, _, _ string) error {
	a.entryCalls++
	return a.entry
}

func (a *overviewAuthFake) AuthorizeSource(_ context.Context, _, _ uint32, _, module string) error {
	a.sourceCalls++
	if module == "MODEL" {
		return a.models
	}
	return a.inference
}

func overviewCtx() context.Context {
	return auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7})
}

// overviewQuotaFake 记录调用次数与被查询的租户，用于验证只读本租户且只调用一次。
type overviewQuotaFake struct {
	accounts []*quotapb.QuotaAccountView
	err      error
	calls    int
	lastTid  uint32
}

func (f *overviewQuotaFake) ListTenantAccounts(_ context.Context, tid uint32) ([]*quotapb.QuotaAccountView, error) {
	f.calls++
	f.lastTid = tid
	if f.err != nil {
		return nil, f.err
	}
	return f.accounts, nil
}

func overviewService(instances overviewInstanceLister, inference overviewInferenceLister, models overviewModelLister, authorization OverviewAuthorizer, quota ...overviewQuotaReader) *OverviewService {
	var q overviewQuotaReader
	if len(quota) > 0 {
		q = quota[0]
	}
	return NewOverviewService(nil, instances, inference, models, q, authorization)
}

func overviewIdentity(t *testing.T, label string, total, primary, degraded, other int64) {
	t.Helper()
	if total != primary+degraded+other {
		t.Fatalf("%s identity broken: total=%d primary=%d degraded=%d other=%d", label, total, primary, degraded, other)
	}
}

func TestOverviewAggregatesCardsAndFoldsPending(t *testing.T) {
	instances := &overviewInstancesFake{totals: map[string]int64{"running": 2, "stopped": 1, "pending": 3}}
	inference := &overviewInferenceFake{
		items: []*inferencev1.InferenceView{
			{DesiredState: "running", Phase: "ready"},
			{DesiredState: "stopped", Phase: "stopped"},
			{DesiredState: "stopped", Phase: "stopped"},
			{DesiredState: "deleted", Phase: "ready"},
			{DesiredState: "running", Phase: "deploying"},
		},
	}
	inference.total = int64(len(inference.items))
	models := &overviewModelsFake{totals: map[string]int64{"pending": 1, "importing": 2, "ready": 4, "error": 1}, unfiltered: 8}
	s := overviewService(instances, inference, models, &overviewAuthFake{})

	out, err := s.GetOverview(overviewCtx(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("overview failed: %v", err)
	}
	i := out.GetInstances()
	if i.GetTotal() != 6 || i.GetByState()["running"] != 2 || i.GetByState()["stopped"] != 1 ||
		i.GetByState()["failed"] != 3 || i.GetByState()["pending"] != 0 || i.GetOther() != 1 {
		t.Fatalf("instances=%+v", i)
	}
	overviewIdentity(t, "instances", i.GetTotal(), i.GetByState()["running"], i.GetByState()["failed"], i.GetOther())
	if _, ok := i.GetByState()["deleted"]; ok {
		t.Fatal("instances must not expose deleted")
	}

	inf := out.GetInferenceServices()
	if inf.GetTotal() != 4 || inf.GetByStatus()["running"] != 1 || inf.GetByStatus()["stopped"] != 2 ||
		inf.GetByStatus()["failed"] != 1 || inf.GetOther() != 2 {
		t.Fatalf("inference=%+v", inf)
	}
	overviewIdentity(t, "inference", inf.GetTotal(), inf.GetByStatus()["running"], inf.GetByStatus()["failed"], inf.GetOther())

	m := out.GetModels()
	if m.GetTotal() != 8 || m.GetByStatus()["ready"] != 4 || m.GetByStatus()["error"] != 1 || m.GetOther() != 3 {
		t.Fatalf("models=%+v", m)
	}
	overviewIdentity(t, "models", m.GetTotal(), m.GetByStatus()["ready"], m.GetByStatus()["error"], m.GetOther())

	kb := out.GetKnowledgeBases()
	if kb.GetTotal() != 0 || kb.GetOther() != 0 || len(kb.GetByStatus()) != 2 || kb.GetByStatus()["active"] != 0 || kb.GetByStatus()["rebuilding"] != 0 {
		t.Fatalf("knowledge bases=%+v", kb)
	}
	if instances.calls != 3 || inference.calls != 1 || models.calls != 5 {
		t.Fatalf("calls instances=%d inference=%d models=%d", instances.calls, inference.calls, models.calls)
	}
}

func TestOverviewDegradesSingleSource(t *testing.T) {
	instances := &overviewInstancesFake{totals: map[string]int64{"running": 1}}
	inference := &overviewInferenceFake{items: []*inferencev1.InferenceView{{DesiredState: "running", Phase: "ready"}}}
	inference.total = 1
	models := &overviewModelsFake{err: kratoserrors.ServiceUnavailable("MODEL_UNAVAILABLE", "down")}
	s := overviewService(instances, inference, models, &overviewAuthFake{})

	out, err := s.GetOverview(overviewCtx(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("degraded overview must stay 200: %v", err)
	}
	if out.GetModels().GetTotal() != 0 || out.GetModels().GetOther() != 0 || out.GetModels().GetByStatus()["ready"] != 0 {
		t.Fatalf("models not degraded: %+v", out.GetModels())
	}
	if out.GetInstances().GetTotal() != 1 || out.GetInferenceServices().GetTotal() != 1 {
		t.Fatalf("healthy sources lost: %+v", out)
	}
}

func TestOverviewDegradesOnSourcePermissionDenied(t *testing.T) {
	instances := &overviewInstancesFake{totals: map[string]int64{"running": 1}}
	inference := &overviewInferenceFake{items: []*inferencev1.InferenceView{{DesiredState: "running", Phase: "ready"}}}
	inference.total = 1
	models := &overviewModelsFake{totals: map[string]int64{"ready": 4}, unfiltered: 4}
	s := overviewService(instances, inference, models, &overviewAuthFake{models: data.ErrActionAuthorizationDenied})

	out, err := s.GetOverview(overviewCtx(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("degraded overview must stay 200: %v", err)
	}
	if models.calls != 0 {
		t.Fatal("denied source must not be queried")
	}
	if out.GetModels().GetTotal() != 0 || out.GetInstances().GetTotal() != 1 {
		t.Fatalf("degraded auth: %+v", out)
	}
}

func TestOverviewAllSourcesFailStillReturnsZeroCards(t *testing.T) {
	failure := kratoserrors.ServiceUnavailable("SOURCE_UNAVAILABLE", "down")
	instances := &overviewInstancesFake{err: failure}
	inference := &overviewInferenceFake{err: failure}
	models := &overviewModelsFake{err: failure}
	s := overviewService(instances, inference, models, &overviewAuthFake{})

	out, err := s.GetOverview(overviewCtx(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range []struct {
		name   string
		total  int64
		other  int64
		counts map[string]int64
	}{
		{"instances", out.GetInstances().GetTotal(), out.GetInstances().GetOther(), out.GetInstances().GetByState()},
		{"inference", out.GetInferenceServices().GetTotal(), out.GetInferenceServices().GetOther(), out.GetInferenceServices().GetByStatus()},
		{"models", out.GetModels().GetTotal(), out.GetModels().GetOther(), out.GetModels().GetByStatus()},
		{"knowledge_bases", out.GetKnowledgeBases().GetTotal(), out.GetKnowledgeBases().GetOther(), out.GetKnowledgeBases().GetByStatus()},
	} {
		if card.total != 0 || card.other != 0 {
			t.Fatalf("%s not zeroed: %+v", card.name, card)
		}
		for key, count := range card.counts {
			if count != 0 {
				t.Fatalf("%s key %s=%d", card.name, key, count)
			}
		}
	}
}

func TestOverviewEmptyTenantKeepsEveryKey(t *testing.T) {
	s := overviewService(
		&overviewInstancesFake{totals: map[string]int64{}},
		&overviewInferenceFake{},
		&overviewModelsFake{totals: map[string]int64{}},
		&overviewAuthFake{},
	)
	out, err := s.GetOverview(overviewCtx(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]map[string]int64{
		"instances":       out.GetInstances().GetByState(),
		"inference":       out.GetInferenceServices().GetByStatus(),
		"models":          out.GetModels().GetByStatus(),
		"knowledge_bases": out.GetKnowledgeBases().GetByStatus(),
	}
	wants := map[string][]string{
		"instances":       {"pending", "running", "stopped", "failed"},
		"inference":       {"pending", "deploying", "running", "stopping", "stopped", "failed"},
		"models":          {"pending", "importing", "ready", "error"},
		"knowledge_bases": {"active", "rebuilding"},
	}
	for name, keys := range wants {
		for _, key := range keys {
			if _, ok := checks[name][key]; !ok {
				t.Fatalf("%s missing key %s", name, key)
			}
		}
	}
}

func TestOverviewInferencePaginationIsCapped(t *testing.T) {
	inference := &overviewInferenceFake{total: 10000}
	s := overviewService(
		&overviewInstancesFake{totals: map[string]int64{}},
		inference,
		&overviewModelsFake{totals: map[string]int64{}},
		&overviewAuthFake{},
	)
	if _, err := s.GetOverview(overviewCtx(), &emptypb.Empty{}); err != nil {
		t.Fatal(err)
	}
	if inference.calls != overviewInferenceMaxPages {
		t.Fatalf("pages fetched=%d want %d", inference.calls, overviewInferenceMaxPages)
	}
}

func TestOverviewModelUnknownStatusOnlyWarns(t *testing.T) {
	models := &overviewModelsFake{totals: map[string]int64{"pending": 1, "importing": 2, "ready": 4, "error": 1}, unfiltered: 10}
	s := overviewService(&overviewInstancesFake{totals: map[string]int64{}}, &overviewInferenceFake{}, models, &overviewAuthFake{})
	out, err := s.GetOverview(overviewCtx(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetModels().GetTotal() != 8 || out.GetModels().GetOther() != 3 {
		t.Fatalf("unknown status leaked into counts: %+v", out.GetModels())
	}
	if models.calls != 5 {
		t.Fatalf("models calls=%d", models.calls)
	}
}

func TestOverviewEntryAuthorization(t *testing.T) {
	code := func(err error) int32 {
		if err == nil {
			t.Fatal("expected an error")
		}
		return kratoserrors.FromError(err).Code
	}
	s := overviewService(&overviewInstancesFake{totals: map[string]int64{}}, &overviewInferenceFake{}, &overviewModelsFake{totals: map[string]int64{}}, &overviewAuthFake{entry: data.ErrActionAuthorizationDenied})
	if _, err := s.GetOverview(overviewCtx(), &emptypb.Empty{}); code(err) != 403 {
		t.Fatalf("entry denial code=%v", code(err))
	}
	unavailable := overviewService(&overviewInstancesFake{totals: map[string]int64{}}, &overviewInferenceFake{}, &overviewModelsFake{totals: map[string]int64{}}, &overviewAuthFake{entry: data.ErrActionAuthorizationUnavailable})
	if _, err := unavailable.GetOverview(overviewCtx(), &emptypb.Empty{}); code(err) != 503 {
		t.Fatalf("entry unavailable code=%v", code(err))
	}
	fresh := overviewService(&overviewInstancesFake{totals: map[string]int64{}}, &overviewInferenceFake{}, &overviewModelsFake{totals: map[string]int64{}}, &overviewAuthFake{})
	if _, err := fresh.GetOverview(context.Background(), &emptypb.Empty{}); code(err) != 401 {
		t.Fatalf("missing principal code=%v", code(err))
	}
}

// TestOverviewGpuQuotaSelectsTwoCodes 验证只取两个 GPU 配额项，其余配额不进响应。
func TestOverviewGpuQuotaSelectsTwoCodes(t *testing.T) {
	quota := &overviewQuotaFake{accounts: []*quotapb.QuotaAccountView{
		{QuotaCode: "user.count", Unit: "user", Limit: "10", Occupied: "1", Available: "9", Configured: true},
		{QuotaCode: GpuSharedQuotaCode, Unit: "MiB", Limit: "98280", Occupied: "24570", Available: "73710", Configured: true, Enforcement: quotapb.Enforcement_ENFORCED},
		{QuotaCode: GpuPhysicalQuotaCode, Unit: "gpu", Limit: "2", Occupied: "1", Available: "1", Configured: true, Enforcement: quotapb.Enforcement_ENFORCED},
	}}
	s := overviewService(
		&overviewInstancesFake{totals: map[string]int64{}},
		&overviewInferenceFake{},
		&overviewModelsFake{totals: map[string]int64{}},
		&overviewAuthFake{},
		quota,
	)
	out, err := s.GetOverview(overviewCtx(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	shared := out.GetGpuQuota().GetSharedMemoryMib()
	if shared.GetQuotaCode() != GpuSharedQuotaCode || shared.GetUnit() != "MiB" || shared.GetLimit() != "98280" ||
		shared.GetOccupied() != "24570" || shared.GetAvailable() != "73710" || !shared.GetConfigured() ||
		shared.GetEnforcement() != "ENFORCED" || shared.GetOverLimit() {
		t.Fatalf("shared_memory_mib=%+v", shared)
	}
	physical := out.GetGpuQuota().GetPhysicalCount()
	if physical.GetQuotaCode() != GpuPhysicalQuotaCode || physical.GetUnit() != "gpu" || physical.GetLimit() != "2" ||
		physical.GetOccupied() != "1" || physical.GetAvailable() != "1" || !physical.GetConfigured() {
		t.Fatalf("physical_count=%+v", physical)
	}
	if quota.calls != 1 {
		t.Fatalf("quota reader calls=%d want 1", quota.calls)
	}
	if quota.lastTid != 7 {
		t.Fatalf("quota reader tenant=%d want 7 (principal only)", quota.lastTid)
	}
}

// TestOverviewGpuQuotaUnconfiguredFillsZero 验证未配置项补零且 configured=false，不返回 null。
func TestOverviewGpuQuotaUnconfiguredFillsZero(t *testing.T) {
	quota := &overviewQuotaFake{accounts: []*quotapb.QuotaAccountView{
		{QuotaCode: GpuSharedQuotaCode, Unit: "MiB", Limit: "0", Occupied: "0", Available: "0", Configured: false},
	}}
	s := overviewService(
		&overviewInstancesFake{totals: map[string]int64{}},
		&overviewInferenceFake{},
		&overviewModelsFake{totals: map[string]int64{}},
		&overviewAuthFake{},
		quota,
	)
	out, err := s.GetOverview(overviewCtx(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if out.GetGpuQuota().GetSharedMemoryMib() == nil || out.GetGpuQuota().GetPhysicalCount() == nil {
		t.Fatal("both fixed keys must be present")
	}
	if out.GetGpuQuota().GetSharedMemoryMib().GetConfigured() {
		t.Fatal("unconfigured quota must report configured=false")
	}
	physical := out.GetGpuQuota().GetPhysicalCount()
	if physical.GetQuotaCode() != GpuPhysicalQuotaCode || physical.GetUnit() != "gpu" ||
		physical.GetLimit() != "0" || physical.GetOccupied() != "0" || physical.GetAvailable() != "0" || physical.GetConfigured() {
		t.Fatalf("missing definition must be zeroed: %+v", physical)
	}
}

// TestOverviewGpuQuotaDegradesAlone 验证配额源失败只清空自身，其他卡片保持。
func TestOverviewGpuQuotaDegradesAlone(t *testing.T) {
	quota := &overviewQuotaFake{err: kratoserrors.ServiceUnavailable("QUOTA_UNAVAILABLE", "down")}
	s := overviewService(
		&overviewInstancesFake{totals: map[string]int64{"running": 1}},
		&overviewInferenceFake{},
		&overviewModelsFake{totals: map[string]int64{}},
		&overviewAuthFake{},
		quota,
	)
	out, err := s.GetOverview(overviewCtx(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("degraded overview must stay 200: %v", err)
	}
	if out.GetInstances().GetTotal() != 1 {
		t.Fatalf("healthy card lost: %+v", out.GetInstances())
	}
	if out.GetGpuQuota().GetSharedMemoryMib().GetLimit() != "0" || out.GetGpuQuota().GetSharedMemoryMib().GetConfigured() {
		t.Fatalf("gpu quota not degraded: %+v", out.GetGpuQuota())
	}
	if quota.calls != 1 {
		t.Fatalf("quota reader calls=%d want 1", quota.calls)
	}
}

// TestOverviewGpuQuotaMissingReaderKeepsKeys 验证 reader 未装配时仍返回两个零值键。
func TestOverviewGpuQuotaMissingReaderKeepsKeys(t *testing.T) {
	s := overviewService(
		&overviewInstancesFake{totals: map[string]int64{}},
		&overviewInferenceFake{},
		&overviewModelsFake{totals: map[string]int64{}},
		&overviewAuthFake{},
	)
	out, err := s.GetOverview(overviewCtx(), &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	card := out.GetGpuQuota()
	if card.GetSharedMemoryMib().GetQuotaCode() != GpuSharedQuotaCode ||
		card.GetPhysicalCount().GetQuotaCode() != GpuPhysicalQuotaCode {
		t.Fatalf("fixed keys missing: %+v", card)
	}
	if card.GetSharedMemoryMib().GetConfigured() || card.GetPhysicalCount().GetConfigured() {
		t.Fatalf("missing reader must not look configured: %+v", card)
	}
}
