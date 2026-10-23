package data

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
	"go-wind-admin/app/admin/service/internal/data/ent/predicate"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

type ModelDevDeliveryClaim struct {
	Acceptance      *ModelDevAcceptance
	LeaseOwner      string
	LeaseGeneration int64
	AttemptCount    int64
}

// ClaimDelivery is an internal, managed queue operation. Its one bounded global
// scan only locates candidates; each mutation locks tenant then acceptance and
// rechecks both tenant identities, operation and due time in PostgreSQL.
func (r *ModelDevAcceptanceRepo) ClaimDelivery(ctx context.Context, workerID string, lease time.Duration) (*ModelDevDeliveryClaim, error) {
	if !validModelDevLeaseOwner(workerID) || lease < time.Microsecond || lease > 5*time.Minute {
		return nil, fmt.Errorf("%w: modeldev delivery lease", cpup01.ErrInvalidArgument)
	}
	ctx = appViewer.NewSystemViewerContext(ctx)
	candidates, err := r.entClient.Client().ModelDevAcceptance.Query().Where(modelDevDeliveryDue).
		Order(ent.Asc(modeldevacceptance.FieldID)).Limit(32).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.TenantID == nil || *candidate.TenantID == 0 {
			return nil, fmt.Errorf("invalid modeldev delivery tenant")
		}
		var claim *ModelDevDeliveryClaim
		err := r.acceptanceTransaction(ctx, func(tx *ent.Tx) error {
			if _, err := tx.Tenant.Query().Where(tenant.IDEQ(*candidate.TenantID)).ForUpdate().Only(ctx); err != nil {
				return err
			}
			scope := []predicate.ModelDevAcceptance{
				modeldevacceptance.TenantIDEQ(*candidate.TenantID),
				modeldevacceptance.ResourceTenantIDEQ(candidate.ResourceTenantID),
				modeldevacceptance.OperationIDEQ(candidate.OperationID), modelDevDeliveryDue,
			}
			row, err := tx.ModelDevAcceptance.Query().Where(scope...).ForUpdate().Only(ctx)
			if err != nil {
				return err
			}
			accepted, invalidOriginal := modelDevAcceptanceFromRow(row)
			code := ""
			switch {
			case invalidOriginal != nil:
				code = "INVALID_COMMAND"
			case row.LeaseGeneration == math.MaxInt64:
				code = "LEASE_EXHAUSTED"
			case row.AttemptCount == math.MaxInt64:
				code = "ATTEMPTS_EXHAUSTED"
			}
			if code != "" {
				// Preserve the original bytes and stop reselecting an undeliverable
				// row. The same bounded scan can still claim a later healthy row.
				_, err := tx.ModelDevAcceptance.Update().Where(scope...).
					SetDispatchState(modeldevacceptance.DispatchStateUNKNOWN).SetRetryBlocked(true).SetLastErrorCode(code).
					ClearLeaseOwner().ClearLeaseUntil().ClearNextAttemptAt().Save(ctx)
				return err
			}
			n, err := tx.ModelDevAcceptance.Update().Where(scope...).
				SetDispatchState(modeldevacceptance.DispatchStateDISPATCHING).
				SetAttemptCount(row.AttemptCount + 1).SetLeaseGeneration(row.LeaseGeneration + 1).
				SetLeaseOwner(workerID).ClearNextAttemptAt().ClearLastErrorCode().SetRetryBlocked(false).
				Modify(func(u *entsql.UpdateBuilder) { u.Set(modeldevacceptance.FieldLeaseUntil, modelDevDeliveryAfter(lease)) }).Save(ctx)
			if err != nil || n != 1 {
				return err
			}
			accepted.DispatchState = string(modeldevacceptance.DispatchStateDISPATCHING)
			claim = &ModelDevDeliveryClaim{Acceptance: accepted, LeaseOwner: workerID, LeaseGeneration: row.LeaseGeneration + 1, AttemptCount: row.AttemptCount + 1}
			return nil
		})
		if ent.IsNotFound(err) {
			continue // Another worker won or this candidate was removed.
		}
		if err != nil {
			return nil, err // Never return a claim whose COMMIT failed.
		}
		if claim != nil {
			return claim, nil
		}
	}
	return nil, nil
}

func (r *ModelDevAcceptanceRepo) AckDelivery(ctx context.Context, claim *ModelDevDeliveryClaim, receipt ModelDevOwnerReceipt) (bool, error) {
	return r.withModelDevDeliveryClaim(ctx, claim, func(ctx context.Context, tx *ent.Tx, row *ent.ModelDevAcceptance, fence []predicate.ModelDevAcceptance) (bool, error) {
		// Reload and validate the committed original, not mutable caller payloads.
		accepted, err := modelDevAcceptanceFromRow(row)
		if err != nil {
			return false, err
		}
		canonical, err := encodeModelDevOwnerReceipt(receipt, accepted.Envelope())
		if err != nil {
			return false, err
		}
		n, err := tx.ModelDevAcceptance.Update().Where(fence...).
			SetDispatchState(modeldevacceptance.DispatchStateACKED).SetOwnerReceiptCanonical(canonical).
			ClearLeaseOwner().ClearLeaseUntil().ClearNextAttemptAt().ClearLastErrorCode().SetRetryBlocked(false).Save(ctx)
		return n == 1, err
	})
}

func (r *ModelDevAcceptanceRepo) DeferDelivery(ctx context.Context, claim *ModelDevDeliveryClaim, failure ModelDevDeliveryFailure) (bool, error) {
	switch failure.Code {
	case "INVALID_COMMAND", "COMMAND_CONFLICT":
		if !failure.Permanent {
			return false, fmt.Errorf("%w: modeldev permanent failure", cpup01.ErrInvalidArgument)
		}
	case "OWNER_UNAVAILABLE", "INVALID_ACK":
		if failure.Permanent {
			return false, fmt.Errorf("%w: modeldev transient failure", cpup01.ErrInvalidArgument)
		}
	default:
		return false, fmt.Errorf("%w: modeldev delivery failure", cpup01.ErrInvalidArgument)
	}
	return r.withModelDevDeliveryClaim(ctx, claim, func(ctx context.Context, tx *ent.Tx, row *ent.ModelDevAcceptance, fence []predicate.ModelDevAcceptance) (bool, error) {
		delay := min(30*time.Second, time.Second<<min(max(row.AttemptCount-1, 0), 5))
		update := tx.ModelDevAcceptance.Update().Where(fence...).
			SetDispatchState(modeldevacceptance.DispatchStateUNKNOWN).SetLastErrorCode(failure.Code).SetRetryBlocked(failure.Permanent).
			ClearLeaseOwner().ClearLeaseUntil()
		if failure.Permanent {
			update.ClearNextAttemptAt()
		} else {
			update.Modify(func(u *entsql.UpdateBuilder) {
				u.Set(modeldevacceptance.FieldNextAttemptAt, modelDevDeliveryAfter(delay))
			})
		}
		n, err := update.Save(ctx)
		return n == 1, err
	})
}

func (r *ModelDevAcceptanceRepo) withModelDevDeliveryClaim(ctx context.Context, claim *ModelDevDeliveryClaim, work func(context.Context, *ent.Tx, *ent.ModelDevAcceptance, []predicate.ModelDevAcceptance) (bool, error)) (bool, error) {
	if claim == nil || claim.Acceptance == nil || claim.LeaseGeneration <= 0 || claim.AttemptCount <= 0 || !validModelDevLeaseOwner(claim.LeaseOwner) {
		return false, fmt.Errorf("%w: modeldev delivery claim", cpup01.ErrInvalidArgument)
	}
	a := claim.Acceptance
	if err := validateModelDevAdmissionScope(a.Scope); err != nil {
		return false, err
	}
	for _, id := range []string{a.Scope.ResourceTenantID, a.OperationID} {
		if !modelDevCanonicalUUID(id) {
			return false, fmt.Errorf("%w: modeldev delivery identity", cpup01.ErrInvalidArgument)
		}
	}
	ctx = appViewer.NewSystemViewerContext(ctx)
	var changed bool
	err := r.acceptanceTransaction(ctx, func(tx *ent.Tx) error {
		if _, err := tx.Tenant.Query().Where(tenant.IDEQ(a.Scope.TenantID)).ForUpdate().Only(ctx); err != nil {
			return err
		}
		fence := []predicate.ModelDevAcceptance{
			modeldevacceptance.TenantIDEQ(a.Scope.TenantID), modeldevacceptance.ResourceTenantIDEQ(a.Scope.ResourceTenantID),
			modeldevacceptance.OperationIDEQ(a.OperationID), modeldevacceptance.LeaseOwnerEQ(claim.LeaseOwner),
			modeldevacceptance.LeaseGenerationEQ(claim.LeaseGeneration), modeldevacceptance.AttemptCountEQ(claim.AttemptCount),
			modeldevacceptance.DispatchStateEQ(modeldevacceptance.DispatchStateDISPATCHING), modelDevDeliveryLeaseLive,
		}
		row, err := tx.ModelDevAcceptance.Query().Where(fence...).ForUpdate().Only(ctx)
		if err != nil {
			return err
		}
		changed, err = work(ctx, tx, row, fence)
		return err
	})
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return changed, nil
}

func validModelDevLeaseOwner(owner string) bool {
	return owner != "" && len(owner) <= 128 && strings.TrimSpace(owner) == owner && !strings.ContainsAny(owner, "\r\n\t\x00")
}

// A fresh statement clock matters after waiting for the tenant lock: the
// transaction's start time could already be older than a lease or retry due.
func modelDevDeliveryDue(s *entsql.Selector) {
	s.Where(entsql.And(entsql.EQ(s.C(modeldevacceptance.FieldRetryBlocked), false),
		// Stop-before-create must first persist the owner's tombstone. An
		// already in-flight create remains subject to the owner's shared fence.
		entsql.Or(entsql.EQ(s.C(modeldevacceptance.FieldStopIntentGeneration), 0), entsql.EQ(s.C(modeldevacceptance.FieldCloseDispatchState), "ACKED")),
		entsql.Or(entsql.IsNull(s.C(modeldevacceptance.FieldNextAttemptAt)), entsql.LTE(s.C(modeldevacceptance.FieldNextAttemptAt), entsql.Expr("statement_timestamp()"))),
		entsql.Or(entsql.In(s.C(modeldevacceptance.FieldDispatchState), "QUEUED", "UNKNOWN"),
			entsql.And(entsql.EQ(s.C(modeldevacceptance.FieldDispatchState), "DISPATCHING"), entsql.LTE(s.C(modeldevacceptance.FieldLeaseUntil), entsql.Expr("statement_timestamp()"))))))
}

func modelDevDeliveryLeaseLive(s *entsql.Selector) {
	s.Where(entsql.GT(s.C(modeldevacceptance.FieldLeaseUntil), entsql.Expr("statement_timestamp()")))
}

func modelDevDeliveryAfter(delay time.Duration) entsql.Querier {
	return entsql.ExprFunc(func(b *entsql.Builder) {
		b.WriteString("statement_timestamp() + ").Arg(delay.Microseconds()).WriteString(" * INTERVAL '1 microsecond'")
	})
}
