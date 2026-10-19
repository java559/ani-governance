//go:build quota_pg

package data

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/gpuusagesync"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

func TestQuotaGpuSyncLeaseRetryAndBlock(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	r := newTestRepo(c)
	ctx := context.Background()
	tid, pid := seedTenantPlan(t, c, "sync-lease", 20)
	otherTid, _ := seedTenantPlan(t, c, "sync-other-tenant", 20)
	in := gpuLedgerInput(t, c.Client(), tid, pid)
	accepted, e := r.Occupy(ctx, in)
	require.NoError(t, e)
	id := accepted.OperationID
	payload := gpuProjectionPayload(in, id, 1, "declared-lease")
	require.NoError(t, r.UpsertGpuUsageProjection(ctx, tid, id, 1, "DECLARED", payload, "declared-lease"))
	read := func() *ent.GpuUsageSync {
		var row *ent.GpuUsageSync
		require.NoError(t, r.transaction(ctx, func(tx *ent.Tx) error {
			ctx := appViewer.NewSystemViewerContext(ctx)
			var e error
			row, e = tx.GpuUsageSync.Query().Where(gpuusagesync.TenantIDEQ(tid), gpuusagesync.OperationIDEQ(id)).Only(ctx)
			return e
		}))
		return row
	}
	balance := func(gpu, storage int64) {
		op, e := r.GetOperationForUser(ctx, tid, id)
		require.NoError(t, e)
		require.Zero(t, op.AttemptCount)
		rows, e := r.RecomputeInvariants(ctx, tid)
		require.NoError(t, e)
		for _, v := range rows {
			require.True(t, v.Balanced)
			if isGPUCode(v.QuotaCode) {
				require.Equal(t, gpu, v.OccupiedUnits)
			} else if v.QuotaCode == "storage.bytes" {
				require.Equal(t, storage, v.OccupiedUnits)
			}
		}
	}
	ok, e := r.AckGpuUsageSync(ctx, tid, id, 1, 0)
	require.NoError(t, e)
	require.False(t, ok, "a projection that has never been claimed cannot be acknowledged")
	var wg sync.WaitGroup
	start := make(chan struct{})
	claims := make(chan []GpuUsageSyncRecord, 2)
	errs := make(chan error, 2)
	for _, worker := range []string{"sync-a", "sync-b"} {
		wg.Add(1)
		go func(worker string) {
			defer wg.Done()
			<-start
			v, e := r.ClaimGpuUsageSync(ctx, worker, time.Second, 1)
			claims <- v
			errs <- e
		}(worker)
	}
	close(start)
	wg.Wait()
	close(claims)
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var first GpuUsageSyncRecord
	n := 0
	for v := range claims {
		n += len(v)
		if len(v) > 0 {
			first = v[0]
		}
	}
	require.Equal(t, 1, n, "two workers cannot hold the same live lease")
	var second GpuUsageSyncRecord
	require.Eventually(t, func() bool {
		v, e := r.ClaimGpuUsageSync(ctx, "takeover", time.Second, 1)
		require.NoError(t, e)
		if len(v) == 1 {
			second = v[0]
			return true
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	require.Greater(t, second.LeaseGeneration, first.LeaseGeneration)
	ok, e = r.AckGpuUsageSync(ctx, tid, id, 1, first.LeaseGeneration)
	require.NoError(t, e)
	require.False(t, ok)
	ok, e = r.BlockGpuUsageSync(ctx, tid, id, 1, first.LeaseGeneration, "STALE")
	require.NoError(t, e)
	require.False(t, ok)
	var databaseNow time.Time
	require.NoError(t, r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		tenant, e := lockQuotaTenant(ctx, tx, tid)
		databaseNow = tenant.DatabaseNow
		return e
	}))
	due := databaseNow.Add(250 * time.Millisecond)
	ok, e = r.RetryGpuUsageSync(ctx, tid, id, 1, second.LeaseGeneration, due, "RPC_UNAVAILABLE")
	require.NoError(t, e)
	require.True(t, ok)
	row := read()
	require.Equal(t, payload, row.PayloadJSON)
	require.Equal(t, "declared-lease", row.PayloadHash)
	require.NotNil(t, row.LastErrorCode)
	require.Equal(t, "RPC_UNAVAILABLE", *row.LastErrorCode)
	require.Equal(t, due.UnixMicro(), row.NextAttemptAt.UnixMicro())
	require.Nil(t, row.LeaseUntil)
	v, e := r.ClaimGpuUsageSync(ctx, "too-early", time.Second, 1)
	require.NoError(t, e)
	require.Empty(t, v)
	var third GpuUsageSyncRecord
	require.Eventually(t, func() bool {
		v, e := r.ClaimGpuUsageSync(ctx, "retry", time.Second, 1)
		require.NoError(t, e)
		if len(v) == 1 {
			third = v[0]
			return true
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	ok, e = r.RetryGpuUsageSync(ctx, tid, id, 1, second.LeaseGeneration, due, "STALE")
	require.NoError(t, e)
	require.False(t, ok)
	ok, e = r.BlockGpuUsageSync(ctx, tid, id, 1, third.LeaseGeneration, "PAYLOAD_CONFLICT")
	require.NoError(t, e)
	require.True(t, ok)
	row = read()
	require.True(t, row.RetryBlocked)
	require.Equal(t, payload, row.PayloadJSON)
	require.Equal(t, "PAYLOAD_CONFLICT", *row.LastErrorCode)
	v, e = r.ClaimGpuUsageSync(ctx, "blocked", time.Second, 1)
	require.NoError(t, e)
	require.Empty(t, v)
	balance(2, 10)
	// A real local deletion supplies the new terminal facts; the sync adapter
	// alone has left both business balances and business attempt_count unchanged.
	deleted, e := r.CreateDeleteOperation(ctx, gpuDelete(t, in))
	require.NoError(t, e)
	require.True(t, deleted.LocalCanceled)
	balance(0, 0)
	terminal := gpuProjectionPayload(in, id, 2, "ended-lease")
	require.NoError(t, r.UpsertGpuUsageProjection(ctx, tid, id, 2, "ENDED", terminal, "ended-lease"))
	row = read()
	require.False(t, row.RetryBlocked)
	require.Greater(t, row.LeaseGeneration, third.LeaseGeneration)
	require.Nil(t, row.LeaseUntil)
	require.Equal(t, int64(0), row.AckedRevision)
	ok, e = r.AckGpuUsageSync(ctx, tid, id, 1, third.LeaseGeneration)
	require.NoError(t, e)
	require.False(t, ok)
	ok, e = r.AckGpuUsageSync(ctx, tid, id, 2, third.LeaseGeneration)
	require.NoError(t, e)
	require.False(t, ok, "old lease cannot acknowledge unsent terminal revision")
	ok, e = r.AckGpuUsageSync(ctx, tid, id, 2, row.LeaseGeneration)
	require.NoError(t, e)
	require.False(t, ok, "a current generation still requires an actual claim")
	v, e = r.ClaimGpuUsageSync(ctx, "terminal", time.Second, 1)
	require.NoError(t, e)
	require.Len(t, v, 1)
	require.Equal(t, int64(2), v[0].Revision)
	ok, e = r.AckGpuUsageSync(ctx, otherTid, id, 2, v[0].LeaseGeneration)
	require.NoError(t, e)
	require.False(t, ok)
	ok, e = r.AckGpuUsageSync(ctx, tid, id, 2, v[0].LeaseGeneration)
	require.NoError(t, e)
	require.True(t, ok)
	ok, e = r.BlockGpuUsageSync(ctx, tid, id, 2, v[0].LeaseGeneration, "LATE_ERROR")
	require.NoError(t, e)
	require.False(t, ok)
	row = read()
	require.Equal(t, int64(2), row.AckedRevision)
	require.Nil(t, row.LastErrorCode)
	require.Equal(t, terminal, row.PayloadJSON)
	balance(0, 0)
}
