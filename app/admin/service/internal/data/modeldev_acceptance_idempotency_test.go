//go:build modeldev_pg

package data

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
)

func TestModelDevAcceptanceConflictingIntentPreservesOriginal(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	repo := NewModelDevAcceptanceRepo(client)
	original, replayed, err := repo.AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)

	changed := candidate
	changed.OperationID, changed.ExecutionID = uuid.NewString(), uuid.NewString()
	rate := "0.02"
	p := *changed.Intent.MLP
	p.LearningRate = &rate
	changed.Intent.MLP = &p
	changed.Snapshot = conformance.SnapshotV1()
	for i := range changed.Snapshot.Program.ResolvedParameters {
		if changed.Snapshot.Program.ResolvedParameters[i].Name == "learning_rate" {
			changed.Snapshot.Program.ResolvedParameters[i].Value = "0.02"
		}
	}
	changed = freezeModelDevFixture(t, scope, changed)
	// The second request is a valid, coherent candidate in its own right. Its
	// rejection must be a key conflict, not a malformed parameter or snapshot.
	_, intentHash, err := cpup01.CanonicalIntent(changed.Intent)
	require.NoError(t, err)
	specHash, err := changed.Snapshot.Digest()
	require.NoError(t, err)
	envelope := cpup01.AdmissionEnvelope{
		TenantID: scope.ResourceTenantID, Actor: scope.Actor,
		OperationID: changed.OperationID, ExecutionID: changed.ExecutionID,
		Intent: changed.Intent, IntentHash: intentHash,
		Snapshot: changed.Snapshot, SpecHash: specHash, AcceptedAt: changed.AcceptedAt,
	}
	_, _, err = envelope.CanonicalPayloads()
	require.NoError(t, err)
	rejected, replayed, err := repo.AcceptFrozen(ctx, scope, changed)
	require.ErrorIs(t, err, ErrModelDevIdempotencyConflict)
	require.Nil(t, rejected)
	require.False(t, replayed)

	reader := newModelDevPGClient(t)
	actual, replayed, err := NewModelDevAcceptanceRepo(reader).AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, original.OperationID, actual.OperationID)
	require.Equal(t, original.ExecutionID, actual.ExecutionID)
	require.Equal(t, original.IntentHash, actual.IntentHash)
	require.Equal(t, original.ExecutionSpecHash, actual.ExecutionSpecHash)
	canonical, err := actual.Snapshot.Canonical()
	require.NoError(t, err)
	require.Equal(t, fixtureSnapshotBytes(t, candidate), canonical)
	count, err := reader.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestModelDevAcceptanceConcurrentScopeIsolation(t *testing.T) {
	baseContext, firstClient, firstScope, firstCandidate := newModelDevAcceptanceFixture(t)
	_, secondClient, secondScope, secondCandidate := newModelDevAcceptanceFixture(t)
	secondScope.Actor, secondScope.IdempotencyKey = firstScope.Actor, firstScope.IdempotencyKey
	otherKey, otherActor := firstScope, firstScope
	otherKey.IdempotencyKey = uuid.NewString()
	otherActor.Actor = "governance:access-key:9"
	groups := []struct {
		scope     ModelDevAdmissionScope
		candidate ModelDevFrozenCandidate
		repo      *ModelDevAcceptanceRepo
	}{
		{firstScope, firstCandidate, NewModelDevAcceptanceRepo(firstClient)},
		{otherKey, firstCandidate, NewModelDevAcceptanceRepo(firstClient)},
		{otherActor, firstCandidate, NewModelDevAcceptanceRepo(firstClient)},
		{secondScope, secondCandidate, NewModelDevAcceptanceRepo(secondClient)},
	}
	const contenders = 3
	type outcome struct {
		group    int
		accepted *ModelDevAcceptance
		replayed bool
		err      error
	}
	ctx, cancel := context.WithTimeout(baseContext, 15*time.Second)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()
	start := make(chan struct{})
	results := make(chan outcome, len(groups)*contenders)
	proposedIDs := make([]map[string]string, len(groups))
	for groupIndex, group := range groups {
		proposedIDs[groupIndex] = make(map[string]string)
		for range contenders {
			candidate := group.candidate
			candidate.OperationID, candidate.ExecutionID = uuid.NewString(), uuid.NewString()
			candidate = freezeModelDevFixture(t, group.scope, candidate)
			proposedIDs[groupIndex][candidate.OperationID] = candidate.ExecutionID
			workers.Add(1)
			go func(index int, repo *ModelDevAcceptanceRepo, scope ModelDevAdmissionScope, candidate ModelDevFrozenCandidate) {
				defer workers.Done()
				<-start
				accepted, replayed, err := repo.AcceptFrozen(ctx, scope, candidate)
				results <- outcome{index, accepted, replayed, err}
			}(groupIndex, group.repo, group.scope, candidate)
		}
	}
	close(start)
	winners := make([]*ModelDevAcceptance, len(groups))
	fresh := make([]int, len(groups))
	for range len(groups) * contenders {
		var result outcome
		select {
		case result = <-results:
		case <-ctx.Done():
			t.Fatal("concurrent admission did not finish within its bound")
		}
		require.NoError(t, result.err)
		require.NotNil(t, result.accepted)
		require.Equal(t, groups[result.group].scope, result.accepted.Scope)
		require.Contains(t, proposedIDs[result.group], result.accepted.OperationID)
		require.Equal(t, proposedIDs[result.group][result.accepted.OperationID], result.accepted.ExecutionID)
		hash, err := result.accepted.Snapshot.Digest()
		require.NoError(t, err)
		require.Equal(t, hash, result.accepted.ExecutionSpecHash)
		if !result.replayed {
			fresh[result.group]++
		}
		if winners[result.group] == nil {
			winners[result.group] = result.accepted
		} else {
			require.Equal(t, winners[result.group].OperationID, result.accepted.OperationID)
			require.Equal(t, winners[result.group].ExecutionID, result.accepted.ExecutionID)
		}
	}
	workers.Wait()
	uniqueOperations := make(map[string]bool)
	for groupIndex, winner := range winners {
		require.Equal(t, 1, fresh[groupIndex], "one fresh acceptance per tenant/actor/key scope")
		uniqueOperations[winner.OperationID] = true
	}
	require.Len(t, uniqueOperations, len(groups))

	// A forged pairing of two real tenant identities must neither replay a
	// different scope nor create a fifth acceptance. This is storage isolation;
	// current principal authorization remains the inbound service's concern.
	forged := firstScope
	forged.ResourceTenantID = secondScope.ResourceTenantID
	rejected, replayed, err := groups[0].repo.AcceptFrozen(ctx, forged, firstCandidate)
	require.ErrorIs(t, err, cpup01.ErrInvalidArgument)
	require.Nil(t, rejected)
	require.False(t, replayed)
	reader := newModelDevPGClient(t)
	for groupIndex, group := range groups {
		rows, err := reader.Client().ModelDevAcceptance.Query().Where(
			modeldevacceptance.TenantIDEQ(group.scope.TenantID),
			modeldevacceptance.ResourceTenantIDEQ(group.scope.ResourceTenantID),
			modeldevacceptance.ActorEQ(group.scope.Actor),
			modeldevacceptance.ActionEQ(group.scope.Action),
			modeldevacceptance.IdempotencyKeyEQ(group.scope.IdempotencyKey),
		).All(ctx)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, winners[groupIndex].OperationID, rows[0].OperationID)
		require.Equal(t, winners[groupIndex].ExecutionID, rows[0].ExecutionID)
	}
	for tenantID, expected := range map[uint32]int{firstScope.TenantID: 3, secondScope.TenantID: 1} {
		count, err := reader.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(tenantID)).Count(ctx)
		require.NoError(t, err)
		require.Equal(t, expected, count)
	}
}
