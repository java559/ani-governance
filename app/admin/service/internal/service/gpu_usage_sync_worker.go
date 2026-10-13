package service

import (
	"context"
	"encoding/json"
	"fmt"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"sync"
	"time"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type GpuUsageSynchronizer interface {
	SyncGpuUsage(context.Context, *acc.SyncGpuUsageRequest) (*acc.SyncGpuUsageResponse, error)
}

// GpuUsageSyncWorker is a derived read-model worker. It never calls owner
// dispatch or changes charges, receipts, balances, or business attempt counts.
type GpuUsageSyncWorker struct {
	ledger   *data.QuotaLedgerRepo
	client   GpuUsageSynchronizer
	workerID string
	afterID  int64
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	onError  func(error)
}

func NewGpuUsageSyncWorker(ledger *data.QuotaLedgerRepo, client GpuUsageSynchronizer, onError func(error)) *GpuUsageSyncWorker {
	return &GpuUsageSyncWorker{ledger: ledger, client: client, workerID: "gpu-sync-" + uuid.NewString(), onError: onError}
}

func (w *GpuUsageSyncWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return fmt.Errorf("GPU sync worker already started")
	}
	if w.ledger == nil || w.client == nil {
		return fmt.Errorf("GPU sync dependencies missing")
	}
	ctx, w.cancel = context.WithCancel(ctx)
	w.done = make(chan struct{})
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := w.Step(ctx); err != nil && ctx.Err() == nil && w.onError != nil {
				w.onError(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}
func (w *GpuUsageSyncWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Step scans a bounded page and always wraps at the end. Old operations whose
// charges are released later are revisited, including after a lost sync row.
func (w *GpuUsageSyncWorker) Step(ctx context.Context) error {
	candidates, err := w.ledger.ScanGpuCreatePage(ctx, w.afterID, 64)
	if err != nil {
		return err
	}
	var first error
	for _, candidate := range candidates {
		op, charges, hasDelete, e := w.ledger.LoadGpuProjectionSource(ctx, candidate.TenantID, candidate.OperationID)
		if e == nil {
			var projection *acc.GpuUsageProjection
			projection, e = deriveGpuProjection(op, charges, hasDelete)
			if e == nil {
				var payload []byte
				payload, e = json.Marshal(projection)
				if e == nil {
					e = w.ledger.UpsertGpuUsageProjection(ctx, candidate.TenantID, candidate.OperationID, int64(projection.Revision), projection.State.String(), string(payload), projection.PayloadDigest)
				}
			}
		}
		if e != nil && first == nil {
			first = e
		}
		w.afterID = candidate.ID
	}
	if len(candidates) < 64 {
		w.afterID = 0
	}
	// Claim immediately before RPC, outside the derivation and database locks.
	records, err := w.ledger.ClaimGpuUsageSync(ctx, w.workerID, 15*time.Second, 1)
	if err != nil {
		return err
	}
	for _, record := range records {
		var p acc.GpuUsageProjection
		e := json.Unmarshal([]byte(record.PayloadJSON), &p)
		permanent := e != nil
		if e == nil {
			d, de := GpuProjectionDigest(&p)
			if de != nil || d != record.PayloadHash || p.PayloadDigest != d || int64(p.Revision) != record.Revision || p.Ref == nil || p.Ref.CreateOperationId != record.OperationID {
				e = fmt.Errorf("invalid persisted GPU sync payload")
				permanent = true
			}
		}
		if e == nil {
			rpcCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			ack, callErr := w.client.SyncGpuUsage(rpcCtx, &acc.SyncGpuUsageRequest{RequestId: uuid.NewString(), Projection: &p})
			cancel()
			e = callErr
			if callErr != nil {
				// An immutable receiver conflict cannot heal by retrying the
				// same projection. Keep its precise contract reason; ordinary
				// Aborted transport errors still follow the retry policy.
				permanent = isPermanentContractError(callErr) || kerrors.FromError(callErr).Reason == "USAGE_PROJECTION_CONFLICT"
			}
			if e == nil {
				if ack == nil || !proto.Equal(ack.Ref, p.Ref) || ack.AppliedRevision < p.Revision || ack.AppliedRevision > 2 || (ack.AppliedRevision == 1 && ack.State != acc.UsageState_DECLARED) || (ack.AppliedRevision == 2 && ack.State != acc.UsageState_ENDED) {
					e = fmt.Errorf("invalid GPU sync acknowledgement")
					permanent = true
				}
			}
		}
		if e == nil {
			_, e = w.ledger.AckGpuUsageSync(ctx, record.TenantID, record.OperationID, record.Revision, record.LeaseGeneration)
		} else {
			code := status.Code(e).String()
			if reason := kerrors.FromError(e).Reason; reason == "USAGE_PROJECTION_CONFLICT" {
				code = reason
			}
			if code == "Unknown" {
				code = "INVALID_SYNC_PAYLOAD_OR_ACK"
			}
			var writeErr error
			if permanent {
				_, writeErr = w.ledger.BlockGpuUsageSync(ctx, record.TenantID, record.OperationID, record.Revision, record.LeaseGeneration, code)
			} else {
				_, writeErr = w.ledger.RetryGpuUsageSync(ctx, record.TenantID, record.OperationID, record.Revision, record.LeaseGeneration, time.Now().Add(backoffForAttempt(record.AttemptCount)), code)
			}
			if writeErr != nil {
				e = writeErr
			}
		}
		if e != nil && first == nil {
			first = e
		}
	}
	return first
}

func deriveGpuProjection(op *ent.QuotaOperation, charges []*ent.QuotaCharge, hasDelete bool) (*acc.GpuUsageProjection, error) {
	if op == nil || op.TenantID == nil || *op.TenantID == 0 || op.CreateOperationID != nil || op.CreatedAt == nil || !supportedGpuOwner(op.OwnerService) {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid GPU projection source")
	}
	c, err := DecodeGpuCanonical([]byte(op.CanonicalRequest))
	if err != nil {
		return nil, err
	}
	refs := make([]QuotaChargeRef, 0, len(charges))
	gpuEnded := true
	allEnded := true
	for _, q := range charges {
		if q == nil || q.TenantID == nil || *q.TenantID != *op.TenantID || q.OperationID != op.OperationID || q.ReleasedUnits < 0 || q.ReleasedUnits > q.OriginalUnits {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid original GPU accounting")
		}
		refs = append(refs, QuotaChargeRef{ChargeID: q.ChargeID, QuotaCode: q.QuotaCode, ChargedUnits: q.OriginalUnits})
		if q.ReleasedUnits != q.OriginalUnits {
			allEnded = false
			if q.QuotaCode == GpuPhysicalQuotaCode || q.QuotaCode == GpuSharedQuotaCode {
				gpuEnded = false
			}
		}
	}
	if _, err = GpuChargeSubset(c, refs); err != nil {
		return nil, err
	}
	p := &acc.GpuUsageProjection{Ref: &acc.GpuUsageRef{TenantId: op.ResourceTenantID, OwnerService: op.OwnerService, ResourceId: op.ResourceID, CreateOperationId: op.OperationID}, Revision: 1, State: acc.UsageState_DECLARED, Plan: c.GpuPlan, SourceOperationCreatedAt: op.CreatedAt.UTC().Format(time.RFC3339Nano)}
	suffix := "accepted"
	if op.DispatchState == quotaoperation.DispatchStateCanceledUnsent {
		if op.AttemptCount != 0 || !allEnded {
			return nil, quotapb.ErrorInvalidQuotaRequest("%s", "inconsistent local GPU cancellation")
		}
		p.Revision = 2
		p.State = acc.UsageState_ENDED
		p.EndReason = acc.UsageEndReason_GOVERNANCE_CANCELED_UNSENT
		suffix = "canceled-unsent"
	} else if gpuEnded {
		p.Revision = 2
		p.State = acc.UsageState_ENDED
		p.EndReason = acc.UsageEndReason_OWNER_RESOURCE_RELEASED
		suffix = "gpu-fully-released"
	}
	p.SourceFactRef = "quota-operation:" + op.OperationID + ":" + suffix
	p.PayloadDigest, err = GpuProjectionDigest(p)
	return p, err
}
