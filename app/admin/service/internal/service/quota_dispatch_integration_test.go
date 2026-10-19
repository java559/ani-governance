//go:build quota_pg

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

// retryPeer durably accepts the first request, then deliberately loses its ACK.
// A second identical command must replay the same durable acceptance.
type retryPeer struct {
	mu       sync.Mutex
	accepted map[string]string
	calls    int
	first    chan struct{}
}

func (*retryPeer) OwnerService() string { return "quota-retry-test-owner" }
func (*retryPeer) Actions() []string    { return []string{"RETRY_CREATE"} }
func (p *retryPeer) Dispatch(_ context.Context, cmd *QuotaDispatchCommand) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	payload := fmt.Sprintf("%s/%s/%s", cmd.OperationID, cmd.ResourceID, string(cmd.CanonicalRequest))
	if old, ok := p.accepted[cmd.OperationID]; ok && old != payload {
		return nil, errors.New("replay payload changed")
	}
	p.accepted[cmd.OperationID] = payload
	if p.calls == 1 {
		close(p.first)
		return nil, errors.New("accepted durably; ACK lost")
	}
	return []byte(fmt.Sprintf(`{"operation_id":%q,"resource_id":%q,"accepted":true}`, cmd.OperationID, cmd.ResourceID)), nil
}

func TestQuotaDispatchRetryAfterLostAck(t *testing.T) {
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	plan, err := c.Client().Plan.Create().SetName("retry-peer").Save(ctx)
	require.NoError(t, err)
	tenant, err := c.Client().Tenant.Create().SetName("retry-peer").SetCode("retry-peer").SetPlanID(plan.ID).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(plan.ID).SetQuotaCode(data.QuotaCodeGpuCount).SetQuotaValue(8).Exec(ctx))
	ledger := data.NewQuotaLedgerRepo(testutil.NewBootstrapContext(nil), c)
	peer := &retryPeer{accepted: make(map[string]string), first: make(chan struct{})}
	registry := NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(peer))
	worker := NewQuotaDispatchWorker(testutil.NewBootstrapContext(nil), ledger, registry)
	// Commit before Start: the operation is genuinely QUEUED, with no lease.
	occupied, err := ledger.Occupy(context.Background(), &data.QuotaOccupyInput{TenantID: tenant.ID, ResourceTenantID: tenant.ResourceTenantID, ResourceID: uuid.NewString(), ActorType: "user", ActorID: "1", OwnerService: "quota-retry-test-owner", Action: "RETRY_CREATE", IdempotencyKey: uuid.NewString(), RequestHash: "retry-hash", CanonicalRequest: `{"schema_version":1}`, Items: []data.QuotaOccupyItem{{QuotaCode: data.QuotaCodeGpuCount, Units: 2}}})
	require.NoError(t, err)
	before, err := ledger.GetOperationForUser(context.Background(), tenant.ID, occupied.OperationID)
	require.NoError(t, err)
	require.Equal(t, quotaoperation.DispatchStateQueued, before.DispatchState)
	require.Zero(t, before.AttemptCount)
	require.NoError(t, worker.Start(context.Background()))
	t.Cleanup(func() { _ = worker.Stop(context.Background()) })
	select {
	case <-peer.first:
	case <-time.After(5 * time.Second):
		t.Fatal("first durable peer acceptance not reached")
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		op, e := ledger.GetOperationForUser(context.Background(), tenant.ID, occupied.OperationID)
		require.NoError(t, e)
		if op.DispatchState == quotaoperation.DispatchStateAcked {
			require.Equal(t, 2, op.AttemptCount)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	final, err := ledger.GetOperationForUser(context.Background(), tenant.ID, occupied.OperationID)
	require.NoError(t, err)
	require.Equal(t, quotaoperation.DispatchStateAcked, final.DispatchState)
	peer.mu.Lock()
	require.Len(t, peer.accepted, 1)
	require.Equal(t, 2, peer.calls)
	peer.mu.Unlock()
	rows, err := ledger.RecomputeInvariants(context.Background(), tenant.ID)
	require.NoError(t, err)
	for _, r := range rows {
		require.True(t, r.Balanced)
		if r.QuotaCode == data.QuotaCodeGpuCount {
			require.EqualValues(t, 2, r.OccupiedUnits)
		}
	}
}
