//go:build modeldev_pg

package data

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
)

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
