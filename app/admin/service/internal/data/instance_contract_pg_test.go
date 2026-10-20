//go:build quota_pg

package data

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestQuotaPostgresInstanceConcurrentAdmissionUsesCurrentContract(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenant, _ := seedTenantPlan(t, c, "instance-contract-race", 8)
	key := uuid.NewString()
	inputs := make([]*QuotaOccupyInput, 2)
	for i := range inputs {
		input := occupyInput(c, tenant, "7", key, 1)
		input.OwnerService, input.Action, input.Items = instanceCPUOwner, instanceCPUCreateAction, nil
		input.InstanceContractVersion = CurrentInstanceContractVersion
		input.CanonicalRequest = string(frozenCPUConfiguration(t))
		input.RequestHash = "current-instance-intent"
		inputs[i] = input
	}
	start := make(chan struct{})
	results := make([]*QuotaOccupyResult, 2)
	errors := make([]error, 2)
	var wg sync.WaitGroup
	for i := range inputs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errors[index] = repo.Occupy(context.Background(), inputs[index])
		}(i)
	}
	close(start)
	wg.Wait()
	require.NoError(t, errors[0])
	require.NoError(t, errors[1])
	require.Equal(t, results[0].OperationID, results[1].OperationID)
	require.NotEqual(t, results[0].Replayed, results[1].Replayed)
	winner, err := repo.FindIdempotentOperation(context.Background(), tenant, "user", "7", instanceCPUCreateAction, key)
	require.NoError(t, err)
	require.EqualValues(t, 2, winner.InstanceContractVersion)
	require.Equal(t, "current-instance-intent", winner.RequestHash)
	require.Equal(t, string(frozenCPUConfiguration(t)), winner.CanonicalRequest)
	require.True(t, AllowsInstanceCPUCommand(winner.OwnerService, winner.Action, winner.CanonicalRequest))
	require.Empty(t, results[0].ChargeIDs)
}

func TestQuotaPostgresInstanceRejectsRetiredAdmission(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenant, _ := seedTenantPlan(t, c, "instance-current-contract", 8)
	for _, version := range []uint32{0, 1, 3} {
		input := occupyInput(c, tenant, "7", uuid.NewString(), 1)
		input.OwnerService, input.Action, input.Items = instanceCPUOwner, instanceCPUCreateAction, nil
		input.InstanceContractVersion = version
		input.CanonicalRequest = string(frozenCPUConfiguration(t))
		result, err := repo.Occupy(context.Background(), input)
		require.Error(t, err)
		require.Nil(t, result)
	}
}
