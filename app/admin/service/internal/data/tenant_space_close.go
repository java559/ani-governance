package data

import (
	"context"
	"fmt"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	kratosErrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

type TenantSpaceCloseIntent struct {
	TenantID                      uint32
	ResourceTenantID, OperationID string
}

// RequestTenantSpaceClose persists a one-way close intent before destructive
// tenant operations. Existing open leases are revoked in the same transaction.
func (r *TenantRepo) RequestTenantSpaceClose(ctx context.Context, tenantID uint32) (*TenantSpaceCloseIntent, bool, error) {
	if tenantID == 0 {
		return nil, false, fmt.Errorf("invalid tenant")
	}
	ctx = appViewer.NewSystemViewerContext(ctx)
	tx, err := r.entClient.Client().Tx(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	intent, closed, err := beginTenantSpaceClose(ctx, tx, tenantID, supportsRowLock(r.entClient))
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	return intent, closed, nil
}

// beginTenantSpaceClose shares the caller's transaction so a destructive
// action cannot race the transition or silently discard the close intent.
func beginTenantSpaceClose(ctx context.Context, tx *ent.Tx, tenantID uint32, rowLock bool) (*TenantSpaceCloseIntent, bool, error) {
	if tenantID == 0 {
		return nil, false, fmt.Errorf("invalid tenant")
	}
	query := tx.Tenant.Query().Where(tenant.IDEQ(tenantID))
	if rowLock {
		query = query.ForUpdate()
	}
	row, err := query.Only(ctx)
	if err != nil {
		return nil, false, err
	}
	if !tenantSpaceUUID(row.ResourceTenantID) {
		return nil, false, fmt.Errorf("invalid resource tenant")
	}
	if row.TenantSpaceOperationID == nil && row.TenantSpaceState != tenant.TenantSpaceStateLEGACY {
		return nil, false, fmt.Errorf("missing tenant space operation")
	}
	operation := uuid.NewString()
	if row.TenantSpaceOperationID != nil {
		operation = *row.TenantSpaceOperationID
	}
	if !tenantSpaceUUID(operation) {
		return nil, false, fmt.Errorf("invalid tenant space operation")
	}
	intent := &TenantSpaceCloseIntent{TenantID: tenantID, ResourceTenantID: row.ResourceTenantID, OperationID: operation}
	closed := row.TenantSpaceState == tenant.TenantSpaceStateCLOSED
	if !closed && row.TenantSpaceState != tenant.TenantSpaceStateCLOSING {
		// Pending commands may have reached Resource even if their ACK was lost.
		// Both registration and this check hold the same tenant row lock.
		// SQLite fixtures do not contain this PostgreSQL-only journal table.
		if rowLock {
			var pending bool
			if err := tx.SandboxScan(ctx, `SELECT EXISTS(SELECT 1 FROM sys_sandbox_create_intents WHERE tenant_id=$1 AND NOT acknowledged)`, []any{tenantID}, &pending); err != nil {
				return nil, false, err
			}
			if pending {
				return nil, false, kratosErrors.Conflict("SANDBOX_CREATE_PENDING", "Sandbox 创建结果尚未确认，请稍后重试")
			}
		}
		update := tx.Tenant.UpdateOneID(tenantID).SetTenantSpaceState(tenant.TenantSpaceStateCLOSING).ClearTenantSpaceLeaseOwner().ClearTenantSpaceLeaseUntil().ClearTenantSpaceNextAttemptAt()
		// Legacy rows have never had an operation. Set it only once under the
		// tenant lock; Ent intentionally makes the field immutable thereafter.
		if row.TenantSpaceOperationID == nil {
			update.Modify(func(u *entsql.UpdateBuilder) { u.Set(tenant.FieldTenantSpaceOperationID, operation) })
		}
		_, err = update.Save(ctx)
		if err != nil {
			return nil, false, err
		}
	}
	return intent, closed, nil
}

// ClaimTenantSpaceClose uses a retry delay only for scheduling. Concurrent or
// late Close calls are safe because Resource uses the same permanent tombstone.
func (r *TenantRepo) ClaimTenantSpaceClose(ctx context.Context, delay time.Duration) (*TenantSpaceCloseIntent, error) {
	if delay < time.Microsecond || delay > 5*time.Minute {
		return nil, fmt.Errorf("invalid close retry delay")
	}
	ctx = appViewer.NewSystemViewerContext(ctx)
	candidates, err := r.entClient.Client().Tenant.Query().Where(tenantSpaceCloseDue).Order(ent.Asc(tenant.FieldID)).Limit(32).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		tx, err := r.entClient.Client().Tx(ctx)
		if err != nil {
			return nil, err
		}
		row, err := tx.Tenant.Query().Where(tenant.IDEQ(candidate.ID), tenantSpaceCloseDue).ForUpdate().Only(ctx)
		if ent.IsNotFound(err) {
			_ = tx.Rollback()
			continue
		}
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if row.TenantSpaceOperationID == nil || !tenantSpaceUUID(*row.TenantSpaceOperationID) || !tenantSpaceUUID(row.ResourceTenantID) {
			_ = tx.Rollback()
			return nil, fmt.Errorf("invalid tenant close intent")
		}
		_, err = tx.Tenant.UpdateOneID(row.ID).Modify(func(u *entsql.UpdateBuilder) { u.Set(tenant.FieldTenantSpaceNextAttemptAt, tenantSpaceAfter(delay)) }).Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return &TenantSpaceCloseIntent{TenantID: row.ID, ResourceTenantID: row.ResourceTenantID, OperationID: *row.TenantSpaceOperationID}, nil
	}
	return nil, nil
}

func tenantSpaceCloseDue(s *entsql.Selector) {
	s.Where(entsql.And(entsql.EQ(s.C(tenant.FieldTenantSpaceState), string(tenant.TenantSpaceStateCLOSING)), entsql.Or(entsql.IsNull(s.C(tenant.FieldTenantSpaceNextAttemptAt)), entsql.LTE(s.C(tenant.FieldTenantSpaceNextAttemptAt), entsql.Expr("statement_timestamp()")))))
}

// CompleteTenantSpaceClose requires an exact closed response from Resource.
func (r *TenantRepo) CompleteTenantSpaceClose(ctx context.Context, intent *TenantSpaceCloseIntent) (bool, error) {
	if intent == nil || intent.TenantID == 0 || !tenantSpaceUUID(intent.ResourceTenantID) || !tenantSpaceUUID(intent.OperationID) {
		return false, fmt.Errorf("invalid tenant close intent")
	}
	ctx = appViewer.NewSystemViewerContext(ctx)
	n, err := r.entClient.Client().Tenant.Update().Where(tenant.IDEQ(intent.TenantID), tenant.ResourceTenantIDEQ(intent.ResourceTenantID), tenant.TenantSpaceOperationIDEQ(intent.OperationID), tenant.TenantSpaceStateEQ(tenant.TenantSpaceStateCLOSING)).SetTenantSpaceState(tenant.TenantSpaceStateCLOSED).ClearTenantSpaceNextAttemptAt().Save(ctx)
	return n == 1, err
}
