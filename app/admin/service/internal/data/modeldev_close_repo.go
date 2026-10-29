package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
	"go-wind-admin/app/admin/service/internal/data/ent/predicate"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

// Generation belongs to Governance's durable source command, not ModelDev's
// independently allocated creation fence. A user Stop never resets this intent.
type ModelDevStopIntent struct {
	TenantID                                                      uint32
	ResourceTenantID, OperationID, ExecutionID, ExecutionSpecHash string
	Generation                                                    uint64
	RequestedActor                                                string
	RequestedAt                                                   time.Time
}

func (r *ModelDevAcceptanceRepo) AcceptStop(ctx context.Context, tenantID uint32, resourceTenantID, executionID, actor string) (*ModelDevStopIntent, bool, error) {
	if tenantID == 0 || !modelDevCanonicalUUID(resourceTenantID) || !modelDevCanonicalUUID(executionID) || !validModelDevClientActor(actor) {
		return nil, false, fmt.Errorf("%w: modeldev stop scope", cpup01.ErrInvalidArgument)
	}
	var intent *ModelDevStopIntent
	var replayed bool
	err := r.acceptanceTransaction(ctx, func(tx *ent.Tx) error {
		if _, err := tx.Tenant.Query().Where(tenant.IDEQ(tenantID), tenant.ResourceTenantIDEQ(resourceTenantID)).ForUpdate().Only(ctx); err != nil {
			return err
		}
		row, err := tx.ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(tenantID), modeldevacceptance.ResourceTenantIDEQ(resourceTenantID), modeldevacceptance.ExecutionIDEQ(executionID)).ForUpdate().Only(ctx)
		if err != nil {
			return err
		}
		if err := validateModelDevStopAcceptanceIdentity(row); err != nil {
			return err
		}
		if row.StopIntentGeneration == 0 {
			_, err = tx.ModelDevAcceptance.Update().Where(modeldevacceptance.IDEQ(row.ID), modeldevacceptance.TenantIDEQ(tenantID), modeldevacceptance.StopIntentGenerationEQ(0)).
				SetStopIntentGeneration(1).SetStopRequestedActor(actor).SetCloseDispatchState(modeldevacceptance.CloseDispatchStateQUEUED).
				Modify(func(u *entsql.UpdateBuilder) {
					u.Set(modeldevacceptance.FieldStopRequestedAt, entsql.Expr("statement_timestamp()"))
				}).Save(ctx)
			if err != nil {
				return err
			}
			row, err = tx.ModelDevAcceptance.Query().Where(modeldevacceptance.IDEQ(row.ID), modeldevacceptance.TenantIDEQ(tenantID)).Only(ctx)
			if err != nil {
				return err
			}
		} else {
			replayed = true
		}
		intent, err = modelDevStopIntentFromRow(row)
		return err
	})
	if ent.IsNotFound(err) {
		return nil, false, ErrModelDevAcceptanceNotFound
	}
	if err != nil {
		return nil, false, err
	}
	return intent, replayed, nil
}

// Closing requires both the immutable accepted identity and the current
// creation codec. Matching byte digests never authorize a retired command.
func validateModelDevStopAcceptanceIdentity(row *ent.ModelDevAcceptance) error {
	invalid := fmt.Errorf("invalid persisted modeldev stop identity")
	if row == nil || row.TenantID == nil || *row.TenantID == 0 || row.Action != ModelDevCreateAction || row.IdempotencyKey == "" ||
		!modelDevCanonicalUUID(row.ResourceTenantID) || !modelDevCanonicalUUID(row.OperationID) || !modelDevCanonicalUUID(row.ExecutionID) ||
		!validModelDevClientActor(row.Actor) || !modelDevDigest(row.IntentHash) || !modelDevDigest(row.ExecutionSpecHash) ||
		len(row.IntentCanonical) == 0 || len(row.SnapshotCanonical) == 0 || row.AcceptedAt.IsZero() ||
		row.AcceptedAt.Year() < 1 || row.AcceptedAt.Year() > 9999 || row.AcceptedAt.Nanosecond()%1000 != 0 {
		return invalid
	}
	intentHash, snapshotHash := sha256.Sum256(row.IntentCanonical), sha256.Sum256(row.SnapshotCanonical)
	if hex.EncodeToString(intentHash[:]) != row.IntentHash || hex.EncodeToString(snapshotHash[:]) != row.ExecutionSpecHash {
		return invalid
	}
	_, err := modelDevAcceptanceFromRow(row)
	return err
}

func modelDevStopIntentFromRow(row *ent.ModelDevAcceptance) (*ModelDevStopIntent, error) {
	if err := validateModelDevStopAcceptanceIdentity(row); err != nil {
		return nil, err
	}
	if row.StopIntentGeneration <= 0 || row.StopRequestedAt == nil || row.StopRequestedActor == nil || row.CloseDispatchState == modeldevacceptance.CloseDispatchStateIDLE {
		return nil, fmt.Errorf("invalid persisted modeldev stop intent")
	}
	intent := &ModelDevStopIntent{TenantID: *row.TenantID, ResourceTenantID: row.ResourceTenantID, OperationID: row.OperationID, ExecutionID: row.ExecutionID, ExecutionSpecHash: row.ExecutionSpecHash, Generation: uint64(row.StopIntentGeneration), RequestedActor: *row.StopRequestedActor, RequestedAt: *row.StopRequestedAt}
	if _, err := modelDevCloseRequest(*intent); err != nil {
		return nil, err
	}
	return intent, nil
}

type ModelDevCloseDeliveryClaim struct {
	Intent                        *ModelDevStopIntent
	LeaseOwner                    string
	LeaseGeneration, AttemptCount int64
}

func (r *ModelDevAcceptanceRepo) ClaimCloseDelivery(ctx context.Context, workerID string, lease time.Duration) (*ModelDevCloseDeliveryClaim, error) {
	return r.claimCloseDelivery(ctx, workerID, lease)
}

// ClaimCloseDeliveryFor is the single-execution maintenance path. Its scope is
// the original committed Stop, never a caller-selected replacement identity.
// It shares the worker lease and cannot reopen a blocked or acknowledged row.
func (r *ModelDevAcceptanceRepo) ClaimCloseDeliveryFor(ctx context.Context, intent ModelDevStopIntent, workerID string, lease time.Duration) (*ModelDevCloseDeliveryClaim, error) {
	if _, err := modelDevCloseRequest(intent); err != nil || intent.Generation > math.MaxInt64 {
		return nil, fmt.Errorf("%w: modeldev close identity", cpup01.ErrInvalidArgument)
	}
	return r.claimCloseDelivery(ctx, workerID, lease,
		modeldevacceptance.TenantIDEQ(intent.TenantID), modeldevacceptance.ResourceTenantIDEQ(intent.ResourceTenantID),
		modeldevacceptance.OperationIDEQ(intent.OperationID), modeldevacceptance.ExecutionIDEQ(intent.ExecutionID),
		modeldevacceptance.ExecutionSpecHashEQ(intent.ExecutionSpecHash), modeldevacceptance.StopIntentGenerationEQ(int64(intent.Generation)),
		modeldevacceptance.StopRequestedActorEQ(intent.RequestedActor), modeldevacceptance.StopRequestedAtEQ(intent.RequestedAt))
}

func (r *ModelDevAcceptanceRepo) claimCloseDelivery(ctx context.Context, workerID string, lease time.Duration, target ...predicate.ModelDevAcceptance) (*ModelDevCloseDeliveryClaim, error) {
	if !validModelDevLeaseOwner(workerID) || lease < time.Microsecond || lease > 5*time.Minute {
		return nil, fmt.Errorf("%w: modeldev close lease", cpup01.ErrInvalidArgument)
	}
	ctx = appViewer.NewSystemViewerContext(ctx)
	candidates, err := r.entClient.Client().ModelDevAcceptance.Query().Where(target...).Where(modelDevCloseDeliveryDue).Order(ent.Asc(modeldevacceptance.FieldID)).Limit(32).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.TenantID == nil || *candidate.TenantID == 0 {
			return nil, fmt.Errorf("invalid modeldev close tenant")
		}
		var claim *ModelDevCloseDeliveryClaim
		err := r.acceptanceTransaction(ctx, func(tx *ent.Tx) error {
			if _, err := tx.Tenant.Query().Where(tenant.IDEQ(*candidate.TenantID)).ForUpdate().Only(ctx); err != nil {
				return err
			}
			scope := []predicate.ModelDevAcceptance{modeldevacceptance.TenantIDEQ(*candidate.TenantID), modeldevacceptance.ResourceTenantIDEQ(candidate.ResourceTenantID), modeldevacceptance.OperationIDEQ(candidate.OperationID), modelDevCloseDeliveryDue}
			scope = append(scope, target...)
			row, err := tx.ModelDevAcceptance.Query().Where(scope...).ForUpdate().Only(ctx)
			if err != nil {
				return err
			}
			intent, invalidStop := modelDevStopIntentFromRow(row)
			code := ""
			switch {
			case invalidStop != nil:
				code = "INVALID_COMMAND"
			case row.CloseLeaseGeneration == math.MaxInt64:
				code = "LEASE_EXHAUSTED"
			case row.CloseAttemptCount == math.MaxInt64:
				code = "ATTEMPTS_EXHAUSTED"
			}
			if code != "" {
				_, err := tx.ModelDevAcceptance.Update().Where(scope...).SetCloseDispatchState(modeldevacceptance.CloseDispatchStateUNKNOWN).SetCloseRetryBlocked(true).SetCloseLastErrorCode(code).ClearCloseLeaseOwner().ClearCloseLeaseUntil().ClearCloseNextAttemptAt().Save(ctx)
				return err
			}
			n, err := tx.ModelDevAcceptance.Update().Where(scope...).SetCloseDispatchState(modeldevacceptance.CloseDispatchStateDISPATCHING).
				SetCloseAttemptCount(row.CloseAttemptCount + 1).SetCloseLeaseGeneration(row.CloseLeaseGeneration + 1).SetCloseLeaseOwner(workerID).
				ClearCloseNextAttemptAt().ClearCloseLastErrorCode().SetCloseRetryBlocked(false).
				Modify(func(u *entsql.UpdateBuilder) {
					u.Set(modeldevacceptance.FieldCloseLeaseUntil, modelDevDeliveryAfter(lease))
				}).Save(ctx)
			if err != nil || n != 1 {
				return err
			}
			claim = &ModelDevCloseDeliveryClaim{Intent: intent, LeaseOwner: workerID, LeaseGeneration: row.CloseLeaseGeneration + 1, AttemptCount: row.CloseAttemptCount + 1}
			return nil
		})
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if claim != nil {
			return claim, nil
		}
	}
	return nil, nil
}

func (r *ModelDevAcceptanceRepo) AckCloseDelivery(ctx context.Context, claim *ModelDevCloseDeliveryClaim, receipt ModelDevCloseReceipt) (bool, error) {
	return r.withModelDevCloseClaim(ctx, claim, func(ctx context.Context, tx *ent.Tx, row *ent.ModelDevAcceptance, fence []predicate.ModelDevAcceptance) (bool, error) {
		intent, err := modelDevStopIntentFromRow(row)
		if err != nil {
			return false, err
		}
		if err := validateModelDevCloseReceipt(receipt, *intent); err != nil {
			return false, err
		}
		canonical, err := json.Marshal(receipt)
		if err != nil || len(canonical) > 4096 {
			return false, fmt.Errorf("invalid modeldev close receipt")
		}
		n, err := tx.ModelDevAcceptance.Update().Where(fence...).SetCloseDispatchState(modeldevacceptance.CloseDispatchStateACKED).SetCloseReceiptCanonical(canonical).
			ClearCloseLeaseOwner().ClearCloseLeaseUntil().ClearCloseNextAttemptAt().ClearCloseLastErrorCode().SetCloseRetryBlocked(false).Save(ctx)
		return n == 1, err
	})
}

func (r *ModelDevAcceptanceRepo) DeferCloseDelivery(ctx context.Context, claim *ModelDevCloseDeliveryClaim, failure ModelDevDeliveryFailure) (bool, error) {
	switch failure.Code {
	case "INVALID_COMMAND", "COMMAND_CONFLICT":
		if !failure.Permanent {
			return false, fmt.Errorf("invalid modeldev permanent close failure")
		}
	case "OWNER_UNAVAILABLE", "INVALID_ACK":
		if failure.Permanent {
			return false, fmt.Errorf("invalid modeldev transient close failure")
		}
	default:
		return false, fmt.Errorf("invalid modeldev close failure")
	}
	return r.withModelDevCloseClaim(ctx, claim, func(ctx context.Context, tx *ent.Tx, row *ent.ModelDevAcceptance, fence []predicate.ModelDevAcceptance) (bool, error) {
		update := tx.ModelDevAcceptance.Update().Where(fence...).SetCloseDispatchState(modeldevacceptance.CloseDispatchStateUNKNOWN).SetCloseLastErrorCode(failure.Code).SetCloseRetryBlocked(failure.Permanent).ClearCloseLeaseOwner().ClearCloseLeaseUntil()
		if failure.Permanent {
			update.ClearCloseNextAttemptAt()
		} else {
			delay := min(30*time.Second, time.Second<<min(max(row.CloseAttemptCount-1, 0), 5))
			update.Modify(func(u *entsql.UpdateBuilder) {
				u.Set(modeldevacceptance.FieldCloseNextAttemptAt, modelDevDeliveryAfter(delay))
			})
		}
		n, err := update.Save(ctx)
		return n == 1, err
	})
}

func (r *ModelDevAcceptanceRepo) withModelDevCloseClaim(ctx context.Context, claim *ModelDevCloseDeliveryClaim, work func(context.Context, *ent.Tx, *ent.ModelDevAcceptance, []predicate.ModelDevAcceptance) (bool, error)) (bool, error) {
	if claim == nil || claim.Intent == nil || claim.LeaseGeneration <= 0 || claim.AttemptCount <= 0 || !validModelDevLeaseOwner(claim.LeaseOwner) {
		return false, fmt.Errorf("invalid modeldev close claim")
	}
	i := claim.Intent
	if i.TenantID == 0 || i.Generation == 0 || i.Generation > math.MaxInt64 || !modelDevCanonicalUUID(i.ResourceTenantID) || !modelDevCanonicalUUID(i.OperationID) || !modelDevCanonicalUUID(i.ExecutionID) {
		return false, fmt.Errorf("invalid modeldev close identity")
	}
	if _, err := modelDevCloseRequest(*i); err != nil {
		return false, err
	}
	ctx = appViewer.NewSystemViewerContext(ctx)
	var changed bool
	err := r.acceptanceTransaction(ctx, func(tx *ent.Tx) error {
		if _, err := tx.Tenant.Query().Where(tenant.IDEQ(i.TenantID)).ForUpdate().Only(ctx); err != nil {
			return err
		}
		fence := []predicate.ModelDevAcceptance{modeldevacceptance.TenantIDEQ(i.TenantID), modeldevacceptance.ResourceTenantIDEQ(i.ResourceTenantID), modeldevacceptance.OperationIDEQ(i.OperationID), modeldevacceptance.ExecutionIDEQ(i.ExecutionID), modeldevacceptance.ExecutionSpecHashEQ(i.ExecutionSpecHash), modeldevacceptance.StopIntentGenerationEQ(int64(i.Generation)),
			modeldevacceptance.StopRequestedActorEQ(i.RequestedActor), modeldevacceptance.StopRequestedAtEQ(i.RequestedAt),
			modeldevacceptance.CloseLeaseOwnerEQ(claim.LeaseOwner), modeldevacceptance.CloseLeaseGenerationEQ(claim.LeaseGeneration), modeldevacceptance.CloseAttemptCountEQ(claim.AttemptCount), modeldevacceptance.CloseDispatchStateEQ(modeldevacceptance.CloseDispatchStateDISPATCHING), modelDevCloseLeaseLive}
		row, err := tx.ModelDevAcceptance.Query().Where(fence...).ForUpdate().Only(ctx)
		if err != nil {
			return err
		}
		if _, err := modelDevStopIntentFromRow(row); err != nil {
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

func modelDevCloseDeliveryDue(s *entsql.Selector) {
	s.Where(entsql.And(entsql.EQ(s.C(modeldevacceptance.FieldCloseRetryBlocked), false),
		entsql.Or(entsql.IsNull(s.C(modeldevacceptance.FieldCloseNextAttemptAt)), entsql.LTE(s.C(modeldevacceptance.FieldCloseNextAttemptAt), entsql.Expr("statement_timestamp()"))),
		entsql.Or(entsql.In(s.C(modeldevacceptance.FieldCloseDispatchState), "QUEUED", "UNKNOWN"),
			entsql.And(entsql.EQ(s.C(modeldevacceptance.FieldCloseDispatchState), "DISPATCHING"), entsql.LTE(s.C(modeldevacceptance.FieldCloseLeaseUntil), entsql.Expr("statement_timestamp()"))))))
}
func modelDevCloseLeaseLive(s *entsql.Selector) {
	s.Where(entsql.GT(s.C(modeldevacceptance.FieldCloseLeaseUntil), entsql.Expr("statement_timestamp()")))
}
