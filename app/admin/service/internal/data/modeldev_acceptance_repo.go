package data

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
	"go-wind-admin/app/admin/service/internal/data/ent/predicate"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

const ModelDevCreateAction = "modeldev.execution.create"

var ErrModelDevIdempotencyConflict = errors.New("modeldev idempotency conflict")

var ErrModelDevAcceptanceNotFound = errors.New("modeldev acceptance not found")

var ErrModelDevAdmissionExpired = errors.New("modeldev admission deadline exceeded")

// ModelDevAdmissionScope is derived by Governance from the current verified
// Principal and its persisted resource tenant mapping, never from public JSON.
type ModelDevAdmissionScope struct {
	TenantID         uint32
	ResourceTenantID string
	Actor            string
	Action           string
	IdempotencyKey   string
}

// ModelDevFrozenCandidate is resolved before entering the database transaction.
// Replay returns the first accepted value rather than using new defaults or
// provider observations. All external resolution finishes before this transaction.
type ModelDevFrozenCandidate struct {
	OperationID string
	ExecutionID string
	Intent      cpup01.Intent
	Snapshot    cpup01.Snapshot
	AcceptedAt  time.Time
	// ResolvedGPU is the original ACC observation used by FinalizeAdmission.
	// It is accounted in the same transaction as the immutable acceptance.
	ResolvedGPU *acc.ResolvedGpuRequest
}

type ModelDevAcceptance struct {
	Scope             ModelDevAdmissionScope
	OperationID       string
	ExecutionID       string
	Intent            cpup01.Intent
	Snapshot          cpup01.Snapshot
	IntentHash        string
	ExecutionSpecHash string
	AcceptedAt        time.Time
	DispatchState     string
	OwnerReceipt      *ModelDevOwnerReceipt
}

// ModelDevAcceptanceRepo commits admission, its delivery intent and any GPU
// occupancy together. CPU admissions do not create a quota operation.
type ModelDevAcceptanceRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
	quota     *QuotaLedgerRepo
}

func NewModelDevAcceptanceRepo(client *entCrud.EntClient[*ent.Client]) *ModelDevAcceptanceRepo {
	return &ModelDevAcceptanceRepo{entClient: client, quota: &QuotaLedgerRepo{entClient: client}}
}

// FindAccepted retrieves the immutable original before resolving the current
// catalogue. Scope must come from current trusted identity and tenant mapping;
// the caller must recheck current authorization on every request. A miss is
// ErrModelDevAcceptanceNotFound, and a different intent conflicts. This read
// neither consumes the key nor checks the current submission gate.
func (r *ModelDevAcceptanceRepo) FindAccepted(ctx context.Context, scope ModelDevAdmissionScope, intent cpup01.Intent) (*ModelDevAcceptance, error) {
	if err := validateModelDevAdmissionScope(scope); err != nil {
		return nil, err
	}
	canonical, intentHash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		return nil, err
	}
	mapped, err := r.entClient.Client().Tenant.Query().Where(
		tenant.IDEQ(scope.TenantID), tenant.ResourceTenantIDEQ(scope.ResourceTenantID),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !mapped {
		return nil, fmt.Errorf("%w: modeldev tenant mapping", cpup01.ErrInvalidArgument)
	}
	row, err := r.entClient.Client().ModelDevAcceptance.Query().Where(
		modeldevacceptance.TenantIDEQ(scope.TenantID),
		modeldevacceptance.ResourceTenantIDEQ(scope.ResourceTenantID),
		modeldevacceptance.ActorEQ(scope.Actor),
		modeldevacceptance.ActionEQ(scope.Action),
		modeldevacceptance.IdempotencyKeyEQ(scope.IdempotencyKey),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrModelDevAcceptanceNotFound
	}
	if err != nil {
		return nil, err
	}
	if row.IntentHash != intentHash || !bytes.Equal(row.IntentCanonical, canonical) {
		return nil, ErrModelDevIdempotencyConflict
	}
	return modelDevAcceptanceFromRow(row)
}

// AcceptFrozen returns only after the acceptance transaction commits. The bool
// distinguishes a same-intent replay from this call's newly persisted record.
func (r *ModelDevAcceptanceRepo) AcceptFrozen(ctx context.Context, scope ModelDevAdmissionScope, candidate ModelDevFrozenCandidate) (*ModelDevAcceptance, bool, error) {
	if err := validateModelDevAdmissionScope(scope); err != nil {
		return nil, false, err
	}
	intentCanonical, intentHash, err := cpup01.CanonicalIntent(candidate.Intent)
	if err != nil {
		return nil, false, err
	}
	var accepted *ModelDevAcceptance
	var replayed bool
	err = r.acceptanceTransaction(ctx, func(tx *ent.Tx) error {
		// Serialize acceptance within this tenant before looking up the key. The
		// resource UUID must be the current persisted Governance tenant mapping.
		if _, err := tx.Tenant.Query().Where(tenant.IDEQ(scope.TenantID), tenant.ResourceTenantIDEQ(scope.ResourceTenantID)).ForUpdate().Only(ctx); err != nil {
			if ent.IsNotFound(err) {
				return fmt.Errorf("%w: modeldev tenant mapping", cpup01.ErrInvalidArgument)
			}
			return err
		}
		row, err := tx.ModelDevAcceptance.Query().Where(
			modeldevacceptance.TenantIDEQ(scope.TenantID),
			modeldevacceptance.ResourceTenantIDEQ(scope.ResourceTenantID),
			modeldevacceptance.ActorEQ(scope.Actor),
			modeldevacceptance.ActionEQ(scope.Action),
			modeldevacceptance.IdempotencyKeyEQ(scope.IdempotencyKey),
		).Only(ctx)
		if err == nil {
			if row.IntentHash != intentHash || !bytes.Equal(row.IntentCanonical, intentCanonical) {
				return ErrModelDevIdempotencyConflict
			}
			accepted, err = modelDevAcceptanceFromRow(row)
			replayed = true
			return err
		}
		if !ent.IsNotFound(err) {
			return err
		}
		// Only a new key consumes the resolved candidate. Historical replay is
		// independent of new defaults and does not revalidate a new snapshot.
		specHash, err := candidate.Snapshot.Digest()
		if err != nil {
			return err
		}
		envelope := cpup01.AdmissionEnvelope{
			TenantID: scope.ResourceTenantID, Actor: scope.Actor,
			OperationID: candidate.OperationID, ExecutionID: candidate.ExecutionID,
			Intent: candidate.Intent, IntentHash: intentHash,
			Snapshot: candidate.Snapshot, SpecHash: specHash, AcceptedAt: candidate.AcceptedAt,
		}
		_, snapshotCanonical, err := envelope.CanonicalPayloads()
		if err != nil {
			return err
		}
		if candidate.Snapshot.GPU != nil {
			if _, err := r.quota.OccupyModelDevGPUInTx(ctx, tx, scope, candidate, candidate.ResolvedGPU); err != nil {
				return err
			}
		} else if candidate.ResolvedGPU != nil {
			return fmt.Errorf("%w: CPU admission contains GPU accounting", cpup01.ErrInvalidArgument)
		}
		// The shared envelope rejects sub-microsecond timestamps instead of
		// silently changing the immutable accepted_at persisted by PostgreSQL.
		acceptedAt := candidate.AcceptedAt.UTC()
		// The shared validator accepts UUID case variants. Persist one spelling
		// so string uniqueness enforces the UUID identity received by ModelDev.
		row, err = tx.ModelDevAcceptance.Create().
			SetTenantID(scope.TenantID).
			SetResourceTenantID(scope.ResourceTenantID).
			SetActor(scope.Actor).
			SetAction(scope.Action).
			SetIdempotencyKey(scope.IdempotencyKey).
			SetOperationID(strings.ToLower(candidate.OperationID)).
			SetExecutionID(strings.ToLower(candidate.ExecutionID)).
			SetIntentHash(intentHash).
			SetIntentCanonical(intentCanonical).
			SetExecutionSpecHash(specHash).
			SetSnapshotCanonical(snapshotCanonical).
			SetAcceptedAt(acceptedAt).
			SetDispatchState(modeldevacceptance.DispatchStateQUEUED).
			Save(ctx)
		if err != nil {
			return err
		}
		accepted, err = modelDevAcceptanceFromRow(row)
		if err != nil {
			return err
		}
		// Check the database clock after all writes. A candidate that expired
		// while waiting for a tenant/unique-key lock rolls back as a whole.
		withinBudget, err := tx.Tenant.Query().Where(tenant.IDEQ(scope.TenantID), predicate.Tenant(func(q *entsql.Selector) {
			q.Where(entsql.P(func(b *entsql.Builder) { b.WriteString("clock_timestamp() < ").Arg(candidate.Snapshot.DeadlineAt) }))
		})).Exist(ctx)
		if err != nil {
			return err
		}
		if !withinBudget {
			return ErrModelDevAdmissionExpired
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return accepted, replayed, nil
}

func validateModelDevAdmissionScope(scope ModelDevAdmissionScope) error {
	if scope.TenantID == 0 || scope.ResourceTenantID == "" || !cpup01.ValidAuditActor(scope.Actor) || scope.Action != ModelDevCreateAction || scope.IdempotencyKey == "" {
		return fmt.Errorf("%w: modeldev admission scope", cpup01.ErrInvalidArgument)
	}
	return nil
}

func (r *ModelDevAcceptanceRepo) acceptanceTransaction(ctx context.Context, work func(*ent.Tx) error) (err error) {
	tx, err := r.entClient.Client().Tx(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				err = errors.Join(err, fmt.Errorf("rollback modeldev acceptance: %w", rollbackErr))
			}
		}
	}()
	if err = work(tx); err != nil {
		return err
	}
	err = tx.Commit()
	committed = err == nil
	return err
}

func modelDevAcceptanceFromRow(row *ent.ModelDevAcceptance) (*ModelDevAcceptance, error) {
	invalid := errors.New("invalid persisted modeldev acceptance")
	if row == nil || row.TenantID == nil || *row.TenantID == 0 || row.Action != ModelDevCreateAction || row.IdempotencyKey == "" {
		return nil, invalid
	}
	intent, err := cpup01.DecodeCanonicalIntent(row.IntentCanonical)
	var snapshot cpup01.Snapshot
	if err != nil || json.Unmarshal(row.SnapshotCanonical, &snapshot) != nil {
		return nil, invalid
	}
	envelope := cpup01.AdmissionEnvelope{
		TenantID: row.ResourceTenantID, Actor: row.Actor,
		OperationID: row.OperationID, ExecutionID: row.ExecutionID,
		Intent: intent, IntentHash: row.IntentHash,
		Snapshot: snapshot, SpecHash: row.ExecutionSpecHash, AcceptedAt: row.AcceptedAt,
	}
	intentCanonical, snapshotCanonical, err := envelope.CanonicalPayloads()
	if err != nil || !bytes.Equal(intentCanonical, row.IntentCanonical) || !bytes.Equal(snapshotCanonical, row.SnapshotCanonical) {
		return nil, invalid
	}
	accepted := &ModelDevAcceptance{
		Scope:       ModelDevAdmissionScope{TenantID: *row.TenantID, ResourceTenantID: row.ResourceTenantID, Actor: row.Actor, Action: row.Action, IdempotencyKey: row.IdempotencyKey},
		OperationID: row.OperationID, ExecutionID: row.ExecutionID,
		Intent: intent, Snapshot: snapshot,
		IntentHash: row.IntentHash, ExecutionSpecHash: row.ExecutionSpecHash,
		AcceptedAt: row.AcceptedAt, DispatchState: string(row.DispatchState),
	}
	if row.AttemptCount < 0 || row.LeaseGeneration < 0 {
		return nil, invalid
	}
	if row.DispatchState == modeldevacceptance.DispatchStateDISPATCHING {
		if row.LeaseOwner == nil || *row.LeaseOwner == "" || row.LeaseUntil == nil || row.LeaseGeneration == 0 {
			return nil, invalid
		}
	} else if row.LeaseOwner != nil || row.LeaseUntil != nil {
		return nil, invalid
	}
	if row.DispatchState != modeldevacceptance.DispatchStateUNKNOWN && (row.NextAttemptAt != nil || row.RetryBlocked) {
		return nil, invalid
	}
	switch row.DispatchState {
	case modeldevacceptance.DispatchStateQUEUED, modeldevacceptance.DispatchStateDISPATCHING, modeldevacceptance.DispatchStateUNKNOWN:
		if len(row.OwnerReceiptCanonical) != 0 {
			return nil, invalid
		}
	case modeldevacceptance.DispatchStateACKED:
		receipt, err := decodeModelDevOwnerReceipt(row.OwnerReceiptCanonical, envelope)
		if err != nil {
			return nil, err
		}
		accepted.OwnerReceipt = receipt
	default:
		return nil, invalid
	}
	return accepted, nil
}
