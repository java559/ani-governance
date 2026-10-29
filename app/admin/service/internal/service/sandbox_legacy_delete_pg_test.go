//go:build quota_pg

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

type legacyDeletePeer struct{ calls int }

func (*legacyDeletePeer) OwnerService() string { return InstanceOwner }
func (*legacyDeletePeer) Actions() []string    { return []string{InstanceDeleteAction} }
func (p *legacyDeletePeer) Dispatch(_ context.Context, cmd *QuotaDispatchCommand) ([]byte, error) {
	p.calls++
	return []byte(fmt.Sprintf(`{"operation_id":%q,"resource_id":%q,"accepted":true}`, cmd.OperationID, cmd.ResourceID)), nil
}

func TestSandboxLegacyInstanceDeleteCanDispatch(t *testing.T) {
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	ctx := context.Background()
	sys := appViewer.NewSystemViewerContext(ctx)
	name := "legacy-instance-delete-" + uuid.NewString()
	tenant, err := c.Client().Tenant.Create().SetName(name).SetCode(name).Save(sys)
	require.NoError(t, err)
	createID, deleteID, resource := uuid.NewString(), uuid.NewString(), uuid.NewString()
	payload, err := res.CanonicalBusinessPayloadVersion(instanceSettingsBusiness(t), 2)
	require.NoError(t, err)
	sum := sha256.Sum256(payload)
	command := &data.InstanceCPUCanonical{Schema: data.InstanceCPUCanonicalSchema, InstanceContractVersion: 2, BusinessPayload: payload, BusinessPayloadDigest: hex.EncodeToString(sum[:])}
	createCanonical, err := data.MarshalInstanceCPUCanonical(command)
	require.NoError(t, err)
	command.DeleteExpectedVersion = 1
	deleteCanonical, err := data.MarshalInstanceCPUCanonical(command)
	require.NoError(t, err)
	// Historical delete rows have no Sandbox preflight marker. Their actual
	// valid command still passes contract validation and reaches the owner.
	for _, row := range []struct{ id, action, original, state, canonical string }{{createID, InstanceCreateAction, "", "ACKED", string(createCanonical)}, {deleteID, InstanceDeleteAction, createID, "DISPATCHING", string(deleteCanonical)}} {
		_, err = c.DB().ExecContext(ctx, `INSERT INTO sys_quota_operations(tenant_id,operation_id,resource_tenant_id,resource_id,create_operation_id,actor_type,actor_id,owner_service,action,idempotency_key,request_hash,canonical_request,dispatch_state,instance_contract_version,created_at,updated_at) VALUES($1,$2,$3,$4,NULLIF($5,''),'user','1',$6,$7,$8,'hash',$9,$10,2,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, tenant.ID, row.id, tenant.ResourceTenantID, resource, row.original, InstanceOwner, row.action, uuid.NewString(), row.canonical, row.state)
		require.NoError(t, err)
	}
	ledger := data.NewQuotaLedgerRepo(testutil.NewBootstrapContext(nil), c)
	claimed := data.ClaimedOperation{TenantID: tenant.ID, OperationID: deleteID, ResourceID: resource, ResourceTenantID: tenant.ResourceTenantID, CreateOperationID: createID, ActorType: "user", ActorID: "1", OwnerService: InstanceOwner, Action: InstanceDeleteAction, RequestHash: "hash", CanonicalRequest: string(deleteCanonical)}
	peer := new(legacyDeletePeer)
	registry := NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(peer))
	worker := NewQuotaDispatchWorker(testutil.NewBootstrapContext(nil), ledger, registry)
	worker.dispatchOne(ctx, &claimed)
	require.Equal(t, 1, peer.calls)
	op, err := ledger.GetOperationForUser(ctx, tenant.ID, deleteID)
	require.NoError(t, err)
	require.Equal(t, "ACKED", string(op.DispatchState))
}
