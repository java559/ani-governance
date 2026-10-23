//go:build modeldev_pg

package data

import (
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"testing"
	"time"
)

// Freeze the actual scope/candidate facts in a test snapshot. This helper does
// not write database state or bypass the production envelope validator.
func freezeModelDevFixture(t *testing.T, scope ModelDevAdmissionScope, candidate ModelDevFrozenCandidate) ModelDevFrozenCandidate {
	t.Helper()
	_, hash, err := cpup01.CanonicalIntent(candidate.Intent)
	require.NoError(t, err)
	if candidate.AcceptedAt.IsZero() {
		candidate.AcceptedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	old := candidate.Snapshot.Timing
	timing, err := (cpup01.TimingConfiguration{ExecutionTimeoutDefaultSeconds: old.ExecutionTimeoutSeconds, ExecutionTimeoutMaxSeconds: old.ExecutionTimeoutMaxSeconds, StepTimeoutSeconds: old.StepTimeoutSeconds}).Resolve(candidate.AcceptedAt, candidate.Intent.TimeoutSeconds)
	require.NoError(t, err)
	candidate.Snapshot.IdentityRef = cpup01.IdentityRef{TenantID: scope.ResourceTenantID, Actor: scope.Actor, OperationID: candidate.OperationID, ExecutionID: candidate.ExecutionID, IntentHash: hash}
	candidate.Snapshot.Timing = timing
	candidate.Snapshot.DeadlineAt = timing.ExecutionDeadlineAt
	candidate.Snapshot.RuntimePlan.Name = cpup01.RuntimeName(candidate.Snapshot.Environment.NamespaceUID, candidate.ExecutionID)
	return candidate
}
func fixtureSnapshotBytes(t *testing.T, candidate ModelDevFrozenCandidate) []byte {
	t.Helper()
	b, e := candidate.Snapshot.Canonical()
	require.NoError(t, e)
	return b
}
func fixtureSnapshotHash(t *testing.T, candidate ModelDevFrozenCandidate) string {
	t.Helper()
	h, e := candidate.Snapshot.Digest()
	require.NoError(t, e)
	return h
}

func requireValidModelDevCandidate(t *testing.T, scope ModelDevAdmissionScope, candidate ModelDevFrozenCandidate) {
	t.Helper()
	_, intentHash, err := cpup01.CanonicalIntent(candidate.Intent)
	require.NoError(t, err)
	specHash, err := candidate.Snapshot.Digest()
	require.NoError(t, err)
	envelope := cpup01.AdmissionEnvelope{
		TenantID: scope.ResourceTenantID, Actor: scope.Actor,
		OperationID: candidate.OperationID, ExecutionID: candidate.ExecutionID,
		Intent: candidate.Intent, IntentHash: intentHash,
		Snapshot: candidate.Snapshot, SpecHash: specHash, AcceptedAt: candidate.AcceptedAt,
	}
	_, _, err = envelope.CanonicalPayloads()
	require.NoError(t, err)
}
