//go:build modeldev_pg

package data

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

func TestModelDevAcceptancePersistsAndReplaysFrozenVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 15*time.Second)
	t.Cleanup(cancel)
	client := newModelDevPGClient(t)
	tenant, err := client.Client().Tenant.Create().SetName("modeldev CPU acceptance").SetCode("cpu-" + uuid.NewString()).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
		defer stop()
		_, e := client.Client().ModelDevAcceptance.Delete().Where(modeldevacceptance.TenantIDEQ(tenant.ID)).Exec(cleanup)
		require.NoError(t, e)
		require.NoError(t, client.Client().Tenant.DeleteOneID(tenant.ID).Exec(cleanup))
	})
	scope := ModelDevAdmissionScope{TenantID: tenant.ID, ResourceTenantID: tenant.ResourceTenantID, Actor: "governance:user:7", Action: ModelDevCreateAction, IdempotencyKey: uuid.NewString()}
	snapshot := conformance.SnapshotV1()
	// Shared vectors are individually normative. Align the intent's asset IDs
	// with the snapshot fixture; these synthetic references are never ENV facts.
	intent := conformance.IntentV1()
	intent.ComputePresetID = snapshot.ComputeOrigin.ComputePresetID
	intent.DatasetVersionID = snapshot.Input.InputVersionID
	candidate := ModelDevFrozenCandidate{OperationID: uuid.NewString(), ExecutionID: uuid.NewString(), Intent: intent, Snapshot: snapshot, AcceptedAt: time.Now().UTC().Truncate(time.Microsecond)}
	candidate = freezeModelDevFixture(t, scope, candidate)
	intentCanonical, intentHash, err := cpup01.CanonicalIntent(intent)
	require.NoError(t, err)

	accepted, replayed, err := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)
	require.NotNil(t, accepted)
	require.Equal(t, candidate.OperationID, accepted.OperationID)
	require.Equal(t, candidate.ExecutionID, accepted.ExecutionID)
	require.Equal(t, scope, accepted.Scope)
	require.Equal(t, intentHash, accepted.IntentHash)
	require.Equal(t, fixtureSnapshotHash(t, candidate), accepted.ExecutionSpecHash)
	require.Equal(t, "QUEUED", accepted.DispatchState)
	acceptedIntent, _, err := cpup01.CanonicalIntent(accepted.Intent)
	require.NoError(t, err)
	require.Equal(t, intentCanonical, acceptedIntent)

	// A separately opened connection observes committed rows before any remote
	// ModelDev call. The repository result alone is not persistence evidence.
	reader := newModelDevPGClient(t)
	stored, err := reader.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(tenant.ID), modeldevacceptance.OperationIDEQ(candidate.OperationID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, intentCanonical, stored.IntentCanonical)
	require.Equal(t, fixtureSnapshotBytes(t, candidate), stored.SnapshotCanonical)
	require.Equal(t, scope.ResourceTenantID, stored.ResourceTenantID)
	require.Equal(t, scope.Actor, stored.Actor)
	require.Equal(t, scope.IdempotencyKey, stored.IdempotencyKey)
	require.Equal(t, candidate.ExecutionID, stored.ExecutionID)
	require.Equal(t, intentHash, stored.IntentHash)
	require.Equal(t, fixtureSnapshotHash(t, candidate), stored.ExecutionSpecHash)
	require.Equal(t, "QUEUED", string(stored.DispatchState))
	require.True(t, candidate.AcceptedAt.Equal(stored.AcceptedAt))

	// New resolved defaults and newly proposed IDs must never replace an
	// already accepted execution after process/repository reconstruction.
	next := candidate
	next.OperationID, next.ExecutionID = uuid.NewString(), uuid.NewString()
	next.Snapshot = conformance.SnapshotV1()
	next.Snapshot.Implementation.Version = "different-new-default"
	next.Snapshot.Implementation.PipelineVersionID = uuid.NewString()
	next.AcceptedAt = candidate.AcceptedAt.Add(time.Minute)
	replayedRecord, replayed, err := NewModelDevAcceptanceRepo(reader).AcceptFrozen(ctx, scope, next)
	require.NoError(t, err)
	require.True(t, replayed)
	require.NotNil(t, replayedRecord)
	require.Equal(t, accepted.OperationID, replayedRecord.OperationID)
	require.Equal(t, accepted.ExecutionID, replayedRecord.ExecutionID)
	require.Equal(t, accepted.IntentHash, replayedRecord.IntentHash)
	require.Equal(t, accepted.ExecutionSpecHash, replayedRecord.ExecutionSpecHash)
	require.True(t, accepted.AcceptedAt.Equal(replayedRecord.AcceptedAt))
	require.Equal(t, "QUEUED", replayedRecord.DispatchState)
	replayedIntent, _, err := cpup01.CanonicalIntent(replayedRecord.Intent)
	require.NoError(t, err)
	require.Equal(t, intentCanonical, replayedIntent)
	actualCanonical, err := replayedRecord.Snapshot.Canonical()
	require.NoError(t, err)
	require.Equal(t, fixtureSnapshotBytes(t, candidate), actualCanonical)
	count, err := reader.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(tenant.ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

// This helper only opens the explicitly selected, pre-migrated exclusive PG
// database. It neither creates schema nor resets unrelated fixtures.
func newModelDevPGClient(t *testing.T) *entCrud.EntClient[*ent.Client] {
	t.Helper()
	dsn := os.Getenv("ANI_TEST_DATABASE_DSN")
	if dsn == "" || os.Getenv("ANI_TEST_DATABASE_EXCLUSIVE") != "1" {
		t.Fatal("modeldev_pg requires ANI_TEST_DATABASE_DSN and ANI_TEST_DATABASE_EXCLUSIVE=1")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	driver := entsql.OpenDB("postgres", db)
	client := ent.NewClient(ent.Driver(driver))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx))
	_, err = client.ModelDevAcceptance.Query().Limit(1).Exist(ctx)
	require.NoError(t, err, "modeldev acceptance schema and runtime read grant must be prepared before product RED")
	return entCrud.NewEntClient(client, driver)
}

func TestModelDevCPUAcceptanceCreatesNoGPUAccounting(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	accepted, replayed, err := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)
	require.NotNil(t, accepted)
	// These are actual tenant-scoped reads through the restricted runtime role.
	// CPU admission has no zero-valued placeholder operation, charge or account.
	operations, err := client.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(scope.TenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, operations)
	charges, err := client.Client().QuotaCharge.Query().Where(quotacharge.TenantIDEQ(scope.TenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, charges)
	accounts, err := client.Client().QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(scope.TenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, accounts)
}
