//go:build modeldev_pg

package data

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
)

func TestModelDevAdmissionExpiredCandidateRollsBackOriginalKeyAndIDs(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	candidate.AcceptedAt = time.Now().UTC().Truncate(time.Microsecond).Add(-2 * time.Hour)
	candidate = freezeModelDevFixture(t, scope, candidate)
	originalOperation, originalExecution := candidate.OperationID, candidate.ExecutionID
	accepted, replayed, err := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
	require.ErrorIs(t, err, ErrModelDevAdmissionExpired)
	require.Nil(t, accepted)
	require.False(t, replayed)
	count, err := client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	candidate.AcceptedAt = time.Now().UTC().Truncate(time.Microsecond)
	candidate = freezeModelDevFixture(t, scope, candidate)
	accepted, replayed, err = NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, originalOperation, accepted.OperationID)
	require.Equal(t, originalExecution, accepted.ExecutionID)
}

func TestModelDevAdmissionChecksDatabaseDeadlineAfterTenantLockWait(t *testing.T) {
	base, client, scope, candidate := newModelDevAcceptanceFixture(t)
	ctx, cancel := context.WithTimeout(base, 12*time.Second)
	defer cancel()
	seconds := uint64(1)
	candidate.Intent.TimeoutSeconds = &seconds
	candidate.AcceptedAt = time.Now().UTC().Truncate(time.Microsecond)
	candidate = freezeModelDevFixture(t, scope, candidate)
	tx, err := client.Client().Tx(ctx)
	require.NoError(t, err)
	closed := false
	defer func() {
		if !closed {
			_ = tx.Rollback()
		}
	}()
	_, err = tx.Tenant.Query().Where(tenant.IDEQ(scope.TenantID)).ForUpdate().Only(ctx)
	require.NoError(t, err)
	type outcome struct {
		accepted *ModelDevAcceptance
		replayed bool
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		a, r, e := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
		done <- outcome{a, r, e}
	}()
	// Observe the actual database lock wait; no scheduling sleep is used as proof.
	require.Eventually(t, func() bool {
		var waiting bool
		e := client.DB().QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND usename=current_user AND wait_event_type='Lock' AND query LIKE '%sys_tenants%')").Scan(&waiting)
		return e == nil && waiting
	}, 4*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		var expired bool
		e := client.DB().QueryRowContext(ctx, "SELECT clock_timestamp() >= $1", candidate.Snapshot.DeadlineAt).Scan(&expired)
		return e == nil && expired
	}, 4*time.Second, 10*time.Millisecond)
	select {
	case <-done:
		t.Fatal("acceptance escaped its held tenant lock")
	default:
	}
	require.NoError(t, tx.Rollback())
	closed = true
	var result outcome
	select {
	case result = <-done:
	case <-ctx.Done():
		t.Fatal("admission did not finish after lock release")
	}
	require.ErrorIs(t, result.err, ErrModelDevAdmissionExpired)
	require.Nil(t, result.accepted)
	require.False(t, result.replayed)
	count, err := client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}
