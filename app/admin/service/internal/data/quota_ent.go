package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	quotapb "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"strconv"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/gpuusagesync"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
)

// quotaTransaction keeps every read and mutation on the same Ent transaction.
// Deferred rollback also covers panics; a failed rollback is retained with the
// original failure. No connection or driver is borrowed outside Ent.
func quotaTransaction(ctx context.Context, client *ent.Client, fn func(*ent.Tx) error) (err error) {
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback quota transaction: %w", rollbackErr))
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	err = tx.Commit()
	committed = err == nil
	return err
}

type quotaLockedTenant struct {
	*ent.Tenant
	DatabaseNow time.Time
}

// quotaDatabaseNow reads PostgreSQL's transaction timestamp through the same
// Ent transaction as the mutations. A constant SELECT has no dependency on
// catalog rows and preserves now() semantics even across multiple writes.
func quotaDatabaseNow(ctx context.Context, tx *ent.Tx) (time.Time, error) {
	var rows []struct {
		Now time.Time `json:"database_now"`
	}
	err := tx.QuotaDefinition.Query().Modify(func(s *entsql.Selector) {
		*s = *entsql.Dialect(s.Dialect()).SelectExpr(entsql.Expr("CURRENT_TIMESTAMP AS database_now"))
	}).Scan(ctx, &rows)
	if err != nil {
		return time.Time{}, err
	}
	if len(rows) != 1 {
		return time.Time{}, fmt.Errorf("quota transaction clock returned %d rows", len(rows))
	}
	return rows[0].Now, nil
}

// Admission, refunds, cancellation and dispatch claims take the tenant lock
// first. Read database time on this same transaction for subscription expiry.
func lockQuotaTenant(ctx context.Context, tx *ent.Tx, id uint32) (*quotaLockedTenant, error) {
	t, err := tx.Tenant.Query().Where(tenant.IDEQ(id), tenant.IDGT(0)).ForUpdate().Only(ctx)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		DatabaseNow time.Time `json:"database_now"`
	}
	err = tx.Tenant.Query().Where(tenant.IDEQ(id)).Modify(func(s *entsql.Selector) {
		s.SelectExpr(entsql.Expr("CURRENT_TIMESTAMP AS database_now"))
	}).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, &ent.NotFoundError{}
	}
	return &quotaLockedTenant{Tenant: t, DatabaseNow: rows[0].DatabaseNow}, nil
}

// Release has one owner-scoped locator before the tenant is known. All reads
// and writes following it explicitly retain that tenant and the original UUID.
func locateQuotaReleaseTenant(ctx context.Context, tx *ent.Tx, id, owner string) (uint32, error) {
	op, err := tx.QuotaOperation.Query().Where(quotaoperation.OperationIDEQ(id), quotaoperation.OwnerServiceEQ(owner), quotaoperation.CreateOperationIDIsNil()).Only(ctx)
	if err != nil {
		return 0, err
	}
	if op.TenantID == nil || *op.TenantID == 0 {
		return 0, quotapb.ErrorInvalidQuotaRequest("%s", "operation tenant required")
	}
	return *op.TenantID, nil
}

func quotaAccountNonnegative(delta int64) func(*entsql.Selector) {
	return func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			b.Ident(s.C(quotaaccount.FieldOccupiedUnits)).WriteString(" + ").Arg(delta).WriteString(" >= 0")
		}))
	}
}

func databaseDue(s *entsql.Selector, field string) *entsql.Predicate {
	return entsql.Or(entsql.IsNull(s.C(field)), entsql.LTE(s.C(field), entsql.Expr("CURRENT_TIMESTAMP")))
}
func quotaDispatchDue(s *entsql.Selector) {
	// ModelDev has one delivery queue: its immutable acceptance. Its original
	// quota operation carries accounting only and must never be dispatched here,
	// including after an unknown response or an expired lease.
	s.Where(entsql.And(entsql.NEQ(s.C(quotaoperation.FieldOwnerService), commandcontract.ModelDevOwner), entsql.EQ(s.C(quotaoperation.FieldRetryBlocked), false), databaseDue(s, quotaoperation.FieldNextAttemptAt),
		entsql.Or(entsql.In(s.C(quotaoperation.FieldDispatchState), "QUEUED", "UNKNOWN"),
			entsql.And(entsql.EQ(s.C(quotaoperation.FieldDispatchState), "DISPATCHING"), entsql.LTE(s.C(quotaoperation.FieldLeaseUntil), entsql.Expr("CURRENT_TIMESTAMP"))))))
}
func gpuSyncUnacked(s *entsql.Selector) {
	s.Where(entsql.ColumnsLT(s.C(gpuusagesync.FieldAckedRevision), s.C(gpuusagesync.FieldRevision)))
}
func gpuSyncDue(s *entsql.Selector) {
	gpuSyncUnacked(s)
	s.Where(entsql.And(entsql.EQ(s.C(gpuusagesync.FieldRetryBlocked), false), databaseDue(s, gpuusagesync.FieldNextAttemptAt), databaseDue(s, gpuusagesync.FieldLeaseUntil)))
}
func quotaLeaseUntil(lease time.Duration) entsql.Querier {
	return entsql.ExprFunc(func(b *entsql.Builder) {
		b.WriteString("CURRENT_TIMESTAMP + ").Arg(max(1, lease.Microseconds())).WriteString(" * INTERVAL '1 microsecond'")
	})
}
func claimQuotaOperation(ctx context.Context, tx *ent.Tx, tid uint32, id, worker string, lease time.Duration) (*ent.QuotaOperation, error) {
	databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
	if clockErr != nil {
		return nil, clockErr
	}
	op, err := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id), quotaDispatchDue).ForUpdate().Only(ctx)
	if err != nil {
		return nil, err
	}
	return tx.QuotaOperation.UpdateOne(op).SetUpdatedAt(databaseNow).Where(quotaoperation.TenantIDEQ(tid), quotaoperation.OperationIDEQ(id), quotaDispatchDue).
		SetDispatchState(quotaoperation.DispatchStateDispatching).AddAttemptCount(1).AddLeaseGeneration(1).SetLeaseOwner(worker).
		Modify(func(u *entsql.UpdateBuilder) { u.Set(quotaoperation.FieldLeaseUntil, quotaLeaseUntil(lease)) }).Save(ctx)
}
func claimGpuUsageSync(ctx context.Context, tx *ent.Tx, tid uint32, id, worker string, lease time.Duration) (*ent.GpuUsageSync, error) {
	databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
	if clockErr != nil {
		return nil, clockErr
	}
	row, err := tx.GpuUsageSync.Query().Where(gpuusagesync.TenantIDEQ(tid), gpuusagesync.OperationIDEQ(id), gpuSyncDue).ForUpdate().Only(ctx)
	if err != nil {
		return nil, err
	}
	return tx.GpuUsageSync.UpdateOne(row).SetUpdatedAt(databaseNow).Where(gpuusagesync.TenantIDEQ(tid), gpuusagesync.OperationIDEQ(id), gpuSyncDue).
		AddAttemptCount(1).AddLeaseGeneration(1).SetLeaseOwner(worker).
		Modify(func(u *entsql.UpdateBuilder) { u.Set(gpuusagesync.FieldLeaseUntil, quotaLeaseUntil(lease)) }).Save(ctx)
}

// New revisions invalidate the old lease atomically. Late projections cannot
// overwrite a newer revision, payload, retry decision or acknowledgement.
func upsertGpuUsageSync(ctx context.Context, tx *ent.Tx, v *ent.GpuUsageSync) error {
	databaseNow, clockErr := quotaDatabaseNow(ctx, tx)
	if clockErr != nil {
		return clockErr
	}
	err := tx.GpuUsageSync.Create().SetCreatedAt(databaseNow).SetUpdatedAt(databaseNow).SetTenantID(*v.TenantID).SetOperationID(v.OperationID).
		SetRevision(v.Revision).SetState(v.State).SetPayloadJSON(v.PayloadJSON).SetPayloadHash(v.PayloadHash).
		SetResourceTenantID(v.ResourceTenantID).SetOwnerService(v.OwnerService).SetResourceID(v.ResourceID).
		OnConflict(entsql.ConflictColumns(gpuusagesync.FieldTenantID, gpuusagesync.FieldOperationID),
			entsql.UpdateWhere(entsql.LT(gpuusagesync.FieldRevision, entsql.Expr("EXCLUDED.revision")))).
		Update(func(u *ent.GpuUsageSyncUpsert) {
			u.SetRevision(v.Revision).SetState(v.State).SetPayloadJSON(v.PayloadJSON).SetPayloadHash(v.PayloadHash).
				SetRetryBlocked(false).ClearNextAttemptAt().ClearLeaseOwner().ClearLeaseUntil().AddLeaseGeneration(1).SetUpdatedAt(databaseNow)
		}).Exec(ctx)
	// Ent upsert requests RETURNING id. A rejected older/equal revision
	// returns no row, which is the intended conditional no-op.
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// Only this bounded worker scan is global. Returned candidates carry a tenant;
// projection source reads and mutations always use tenant-scoped Ent predicates.
func scanGpuCreatePage(ctx context.Context, tx *ent.Tx, afterID int64, limit int) ([]*ent.QuotaOperation, error) {
	return tx.QuotaOperation.Query().Where(quotaoperation.CreateOperationIDIsNil(), func(s *entsql.Selector) {
		s.Where(entsql.GT(s.C(quotaoperation.FieldID), afterID))
		// The schema marker selects only accepted new-contract commands. Terminal
		// audit records from the cutover are never parsed as running payloads.
		s.Where(entsql.P(func(b *entsql.Builder) {
			column := s.C(quotaoperation.FieldCanonicalRequest)
			b.WriteString("(" + column + "::jsonb->>'schema_version' = '3' OR (" + column + "::jsonb->>'schema' = ").Arg(commandcontract.InferenceSchema).WriteString(" AND " + column + "::jsonb->>'resource_class' = ").Arg(strconv.FormatInt(int64(commandcontract.InferenceGPUClass), 10)).WriteString(") OR (" + s.C(quotaoperation.FieldOwnerService) + " = ").Arg(commandcontract.ModelDevOwner).WriteString(" AND " + column + "::jsonb->>'schema' = ").Arg(commandcontract.ModelDevGPUSchema).WriteString("))")
		}))

	}).Order(ent.Asc(quotaoperation.FieldID)).Limit(limit).All(ctx)
}
