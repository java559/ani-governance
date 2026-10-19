package data

import (
	"context"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"time"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/gpuusagesync"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

type GpuCreateCandidate struct {
	ID          int64
	TenantID    uint32
	OperationID string
}
type GpuUsageSyncRecord struct {
	TenantID                        uint32
	OperationID                     string
	Revision                        int64
	State, PayloadJSON, PayloadHash string
	AckedRevision, LeaseGeneration  int64
	AttemptCount                    int
}

func (r *QuotaLedgerRepo) ScanGpuCreatePage(ctx context.Context, afterID int64, limit int) (out []GpuCreateCandidate, err error) {
	if afterID < 0 || limit < 1 || limit > 1000 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid scan cursor or limit")
	}
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		rows, e := scanGpuCreatePage(ctx, tx, afterID, limit)
		if e != nil {
			return e
		}
		for _, v := range rows {
			out = append(out, GpuCreateCandidate{int64(v.ID), *v.TenantID, v.OperationID})
		}
		return nil
	})
	return
}
func (r *QuotaLedgerRepo) UpsertGpuUsageProjection(ctx context.Context, tid uint32, id string, revision int64, state, payload, hash string) error {
	if tid == 0 || id == "" || revision < 1 || payload == "" || hash == "" || (state != "DECLARED" && state != "ENDED") {
		return quotapb.ErrorInvalidQuotaRequest("%s", "invalid usage projection")
	}
	return r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		if _, e := lockQuotaTenant(ctx, tx, tid); e != nil {
			return e
		}
		op, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id)).Only(ctx)
		if e != nil {
			return e
		}
		if op.CreateOperationID != nil {
			return quotapb.ErrorInvalidQuotaRequest("%s", "projection requires CREATE")
		}
		old, e := tx.GpuUsageSync.Query().Where(gpuusagesync.TenantIDEQ(tid), gpuusagesync.OperationIDEQ(id)).Only(ctx)
		if e == nil && old.Revision == revision {
			if old.PayloadHash != hash || old.PayloadJSON != payload || old.State != state {
				return quotapb.ErrorIdempotencyConflict("%s", "same projection revision changed payload")
			}
			return nil
		}
		if e != nil && !ent.IsNotFound(e) {
			return e
		}
		e = upsertGpuUsageSync(ctx, tx, &ent.GpuUsageSync{TenantID: &tid, OperationID: id, Revision: revision, State: state, PayloadJSON: payload, PayloadHash: hash, ResourceTenantID: op.ResourceTenantID, OwnerService: op.OwnerService, ResourceID: op.ResourceID})
		return e
	})
}
func (r *QuotaLedgerRepo) ClaimGpuUsageSync(ctx context.Context, worker string, lease time.Duration, limit int) (out []GpuUsageSyncRecord, err error) {
	if worker == "" || lease <= 0 || limit < 1 || limit > 100 {
		return nil, quotapb.ErrorInvalidQuotaRequest("%s", "invalid projection claim")
	}
	var candidates []*ent.GpuUsageSync
	if err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		var e error
		candidates, e = tx.GpuUsageSync.Query().Where(gpuSyncDue).Order(ent.Asc(gpuusagesync.FieldUpdatedAt), ent.Asc(gpuusagesync.FieldID)).Limit(limit).All(ctx)
		return e
	}); err != nil {
		return
	}
	for _, c := range candidates {
		var record *GpuUsageSyncRecord
		e := r.transaction(ctx, func(tx *ent.Tx) error {
			ctx := appViewer.NewSystemViewerContext(ctx)
			v, e := claimGpuUsageSync(ctx, tx, *c.TenantID, c.OperationID, worker, lease)
			if e != nil {
				return e
			}
			record = &GpuUsageSyncRecord{*v.TenantID, v.OperationID, v.Revision, v.State, v.PayloadJSON, v.PayloadHash, v.AckedRevision, v.LeaseGeneration, v.AttemptCount}
			return nil
		})
		if ent.IsNotFound(e) {
			continue
		}
		if e != nil {
			return out, e
		}
		out = append(out, *record)
	}
	return
}
func (r *QuotaLedgerRepo) AckGpuUsageSync(ctx context.Context, tid uint32, id string, revision, generation int64) (ok bool, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
		if clockErr != nil {
			return clockErr
		}
		ctx := appViewer.NewSystemViewerContext(ctx)
		n, e := tx.GpuUsageSync.Update().SetUpdatedAt(databaseNow).Where(gpuusagesync.TenantIDEQ(tid), gpuusagesync.OperationIDEQ(id), gpuusagesync.RevisionEQ(revision), gpuusagesync.LeaseGenerationEQ(generation), gpuusagesync.LeaseUntilNotNil(), gpuSyncUnacked).SetAckedRevision(revision).ClearLeaseUntil().ClearLastErrorCode().Save(ctx)
		ok = n == 1
		return e
	})
	return
}
func (r *QuotaLedgerRepo) RetryGpuUsageSync(ctx context.Context, tid uint32, id string, revision, generation int64, next time.Time, code string) (ok bool, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
		if clockErr != nil {
			return clockErr
		}
		ctx := appViewer.NewSystemViewerContext(ctx)
		n, e := tx.GpuUsageSync.Update().SetUpdatedAt(databaseNow).Where(gpuusagesync.TenantIDEQ(tid), gpuusagesync.OperationIDEQ(id), gpuusagesync.RevisionEQ(revision), gpuusagesync.LeaseGenerationEQ(generation), gpuusagesync.LeaseUntilNotNil(), gpuSyncUnacked).ClearLeaseUntil().SetNillableNextAttemptAt(&next).SetNillableLastErrorCode(&code).Save(ctx)
		ok = n == 1
		return e
	})
	return
}

// LoadGpuProjectionSource holds the original operation lock while reading all
// accounting facts, so concurrent release/delete cannot split the snapshot.
func (r *QuotaLedgerRepo) LoadGpuProjectionSource(ctx context.Context, tid uint32, id string) (op *ent.QuotaOperation, charges []*ent.QuotaCharge, hasDelete bool, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		v, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id)).ForUpdate().Only(ctx)
		if e != nil {
			return e
		}
		if v.CreateOperationID != nil {
			return quotapb.ErrorInvalidQuotaRequest("%s", "projection source must be CREATE")
		}
		rows, e := tx.QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tid), quotacharge.OperationIDEQ(id)).Order(ent.Asc(quotacharge.FieldQuotaCode), ent.Asc(quotacharge.FieldChargeID)).All(ctx)
		if e != nil {
			return e
		}
		if e = validateOperationCharges(v, rows); e != nil {
			return e
		}
		hasDelete, e = tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.CreateOperationIDEQ(id), quotaoperation.OwnerServiceEQ(v.OwnerService)).Exist(ctx)
		if e != nil {
			return e
		}
		op = v.Unwrap()
		for _, c := range rows {
			charges = append(charges, c.Unwrap())
		}
		return nil
	})
	return
}

// BlockGpuUsageSync records a permanent payload/contract error without touching
// business dispatch or balances. A later source revision re-enables projection.
func (r *QuotaLedgerRepo) BlockGpuUsageSync(ctx context.Context, tid uint32, id string, revision, generation int64, code string) (ok bool, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
		if clockErr != nil {
			return clockErr
		}
		ctx := appViewer.NewSystemViewerContext(ctx)
		n, e := tx.GpuUsageSync.Update().SetUpdatedAt(databaseNow).Where(gpuusagesync.TenantIDEQ(tid), gpuusagesync.OperationIDEQ(id), gpuusagesync.RevisionEQ(revision), gpuusagesync.LeaseGenerationEQ(generation), gpuusagesync.LeaseUntilNotNil(), gpuSyncUnacked).SetRetryBlocked(true).ClearLeaseUntil().SetNillableLastErrorCode(&code).Save(ctx)
		ok = n == 1
		return e
	})
	return
}
