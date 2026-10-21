package service

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
)

func TestQuotaRetryBackoffBound(t *testing.T) {
	for attempt, seconds := range []int{1, 1, 2, 4, 8, 16, 30, 30} {
		if got := backoffForAttempt(attempt); got != time.Duration(seconds)*time.Second {
			t.Fatalf("attempt %d: %s, want %ds", attempt, got, seconds)
		}
	}
}

func TestGpuProjectionOnlyGpuTerminal(t *testing.T) {
	p, _, _ := gpuVector(t)
	business := instanceSettingsBusiness(t)
	business.ComputeType = "gpu"
	business.GpuExecution = p.Resolved.Execution
	business.GpuDisplay = p.Resolved.Selection
	body, e := res.CanonicalBusinessPayload(business)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(body)
	c := GpuCanonical{SchemaVersion: 3, InstanceContractVersion: 2, GpuRequest: p.Request, BusinessPayload: body, BusinessPayloadDigest: hex.EncodeToString(sum[:]), MeteringVersion: GpuMeteringVersion, QuotaItems: []GpuQuotaItem{{QuotaCode: GpuSharedQuotaCode, Units: 8192}, {QuotaCode: "storage.bytes", Units: 20}}}
	raw, err := gpuCanonicalJSON(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 0, 0, 0, 123, time.UTC)
	tid := uint32(1)
	op := &ent.QuotaOperation{InstanceContractVersion: 2, TenantID: &tid, CreatedAt: &now, OperationID: "10000000-0000-4000-8000-000000000001", ResourceID: "10000000-0000-4000-8000-000000000002", ResourceTenantID: "10000000-0000-4000-8000-000000000003", OwnerService: InstanceOwner, Action: InstanceCreateAction, CanonicalRequest: string(raw), DispatchState: quotaoperation.DispatchStateAcked, AttemptCount: 1}
	charges := []*ent.QuotaCharge{{TenantID: &tid, OperationID: op.OperationID, ChargeID: "gpu", QuotaCode: GpuSharedQuotaCode, OriginalUnits: 8192}, {TenantID: &tid, OperationID: op.OperationID, ChargeID: "storage", QuotaCode: "storage.bytes", OriginalUnits: 20}}
	declared, e := deriveGpuProjection(op, charges, false)
	if e != nil || declared.Revision != 1 {
		t.Fatal(declared, e)
	}
	charges[1].ReleasedUnits = 20
	nonGpu, e := deriveGpuProjection(op, charges, true)
	if e != nil || nonGpu.Revision != 1 || nonGpu.PayloadDigest != declared.PayloadDigest {
		t.Fatal(nonGpu, e)
	}
	charges[0].ReleasedUnits = 1
	partial, e := deriveGpuProjection(op, charges, true)
	if e != nil || partial.Revision != 1 {
		t.Fatal(partial, e)
	}
	charges[0].ReleasedUnits = 8192
	charges[1].ReleasedUnits = 0
	if noDelete, err := deriveGpuProjection(op, charges, false); err != nil || noDelete.State != acc.UsageState_ENDED {
		t.Fatal("authenticated owner release must end the ledger projection without DELETE", noDelete, err)
	}
	ended, e := deriveGpuProjection(op, charges, true)
	if e != nil || ended.Revision != 2 || ended.EndReason != acc.UsageEndReason_OWNER_RESOURCE_RELEASED {
		t.Fatal(ended, e)
	}
	if ended.SourceOperationCreatedAt != declared.SourceOperationCreatedAt {
		t.Fatal("created_at changed")
	}
	if _, e = deriveGpuProjection(op, nil, true); e == nil {
		t.Fatal("empty charges ended")
	}
	if _, e = deriveGpuProjection(op, charges[1:], true); e == nil {
		t.Fatal("missing GPU charge accepted")
	}
	op.DispatchState = quotaoperation.DispatchStateCanceledUnsent
	op.AttemptCount = 0
	if _, e = deriveGpuProjection(op, charges, true); e == nil {
		t.Fatal("partial local refund accepted")
	}
	charges[1].ReleasedUnits = 20
	canceled, e := deriveGpuProjection(op, charges, true)
	if e != nil || canceled.EndReason != acc.UsageEndReason_GOVERNANCE_CANCELED_UNSENT {
		t.Fatal(canceled, e)
	}
}
