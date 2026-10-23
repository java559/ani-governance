//go:build modeldev_pg

package data

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	entgo "entgo.io/ent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"go-wind-admin/app/admin/service/internal/data/ent"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

func TestModelDevDeliveryClaimsOnceAcrossConcurrentWorkers(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	original := acceptModelDevDelivery(t, ctx, client, scope, candidate)
	clients := []*entCrud.EntClient[*ent.Client]{newModelDevPGClient(t), newModelDevPGClient(t)}
	type outcome struct {
		claim *ModelDevDeliveryClaim
		err   error
	}
	start, results := make(chan struct{}), make(chan outcome, len(clients))
	var workers sync.WaitGroup
	for _, c := range clients {
		workers.Add(1)
		go func(c *entCrud.EntClient[*ent.Client]) {
			defer workers.Done()
			<-start
			claim, err := NewModelDevAcceptanceRepo(c).ClaimDelivery(ctx, uuid.NewString(), time.Minute)
			results <- outcome{claim, err}
		}(c)
	}
	close(start)
	workers.Wait()
	close(results)
	var winner *ModelDevDeliveryClaim
	for result := range results {
		require.NoError(t, result.err, "ClaimDelivery must implement durable acquisition before lease-column assertions")
		if result.claim != nil {
			require.Nil(t, winner, "one queued command cannot have two live claims")
			winner = result.claim
		}
	}
	require.NotNil(t, winner)
	require.Equal(t, int64(1), winner.LeaseGeneration)
	require.Equal(t, int64(1), winner.AttemptCount)
	require.NotEmpty(t, winner.LeaseOwner)
	requireModelDevDeliveryOriginal(t, original, winner.Acceptance)
	observer := newModelDevDeliveryObserver(t, ctx)
	row := readModelDevDeliveryRow(t, ctx, observer, original)
	require.Equal(t, "DISPATCHING", row.State)
	require.Equal(t, winner.LeaseOwner, row.LeaseOwner.String)
	require.Equal(t, int64(1), row.Generation)
	require.Equal(t, int64(1), row.Attempts)
	require.True(t, row.LeaseUntil.Valid)
	var leaseLive bool
	require.NoError(t, observer.QueryRowContext(ctx, `SELECT lease_until > statement_timestamp()
		FROM sys_modeldev_acceptances WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3`,
		scope.TenantID, scope.ResourceTenantID, original.OperationID).Scan(&leaseLive))
	require.True(t, leaseLive, "lease expiry is measured against PostgreSQL's statement clock")
	actual, err := NewModelDevAcceptanceRepo(newModelDevPGClient(t)).FindAccepted(ctx, scope, candidate.Intent)
	require.NoError(t, err)
	requireModelDevDeliveryOriginal(t, original, actual)
}

func TestModelDevDeliveryTakeoverFencesLateAndForeignClaims(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	_, _, otherScope, _ := newModelDevAcceptanceFixture(t)
	original := acceptModelDevDelivery(t, ctx, client, scope, candidate)
	repo := NewModelDevAcceptanceRepo(client)
	old := claimModelDevDelivery(t, ctx, repo, "worker-old")
	observer := newModelDevDeliveryObserver(t, ctx)
	// This fixture explicitly advances its own row's due time. It does not
	// claim that a process waited for a real lease duration to elapse.
	execModelDevDeliveryRow(t, ctx, observer, original, `UPDATE sys_modeldev_acceptances
		SET lease_until=statement_timestamp()-interval '1 second'
		WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3`)
	currentRepo := NewModelDevAcceptanceRepo(newModelDevPGClient(t))
	current := claimModelDevDelivery(t, ctx, currentRepo, "worker-current")
	require.Equal(t, original.OperationID, current.Acceptance.OperationID)
	require.Equal(t, int64(2), current.LeaseGeneration)
	require.Equal(t, int64(2), current.AttemptCount)
	baseline := readModelDevDeliveryRow(t, ctx, observer, original)
	for _, test := range []struct {
		name  string
		claim func() *ModelDevDeliveryClaim
	}{
		{"expired owner", func() *ModelDevDeliveryClaim { return old }},
		{"other local tenant", func() *ModelDevDeliveryClaim {
			c := copyModelDevDeliveryClaim(current)
			c.Acceptance.Scope.TenantID = otherScope.TenantID
			return c
		}},
		{"other resource tenant", func() *ModelDevDeliveryClaim {
			c := copyModelDevDeliveryClaim(current)
			c.Acceptance.Scope.ResourceTenantID = otherScope.ResourceTenantID
			return c
		}},
		{"other operation", func() *ModelDevDeliveryClaim {
			c := copyModelDevDeliveryClaim(current)
			c.Acceptance.OperationID = uuid.NewString()
			return c
		}},
		{"other lease owner", func() *ModelDevDeliveryClaim {
			c := copyModelDevDeliveryClaim(current)
			c.LeaseOwner = "worker-foreign"
			return c
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			claim := test.claim()
			ack, err := currentRepo.AckDelivery(ctx, claim, modelDevDeliveryReceipt(claim.Acceptance, 1))
			require.NoError(t, err)
			require.False(t, ack)
			deferred, err := currentRepo.DeferDelivery(ctx, claim, ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"})
			require.NoError(t, err)
			require.False(t, deferred)
			require.Equal(t, baseline, readModelDevDeliveryRow(t, ctx, observer, original))
		})
	}
	ack, err := currentRepo.AckDelivery(ctx, current, modelDevDeliveryReceipt(original, 1))
	require.NoError(t, err)
	require.True(t, ack)
}

func TestModelDevDeliveryPersistsFullOwnerReceiptAndRejectsInvalidACK(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	original := acceptModelDevDelivery(t, ctx, client, scope, candidate)
	repo := NewModelDevAcceptanceRepo(client)
	claim := claimModelDevDelivery(t, ctx, repo, "receipt-worker")
	observer := newModelDevDeliveryObserver(t, ctx)
	baseline := readModelDevDeliveryRow(t, ctx, observer, original)
	for _, test := range []struct {
		name   string
		change func(*ModelDevOwnerReceipt)
	}{
		{"zero revision", func(r *ModelDevOwnerReceipt) { r.Revision = 0 }},
		{"different operation", func(r *ModelDevOwnerReceipt) { r.OperationID = uuid.NewString() }},
		{"different execution", func(r *ModelDevOwnerReceipt) { r.ExecutionID = uuid.NewString() }},
		{"different specification", func(r *ModelDevOwnerReceipt) { r.ExecutionSpecHash = strings.Repeat("f", 64) }},
		{"unknown compute", func(r *ModelDevOwnerReceipt) { r.ComputeState = "FUTURE" }},
		{"unknown delivery", func(r *ModelDevOwnerReceipt) { r.DeliveryState = "FUTURE" }},
		{"unknown resource", func(r *ModelDevOwnerReceipt) { r.ResourceState = "FUTURE" }},
		{"unknown close", func(r *ModelDevOwnerReceipt) { r.CloseState = "FUTURE" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			receipt := modelDevDeliveryReceipt(original, 1)
			test.change(&receipt)
			ack, err := repo.AckDelivery(ctx, claim, receipt)
			require.ErrorIs(t, err, ErrModelDevInvalidReceipt)
			require.False(t, ack)
			require.Equal(t, baseline, readModelDevDeliveryRow(t, ctx, observer, original))
		})
	}
	want := modelDevDeliveryReceipt(original, ^uint64(0))
	want.Replayed = true
	ack, err := repo.AckDelivery(ctx, claim, want)
	require.NoError(t, err)
	require.True(t, ack)
	reader := NewModelDevAcceptanceRepo(newModelDevPGClient(t))
	actual, err := reader.FindAccepted(ctx, scope, candidate.Intent)
	require.NoError(t, err)
	requireModelDevDeliveryOriginal(t, original, actual)
	require.Equal(t, "ACKED", actual.DispatchState)
	require.Equal(t, &want, actual.OwnerReceipt, "full uint64 revision and all four states survive reconnection together")
	row := readModelDevDeliveryRow(t, ctx, observer, original)
	require.True(t, bytes.Contains(row.Receipt, []byte(`"revision":"18446744073709551615"`)))
	require.False(t, row.LeaseOwner.Valid)
	require.False(t, row.LeaseUntil.Valid)
	require.False(t, row.NextAttempt.Valid)
	require.False(t, row.Blocked)
	late := modelDevDeliveryReceipt(original, 1)
	ack, err = reader.AckDelivery(ctx, claim, late)
	require.NoError(t, err)
	require.False(t, ack)
	require.Equal(t, row, readModelDevDeliveryRow(t, ctx, observer, original), "late ACK cannot downgrade the owner observation")
	missing, err := reader.ClaimDelivery(ctx, "after-ack", time.Minute)
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestModelDevDeliveryUnknownBackoffAndPermanentFailurePreserveOriginal(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	original := acceptModelDevDelivery(t, ctx, client, scope, candidate)
	repo := NewModelDevAcceptanceRepo(client)
	first := claimModelDevDelivery(t, ctx, repo, "retry-worker")
	observer := newModelDevDeliveryObserver(t, ctx)
	deferred, err := repo.DeferDelivery(ctx, first, ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"})
	require.NoError(t, err)
	require.True(t, deferred)
	row := readModelDevDeliveryRow(t, ctx, observer, original)
	require.Equal(t, "UNKNOWN", row.State)
	require.Equal(t, "OWNER_UNAVAILABLE", row.Failure.String)
	require.False(t, row.Blocked)
	require.False(t, row.LeaseOwner.Valid)
	require.False(t, row.LeaseUntil.Valid)
	require.True(t, row.NextAttempt.Valid)
	require.Empty(t, row.Receipt)
	var backoff bool
	require.NoError(t, observer.QueryRowContext(ctx, `SELECT next_attempt_at > statement_timestamp()
		FROM sys_modeldev_acceptances WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3`,
		scope.TenantID, scope.ResourceTenantID, original.OperationID).Scan(&backoff))
	require.True(t, backoff)
	missing, err := repo.ClaimDelivery(ctx, "too-early", time.Minute)
	require.NoError(t, err)
	require.Nil(t, missing)
	// Advance only this synthetic fixture's retry due time using the DB clock.
	execModelDevDeliveryRow(t, ctx, observer, original, `UPDATE sys_modeldev_acceptances
		SET next_attempt_at=statement_timestamp()-interval '1 second'
		WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3`)
	second := claimModelDevDelivery(t, ctx, repo, "retry-after-restart")
	require.Equal(t, int64(2), second.LeaseGeneration)
	require.Equal(t, int64(2), second.AttemptCount)
	deferred, err = repo.DeferDelivery(ctx, second, ModelDevDeliveryFailure{Code: "COMMAND_CONFLICT", Permanent: true})
	require.NoError(t, err)
	require.True(t, deferred)
	row = readModelDevDeliveryRow(t, ctx, observer, original)
	require.Equal(t, "UNKNOWN", row.State)
	require.True(t, row.Blocked)
	require.Equal(t, "COMMAND_CONFLICT", row.Failure.String)
	require.Empty(t, row.Receipt)
	execModelDevDeliveryRow(t, ctx, observer, original, `UPDATE sys_modeldev_acceptances
		SET next_attempt_at=statement_timestamp()-interval '1 second'
		WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3`)
	reader := NewModelDevAcceptanceRepo(newModelDevPGClient(t))
	missing, err = reader.ClaimDelivery(ctx, "blocked-restart", time.Minute)
	require.NoError(t, err)
	require.Nil(t, missing, "a permanent failure is not made retryable merely by time passing")
	actual, err := reader.FindAccepted(ctx, scope, candidate.Intent)
	require.NoError(t, err)
	requireModelDevDeliveryOriginal(t, original, actual)
	require.Nil(t, actual.OwnerReceipt)
}

func TestModelDevDeliveryQuarantinesCorruptOriginalWithoutStarvingNext(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	corrupt := acceptModelDevDelivery(t, ctx, client, scope, candidate)
	repo := NewModelDevAcceptanceRepo(client)
	first := claimModelDevDelivery(t, ctx, repo, "corrupt-preflight")
	deferred, err := repo.DeferDelivery(ctx, first, ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"})
	require.NoError(t, err)
	require.True(t, deferred)
	healthyScope, healthyCandidate := scope, candidate
	healthyScope.IdempotencyKey = uuid.NewString()
	healthyCandidate.OperationID, healthyCandidate.ExecutionID = uuid.NewString(), uuid.NewString()
	healthy := acceptModelDevDelivery(t, ctx, client, healthyScope, healthyCandidate)
	observer := newModelDevDeliveryObserver(t, ctx)
	// Deliberate corruption of this test's persisted bytes, not a new schema or
	// a repaired command. The earlier, due row must not prevent the next claim.
	result, err := observer.ExecContext(ctx, `UPDATE sys_modeldev_acceptances
		SET intent_canonical=$4, next_attempt_at=statement_timestamp()-interval '1 second'
		WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3`,
		scope.TenantID, scope.ResourceTenantID, corrupt.OperationID, []byte("corrupt original"))
	require.NoError(t, err)
	changed, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	before := readModelDevDeliveryRow(t, ctx, observer, corrupt)
	next := claimModelDevDelivery(t, ctx, NewModelDevAcceptanceRepo(newModelDevPGClient(t)), "healthy-next")
	require.Equal(t, healthy.OperationID, next.Acceptance.OperationID)
	requireModelDevDeliveryOriginal(t, healthy, next.Acceptance)
	after := readModelDevDeliveryRow(t, ctx, observer, corrupt)
	require.Equal(t, "UNKNOWN", after.State)
	require.True(t, after.Blocked)
	require.Equal(t, "INVALID_COMMAND", after.Failure.String)
	require.Equal(t, before.Intent, after.Intent, "quarantine must preserve corrupt evidence rather than invent a replacement intent")
	require.Equal(t, before.Snapshot, after.Snapshot)
	require.Equal(t, before.IntentHash, after.IntentHash)
	require.Equal(t, before.SpecHash, after.SpecHash)
	require.Empty(t, after.Receipt)
}

func TestModelDevDeliveryCommitBoundaryFailuresReturnNoClaimOrACK(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	preflight := acceptModelDevDelivery(t, ctx, client, scope, candidate)
	repo := NewModelDevAcceptanceRepo(client)
	preflightClaim := claimModelDevDelivery(t, ctx, repo, "commit-preflight")
	ack, err := repo.AckDelivery(ctx, preflightClaim, modelDevDeliveryReceipt(preflight, 1))
	require.NoError(t, err)
	require.True(t, ack, "normal Claim and ACK must work before injecting a commit-boundary failure")
	// Use a new original and separate faulting/observing connections. The fault
	// is an Ent CommitHook substitute; mutations, locks, rollback and the
	// independent readback use actual PG. This is not a server COMMIT fault.
	nextScope, nextCandidate := scope, candidate
	nextScope.IdempotencyKey = uuid.NewString()
	nextCandidate.OperationID, nextCandidate.ExecutionID = uuid.NewString(), uuid.NewString()
	original := acceptModelDevDelivery(t, ctx, client, nextScope, nextCandidate)
	observer := newModelDevDeliveryObserver(t, ctx)
	beforeClaim := readModelDevDeliveryRow(t, ctx, observer, original)
	failure := errors.New("injected modeldev commit boundary failure")
	faultClient := newModelDevPGClient(t)
	claimCommits := rejectModelDevDeliveryCommit(t, faultClient, failure)
	claim, err := NewModelDevAcceptanceRepo(faultClient).ClaimDelivery(ctx, "commit-rejected", time.Minute)
	require.ErrorIs(t, err, failure)
	require.Nil(t, claim)
	require.Equal(t, int64(1), claimCommits.Load(), "failure must reach the transaction commit boundary")
	require.Equal(t, beforeClaim, readModelDevDeliveryRow(t, ctx, observer, original), "failed acquisition rolls back lease and attempt increments")
	claim = claimModelDevDelivery(t, ctx, repo, "commit-recovered")
	require.Equal(t, int64(1), claim.LeaseGeneration)
	require.Equal(t, int64(1), claim.AttemptCount)
	requireModelDevDeliveryOriginal(t, original, claim.Acceptance)
	beforeACK := readModelDevDeliveryRow(t, ctx, observer, original)
	faultACKClient := newModelDevPGClient(t)
	ackCommits := rejectModelDevDeliveryCommit(t, faultACKClient, failure)
	receipt := modelDevDeliveryReceipt(original, 1)
	ack, err = NewModelDevAcceptanceRepo(faultACKClient).AckDelivery(ctx, claim, receipt)
	require.ErrorIs(t, err, failure)
	require.False(t, ack)
	require.Equal(t, int64(1), ackCommits.Load())
	require.Equal(t, beforeACK, readModelDevDeliveryRow(t, ctx, observer, original), "failed ACK preserves the live claim and leaves no receipt")
	ack, err = repo.AckDelivery(ctx, claim, receipt)
	require.NoError(t, err)
	require.True(t, ack)
	actual, err := NewModelDevAcceptanceRepo(newModelDevPGClient(t)).FindAccepted(ctx, nextScope, nextCandidate.Intent)
	require.NoError(t, err)
	requireModelDevDeliveryOriginal(t, original, actual)
	require.Equal(t, &receipt, actual.OwnerReceipt)
	ack, err = repo.AckDelivery(ctx, claim, receipt)
	require.NoError(t, err)
	require.False(t, ack, "the recovered claim can commit its ACK only once")
}

func acceptModelDevDelivery(t *testing.T, ctx context.Context, client *entCrud.EntClient[*ent.Client], scope ModelDevAdmissionScope, candidate ModelDevFrozenCandidate) *ModelDevAcceptance {
	t.Helper()
	candidate = freezeModelDevFixture(t, scope, candidate)
	original, replayed, err := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)
	require.NotNil(t, original)
	require.Equal(t, "QUEUED", original.DispatchState)
	_, intentHash, err := cpup01.CanonicalIntent(candidate.Intent)
	require.NoError(t, err)
	specHash, err := candidate.Snapshot.Digest()
	require.NoError(t, err)
	expected := &ModelDevAcceptance{
		Scope: scope, OperationID: candidate.OperationID, ExecutionID: candidate.ExecutionID,
		Intent: candidate.Intent, Snapshot: candidate.Snapshot, IntentHash: intentHash,
		ExecutionSpecHash: specHash, AcceptedAt: candidate.AcceptedAt,
	}
	reader := NewModelDevAcceptanceRepo(newModelDevPGClient(t))
	persisted, err := reader.FindAccepted(ctx, scope, candidate.Intent)
	require.NoError(t, err)
	requireModelDevDeliveryOriginal(t, expected, persisted)
	require.Equal(t, "QUEUED", persisted.DispatchState)
	require.Nil(t, persisted.OwnerReceipt)
	t.Log("MODELDEV_DELIVERY_STORAGE_PREFLIGHT PASS")
	return original
}

func claimModelDevDelivery(t *testing.T, ctx context.Context, repo *ModelDevAcceptanceRepo, worker string) *ModelDevDeliveryClaim {
	t.Helper()
	claim, err := repo.ClaimDelivery(ctx, worker, time.Minute)
	require.NoError(t, err, "ClaimDelivery must implement durable acquisition before lease-column assertions")
	require.NotNil(t, claim)
	require.NotNil(t, claim.Acceptance)
	require.Equal(t, worker, claim.LeaseOwner)
	return claim
}

func copyModelDevDeliveryClaim(original *ModelDevDeliveryClaim) *ModelDevDeliveryClaim {
	claim, acceptance := *original, *original.Acceptance
	claim.Acceptance = &acceptance
	return &claim
}

func modelDevDeliveryReceipt(original *ModelDevAcceptance, revision uint64) ModelDevOwnerReceipt {
	return ModelDevOwnerReceipt{
		OperationID: original.OperationID, ExecutionID: original.ExecutionID, ExecutionSpecHash: original.ExecutionSpecHash,
		ComputeState: "ACCEPTED", DeliveryState: "PENDING", ResourceState: "NOT_APPLICABLE", CloseState: "OPEN", Revision: revision,
	}
}

func requireModelDevDeliveryOriginal(t *testing.T, expected, actual *ModelDevAcceptance) {
	t.Helper()
	require.NotNil(t, actual)
	require.Equal(t, expected.Scope, actual.Scope)
	require.Equal(t, expected.OperationID, actual.OperationID)
	require.Equal(t, expected.ExecutionID, actual.ExecutionID)
	require.Equal(t, expected.IntentHash, actual.IntentHash)
	require.Equal(t, expected.ExecutionSpecHash, actual.ExecutionSpecHash)
	require.True(t, expected.AcceptedAt.Equal(actual.AcceptedAt))
	wantIntent, _, err := cpup01.CanonicalIntent(expected.Intent)
	require.NoError(t, err)
	gotIntent, _, err := cpup01.CanonicalIntent(actual.Intent)
	require.NoError(t, err)
	require.Equal(t, wantIntent, gotIntent)
	wantSnapshot, err := expected.Snapshot.Canonical()
	require.NoError(t, err)
	gotSnapshot, err := actual.Snapshot.Canonical()
	require.NoError(t, err)
	require.Equal(t, wantSnapshot, gotSnapshot)
}

type modelDevDeliveryRow struct {
	State       string
	LeaseOwner  sql.NullString
	LeaseUntil  sql.NullTime
	Generation  int64
	Attempts    int64
	NextAttempt sql.NullTime
	Blocked     bool
	Failure     sql.NullString
	Receipt     []byte
	Intent      []byte
	Snapshot    []byte
	IntentHash  string
	SpecHash    string
}

func newModelDevDeliveryObserver(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", os.Getenv("ANI_TEST_DATABASE_DSN"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	db.SetMaxOpenConns(1)
	require.NoError(t, db.PingContext(ctx))
	return db
}

// Call only after a successful public ClaimDelivery: the first RED must be
// missing product behavior, never a query against not-yet-migrated columns.
func readModelDevDeliveryRow(t *testing.T, ctx context.Context, db *sql.DB, original *ModelDevAcceptance) modelDevDeliveryRow {
	t.Helper()
	var row modelDevDeliveryRow
	require.NoError(t, db.QueryRowContext(ctx, `SELECT dispatch_state, lease_owner, lease_until,
		lease_generation, attempt_count, next_attempt_at, retry_blocked, last_error_code,
		owner_receipt_canonical, intent_canonical, snapshot_canonical, intent_hash, execution_spec_hash
		FROM sys_modeldev_acceptances WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3`,
		original.Scope.TenantID, original.Scope.ResourceTenantID, original.OperationID).Scan(
		&row.State, &row.LeaseOwner, &row.LeaseUntil, &row.Generation, &row.Attempts, &row.NextAttempt,
		&row.Blocked, &row.Failure, &row.Receipt, &row.Intent, &row.Snapshot, &row.IntentHash, &row.SpecHash))
	return row
}

func execModelDevDeliveryRow(t *testing.T, ctx context.Context, db *sql.DB, original *ModelDevAcceptance, statement string) {
	t.Helper()
	result, err := db.ExecContext(ctx, statement, original.Scope.TenantID, original.Scope.ResourceTenantID, original.OperationID)
	require.NoError(t, err)
	changed, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), changed, "fixture DML must affect exactly its own accepted command")
}

func rejectModelDevDeliveryCommit(t *testing.T, client *entCrud.EntClient[*ent.Client], failure error) *atomic.Int64 {
	t.Helper()
	calls := &atomic.Int64{}
	client.Client().ModelDevAcceptance.Use(func(next entgo.Mutator) entgo.Mutator {
		return entgo.MutateFunc(func(ctx context.Context, mutation entgo.Mutation) (entgo.Value, error) {
			m, ok := mutation.(*ent.ModelDevAcceptanceMutation)
			if !ok {
				return nil, errors.New("unexpected delivery mutation type")
			}
			tx, err := m.Tx()
			if err != nil {
				return nil, err
			}
			tx.OnCommit(func(ent.Committer) ent.Committer {
				return ent.CommitFunc(func(context.Context, *ent.Tx) error {
					calls.Add(1)
					return failure
				})
			})
			return next.Mutate(ctx, mutation)
		})
	})
	return calls
}
