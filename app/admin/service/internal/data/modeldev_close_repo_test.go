//go:build modeldev_pg

package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
)

func TestModelDevStopRejectsRetiredCreationCodec(t *testing.T) {
	for _, queuedStop := range []bool{false, true} {
		name := "new Stop"
		if queuedStop {
			name = "queued close delivery"
		}
		t.Run(name, func(t *testing.T) {
			ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
			accepted := acceptModelDevDelivery(t, ctx, client, scope, candidate)
			observer := newModelDevDeliveryObserver(t, ctx)
			repo := NewModelDevAcceptanceRepo(client)
			if queuedStop {
				_, _, err := repo.AcceptStop(ctx, scope.TenantID, scope.ResourceTenantID, accepted.ExecutionID, "governance:user:8")
				require.NoError(t, err)
			}
			// Matching bytes and digests do not make a retired creation codec valid
			// for a new Stop or a previously queued close delivery.
			opaqueSnapshot := []byte(`{"retired_creation_codec":1}`)
			digest := sha256.Sum256(opaqueSnapshot)
			specHash := hex.EncodeToString(digest[:])
			_, err := observer.ExecContext(ctx, `UPDATE sys_modeldev_acceptances
		SET snapshot_canonical=$4, execution_spec_hash=$5
		WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3`,
				scope.TenantID, scope.ResourceTenantID, accepted.OperationID, opaqueSnapshot, specHash)
			require.NoError(t, err)
			if queuedStop {
				claim, err := repo.ClaimCloseDelivery(ctx, "reject-retired-close", time.Minute)
				require.NoError(t, err)
				require.Nil(t, claim, "MODELDEV_STOP_CURRENT_CODEC: a retired creation codec cannot become a close delivery")
				row, err := client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID), modeldevacceptance.ExecutionIDEQ(accepted.ExecutionID)).Only(ctx)
				require.NoError(t, err)
				require.Equal(t, modeldevacceptance.CloseDispatchStateUNKNOWN, row.CloseDispatchState)
				require.True(t, row.CloseRetryBlocked)
				require.NotNil(t, row.CloseLastErrorCode)
				require.Equal(t, "INVALID_COMMAND", *row.CloseLastErrorCode)
				require.Zero(t, row.CloseAttemptCount)
				return
			}
			create, err := repo.ClaimDelivery(ctx, "quarantine-retired-create", time.Minute)
			require.NoError(t, err)
			require.Nil(t, create, "an undecodable original must remain ineligible for creation")
			before := readModelDevDeliveryRow(t, ctx, observer, accepted)
			require.Equal(t, "UNKNOWN", before.State)
			require.True(t, before.Blocked)
			require.Equal(t, "INVALID_COMMAND", before.Failure.String)

			intent, replayed, err := repo.AcceptStop(ctx, scope.TenantID, scope.ResourceTenantID, accepted.ExecutionID, "governance:user:8")
			require.Error(t, err, "MODELDEV_STOP_CURRENT_CODEC: Stop must reject a retired creation codec even with matching byte digests")
			require.Nil(t, intent)
			require.False(t, replayed)
			require.Equal(t, before, readModelDevDeliveryRow(t, ctx, observer, accepted), "rejected Stop must preserve the creation bytes and quarantine")
			row, err := client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID), modeldevacceptance.ExecutionIDEQ(accepted.ExecutionID)).Only(ctx)
			require.NoError(t, err)
			require.Zero(t, row.StopIntentGeneration)
			require.Equal(t, modeldevacceptance.CloseDispatchStateIDLE, row.CloseDispatchState)
		})
	}
}

func TestModelDevStopRejectsCorruptAcceptedIdentity(t *testing.T) {
	for _, test := range []struct {
		name, assignment, value string
	}{
		{"snapshot digest", "execution_spec_hash=$4", strings.Repeat("0", 64)},
		{"intent digest", "intent_hash=$4", strings.Repeat("0", 64)},
		{"operation identity", "operation_id=$4", "not-an-operation-uuid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
			accepted := acceptModelDevDelivery(t, ctx, client, scope, candidate)
			observer := newModelDevDeliveryObserver(t, ctx)
			_, err := observer.ExecContext(ctx, `UPDATE sys_modeldev_acceptances SET `+test.assignment+`
				WHERE tenant_id=$1 AND resource_tenant_id=$2 AND execution_id=$3`,
				scope.TenantID, scope.ResourceTenantID, accepted.ExecutionID, test.value)
			require.NoError(t, err)
			intent, replayed, err := NewModelDevAcceptanceRepo(client).AcceptStop(ctx, scope.TenantID, scope.ResourceTenantID, accepted.ExecutionID, "governance:user:8")
			require.Error(t, err, "independent close must not accept a corrupt original identity or digest")
			require.Nil(t, intent)
			require.False(t, replayed)
			row, err := client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID), modeldevacceptance.ExecutionIDEQ(accepted.ExecutionID)).Only(ctx)
			require.NoError(t, err)
			require.Zero(t, row.StopIntentGeneration)
			require.Equal(t, modeldevacceptance.CloseDispatchStateIDLE, row.CloseDispatchState)
		})
	}
}

func TestModelDevCloseDeliveryRejectsChangedStopIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*ModelDevCloseDeliveryClaim)
	}{
		{"other tenant", func(c *ModelDevCloseDeliveryClaim) { c.Intent.TenantID++ }},
		{"other resource tenant", func(c *ModelDevCloseDeliveryClaim) { c.Intent.ResourceTenantID = uuid.NewString() }},
		{"other operation", func(c *ModelDevCloseDeliveryClaim) { c.Intent.OperationID = uuid.NewString() }},
		{"other execution", func(c *ModelDevCloseDeliveryClaim) { c.Intent.ExecutionID = uuid.NewString() }},
		{"other spec hash", func(c *ModelDevCloseDeliveryClaim) { c.Intent.ExecutionSpecHash = strings.Repeat("0", 64) }},
		{"other stop actor", func(c *ModelDevCloseDeliveryClaim) { c.Intent.RequestedActor = "governance:user:9" }},
		{"other stop time", func(c *ModelDevCloseDeliveryClaim) { c.Intent.RequestedAt = c.Intent.RequestedAt.Add(time.Second) }},
		{"other stop generation", func(c *ModelDevCloseDeliveryClaim) { c.Intent.Generation++ }},
		{"other lease owner", func(c *ModelDevCloseDeliveryClaim) { c.LeaseOwner = "foreign-close-worker" }},
		{"other lease generation", func(c *ModelDevCloseDeliveryClaim) { c.LeaseGeneration++ }},
		{"other attempt", func(c *ModelDevCloseDeliveryClaim) { c.AttemptCount++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
			accepted := acceptModelDevDelivery(t, ctx, client, scope, candidate)
			repo := NewModelDevAcceptanceRepo(client)
			intent, _, err := repo.AcceptStop(ctx, scope.TenantID, scope.ResourceTenantID, accepted.ExecutionID, "governance:user:8")
			require.NoError(t, err)
			claim, err := repo.ClaimCloseDelivery(ctx, "original-close-worker", time.Minute)
			require.NoError(t, err)
			require.NotNil(t, claim)
			changed := *claim
			changedIntent := *claim.Intent
			changed.Intent = &changedIntent
			test.change(&changed)
			written, err := repo.DeferCloseDelivery(ctx, &changed, ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"})
			require.NoError(t, err)
			require.False(t, written, "MODELDEV_CLOSE_COMPLETE_IDENTITY: a changed stop identity or lease cannot defer the original")
			receipt := ModelDevCloseReceipt{OperationID: intent.OperationID, ExecutionID: intent.ExecutionID, ExecutionSpecHash: intent.ExecutionSpecHash, CloseGeneration: 1, CloseState: "CLOSING"}
			written, err = repo.AckCloseDelivery(ctx, &changed, receipt)
			require.NoError(t, err)
			require.False(t, written, "a changed stop identity or lease cannot acknowledge the original")
			for _, bad := range []ModelDevCloseReceipt{
				{OperationID: uuid.NewString(), ExecutionID: receipt.ExecutionID, ExecutionSpecHash: receipt.ExecutionSpecHash, CloseGeneration: 1, CloseState: "CLOSING"},
				{OperationID: receipt.OperationID, ExecutionID: receipt.ExecutionID, ExecutionSpecHash: strings.Repeat("0", 64), CloseGeneration: 1, CloseState: "CLOSING"},
			} {
				written, err = repo.AckCloseDelivery(ctx, claim, bad)
				require.Error(t, err)
				require.False(t, written, "a mismatched owner receipt cannot acknowledge the original")
			}
			written, err = repo.AckCloseDelivery(ctx, claim, receipt)
			require.NoError(t, err)
			require.True(t, written, "rejected foreign claims must leave the original lease usable")
		})
	}
}

func TestModelDevCloseDeliveryForClaimsOnlyOriginalStop(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	first := acceptModelDevDelivery(t, ctx, client, scope, candidate)
	repo := NewModelDevAcceptanceRepo(client)
	_, _, err := repo.AcceptStop(ctx, scope.TenantID, scope.ResourceTenantID, first.ExecutionID, "governance:user:8")
	require.NoError(t, err)
	scope.IdempotencyKey = uuid.NewString()
	candidate.OperationID, candidate.ExecutionID = uuid.NewString(), uuid.NewString()
	target := acceptModelDevDelivery(t, ctx, client, scope, candidate)
	intent, _, err := repo.AcceptStop(ctx, scope.TenantID, scope.ResourceTenantID, target.ExecutionID, "governance:user:8")
	require.NoError(t, err)
	foreign := *intent
	foreign.ExecutionSpecHash = strings.Repeat("0", 64)
	claim, err := repo.ClaimCloseDeliveryFor(ctx, foreign, "wrong-stop-identity", time.Minute)
	require.NoError(t, err)
	require.Nil(t, claim)
	claim, err = repo.ClaimCloseDeliveryFor(ctx, *intent, "only-target-close", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, intent, claim.Intent)
	firstRow, err := client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID), modeldevacceptance.ExecutionIDEQ(first.ExecutionID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, modeldevacceptance.CloseDispatchStateQUEUED, firstRow.CloseDispatchState)
	require.Zero(t, firstRow.CloseAttemptCount, "single-execution maintenance must not claim an earlier unrelated Stop")
	written, err := repo.AckCloseDelivery(ctx, claim, ModelDevCloseReceipt{OperationID: intent.OperationID, ExecutionID: intent.ExecutionID, ExecutionSpecHash: intent.ExecutionSpecHash, CloseGeneration: 1, CloseState: "CLOSING"})
	require.NoError(t, err)
	require.True(t, written)
	claim, err = repo.ClaimCloseDeliveryFor(ctx, *intent, "target-already-acked", time.Minute)
	require.NoError(t, err)
	require.Nil(t, claim, "an acknowledged Stop cannot be resent by maintenance")
	firstClaim, err := repo.ClaimCloseDelivery(ctx, "original-global-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, firstClaim)
	require.Equal(t, first.ExecutionID, firstClaim.Intent.ExecutionID, "the unrelated original queue entry remains available")
}

func TestModelDevCloseDeliveryRecoversOriginalAndFencesExpiredWorker(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	accepted := acceptModelDevDelivery(t, ctx, client, scope, candidate)
	repo := NewModelDevAcceptanceRepo(client)
	intent, replayed, err := repo.AcceptStop(ctx, scope.TenantID, scope.ResourceTenantID, accepted.ExecutionID, "governance:user:8")
	require.NoError(t, err)
	require.False(t, replayed)
	claim, err := repo.ClaimCloseDelivery(ctx, "close-original", time.Minute)
	require.NoError(t, err, "CPU10_CLOSE_DELIVERY_NOT_IMPLEMENTED: accepted stop must survive into the reliable command queue")
	require.NotNil(t, claim)
	require.Equal(t, intent, claim.Intent)
	create, err := repo.ClaimDelivery(ctx, "create-after-stop", time.Minute)
	require.NoError(t, err)
	require.Nil(t, create, "a queued create must wait for the stop tombstone acknowledgment")
	second, err := NewModelDevAcceptanceRepo(newModelDevPGClient(t)).ClaimCloseDelivery(ctx, "close-contender", time.Minute)
	require.NoError(t, err)
	require.Nil(t, second, "one live lease owns the stop command")
	observer := newModelDevDeliveryObserver(t, ctx)
	// Advance only this fixture's lease clock to exercise takeover without a
	// sleep. This is an explicit lease-expiry fixture, not a real elapsed wait.
	_, err = observer.ExecContext(ctx, `UPDATE sys_modeldev_acceptances SET close_lease_until=statement_timestamp()-interval '1 second' WHERE tenant_id=$1 AND resource_tenant_id=$2 AND execution_id=$3`, scope.TenantID, scope.ResourceTenantID, accepted.ExecutionID)
	require.NoError(t, err)
	restarted := NewModelDevAcceptanceRepo(newModelDevPGClient(t))
	current, err := restarted.ClaimCloseDelivery(ctx, "close-restarted", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, intent, current.Intent, "restart must preserve original stop actor/time and source generation")
	require.Equal(t, int64(2), current.LeaseGeneration)
	receipt := ModelDevCloseReceipt{OperationID: intent.OperationID, ExecutionID: intent.ExecutionID, ExecutionSpecHash: intent.ExecutionSpecHash, CloseGeneration: 9, CloseState: "CLOSING"}
	written, err := restarted.AckCloseDelivery(ctx, claim, receipt)
	require.NoError(t, err)
	require.False(t, written, "late owner cannot acknowledge after takeover")
	foreign := *current
	foreignIntent := *current.Intent
	foreignIntent.ResourceTenantID = uuid.NewString()
	foreign.Intent = &foreignIntent
	written, err = restarted.AckCloseDelivery(ctx, &foreign, receipt)
	require.NoError(t, err)
	require.False(t, written, "a foreign tenant cannot acknowledge the original")
	bad := receipt
	bad.ExecutionID = uuid.NewString()
	written, err = restarted.AckCloseDelivery(ctx, current, bad)
	require.Error(t, err)
	require.False(t, written)
	written, err = restarted.AckCloseDelivery(ctx, current, receipt)
	require.NoError(t, err)
	require.True(t, written)
	row, err := newModelDevPGClient(t).Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID), modeldevacceptance.ExecutionIDEQ(intent.ExecutionID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, modeldevacceptance.CloseDispatchStateACKED, row.CloseDispatchState)
	require.Equal(t, modeldevacceptance.DispatchStateQUEUED, row.DispatchState, "stop can be delivered before create without forging its ACK")
	var saved ModelDevCloseReceipt
	require.NoError(t, json.Unmarshal(row.CloseReceiptCanonical, &saved))
	require.Equal(t, receipt, saved)
	create, err = restarted.ClaimDelivery(ctx, "create-after-tombstone", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, create, "the original admission still reaches its owner after the durable tombstone")
	require.Equal(t, accepted.ExecutionID, create.Acceptance.ExecutionID)
	_, replayed, err = restarted.AcceptStop(ctx, scope.TenantID, scope.ResourceTenantID, accepted.ExecutionID, "governance:user:9")
	require.NoError(t, err)
	require.True(t, replayed)
	next, err := restarted.ClaimCloseDelivery(ctx, "after-ack", time.Minute)
	require.NoError(t, err)
	require.Nil(t, next)
}

func TestModelDevCloseDeliveryRetainsUnknownAndRetriesAfterRestart(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	accepted := acceptModelDevDelivery(t, ctx, client, scope, candidate)
	repo := NewModelDevAcceptanceRepo(client)
	intent, _, err := repo.AcceptStop(ctx, scope.TenantID, scope.ResourceTenantID, accepted.ExecutionID, "governance:user:8")
	require.NoError(t, err)
	claim, err := repo.ClaimCloseDelivery(ctx, "close-timeout", time.Minute)
	require.NoError(t, err, "CPU10_CLOSE_DELIVERY_NOT_IMPLEMENTED")
	require.NotNil(t, claim)
	written, err := repo.DeferCloseDelivery(ctx, claim, ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"})
	require.NoError(t, err)
	require.True(t, written)
	restarted := NewModelDevAcceptanceRepo(newModelDevPGClient(t))
	var retry *ModelDevCloseDeliveryClaim
	retryContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var e error
		retry, e = restarted.ClaimCloseDelivery(retryContext, "close-retry", time.Minute)
		require.NoError(t, e)
		if retry != nil {
			break
		}
		select {
		case <-retryContext.Done():
			t.Fatal("persisted close retry did not become due")
		case <-ticker.C:
		}
	}
	require.Equal(t, intent, retry.Intent)
	require.Equal(t, int64(2), retry.AttemptCount)
	written, err = restarted.DeferCloseDelivery(ctx, retry, ModelDevDeliveryFailure{Code: "COMMAND_CONFLICT", Permanent: true})
	require.NoError(t, err)
	require.True(t, written)
	next, err := restarted.ClaimCloseDelivery(ctx, "after-conflict", time.Minute)
	require.NoError(t, err)
	require.Nil(t, next, "definitive command conflict remains durable and blocked")
}
