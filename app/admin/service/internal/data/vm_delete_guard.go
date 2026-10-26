package data

import (
	"context"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

// HasAcceptedVMDelete checks only the tenant's original VM CREATE and its
// matching VM DELETE, including a queued DELETE not yet delivered to Resource.
// Other domains sharing the Resource owner cannot participate in this fence.
func (r *QuotaLedgerRepo) HasAcceptedVMDelete(ctx context.Context, tenant uint32, resource string) (blocked bool, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx := appViewer.NewSystemViewerContext(ctx)
		original, err := tx.QuotaOperation.Query().Where(
			quotaoperation.TenantIDEQ(tenant), quotaoperation.OwnerServiceEQ(commandcontract.InstanceOwner),
			quotaoperation.ActionEQ(commandcontract.VMCreateAction), quotaoperation.ResourceIDEQ(resource),
			quotaoperation.CreateOperationIDIsNil(),
		).Only(ctx)
		if err != nil {
			return err
		}
		if err = validateOperationCharges(original, nil); err != nil {
			return err
		}
		blocked, err = tx.QuotaOperation.Query().Where(
			quotaoperation.TenantIDEQ(tenant), quotaoperation.OwnerServiceEQ(commandcontract.InstanceOwner),
			quotaoperation.ActionEQ(commandcontract.VMDeleteAction), quotaoperation.ResourceIDEQ(resource),
			quotaoperation.ResourceTenantIDEQ(original.ResourceTenantID), quotaoperation.CreateOperationIDEQ(original.OperationID),
			quotaoperation.DispatchStateNEQ(quotaoperation.DispatchStateRejected),
		).Exist(ctx)
		return err
	})
	return blocked, err
}

// RejectVMDeleteVersion is valid only for the claimed, never-ACKed VM DELETE
// whose owner explicitly rejected the frozen expected version before admission.
// Transport uncertainty must never call this method.
func (r *QuotaLedgerRepo) RejectVMDeleteVersion(ctx context.Context, tenant uint32, id string, generation int64) (ok bool, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		now, e := quotaDatabaseNow(ctx, tx)
		if e != nil {
			return e
		}
		ctx := appViewer.NewSystemViewerContext(ctx)
		n, e := tx.QuotaOperation.Update().Where(quotaoperation.TenantIDEQ(tenant), quotaoperation.OperationIDEQ(id), quotaoperation.OwnerServiceEQ(commandcontract.InstanceOwner), quotaoperation.ActionEQ(commandcontract.VMDeleteAction), quotaoperation.CreateOperationIDNotNil(), quotaoperation.LeaseGenerationEQ(generation), quotaoperation.DispatchStateEQ(quotaoperation.DispatchStateDispatching)).SetUpdatedAt(now).SetDispatchState(quotaoperation.DispatchStateRejected).SetLastErrorCode("VM_VERSION_CONFLICT").SetRetryBlocked(false).ClearNextAttemptAt().ClearLeaseOwner().ClearLeaseUntil().Save(ctx)
		ok = n == 1
		return e
	})
	return
}
