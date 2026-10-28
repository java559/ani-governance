package data

import (
	"context"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

// HasAcceptedInstanceDelete fences only container-instance DELETEs belonging to
// this tenant's immutable CREATE. A definite owner rejection reopens the gate.
func (r *QuotaLedgerRepo) HasAcceptedInstanceDelete(ctx context.Context, tenant uint32, resource string) (blocked bool, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx = appViewer.NewSystemViewerContext(ctx)
		original, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenant), quotaoperation.OwnerServiceEQ(commandcontract.InstanceOwner), quotaoperation.ActionEQ(commandcontract.InstanceCreateAction), quotaoperation.ResourceIDEQ(resource), quotaoperation.CreateOperationIDIsNil()).Only(ctx)
		if e != nil {
			return e
		}
		blocked, e = tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenant), quotaoperation.OwnerServiceEQ(commandcontract.InstanceOwner), quotaoperation.ActionEQ(commandcontract.InstanceDeleteAction), quotaoperation.ResourceIDEQ(resource), quotaoperation.ResourceTenantIDEQ(original.ResourceTenantID), quotaoperation.CreateOperationIDEQ(original.OperationID), quotaoperation.DispatchStateNEQ(quotaoperation.DispatchStateRejected)).Exist(ctx)
		return e
	})
	return
}

// RejectInstanceDeleteVersion is a lease-fenced, never-ACKed definite rejection.
// Unknown transport outcomes cannot use this path and must keep the gate closed.
func (r *QuotaLedgerRepo) RejectInstanceDeleteVersion(ctx context.Context, tenant uint32, id string, generation int64) (ok bool, err error) {
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		now, e := quotaDatabaseNow(ctx, tx)
		if e != nil {
			return e
		}
		ctx = appViewer.NewSystemViewerContext(ctx)
		op, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenant), quotaoperation.OperationIDEQ(id), quotaoperation.OwnerServiceEQ(commandcontract.InstanceOwner), quotaoperation.ActionEQ(commandcontract.InstanceDeleteAction), quotaoperation.CreateOperationIDNotNil(), quotaoperation.LeaseGenerationEQ(generation), quotaoperation.DispatchStateEQ(quotaoperation.DispatchStateDispatching), quotaoperation.AckJSONIsNil(), quotaoperation.LeaseUntilGT(now)).ForUpdate().Only(ctx)
		if ent.IsNotFound(e) {
			return nil
		}
		if e != nil {
			return e
		}
		original, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenant), quotaoperation.OperationIDEQ(*op.CreateOperationID), quotaoperation.OwnerServiceEQ(commandcontract.InstanceOwner), quotaoperation.ActionEQ(commandcontract.InstanceCreateAction), quotaoperation.ResourceIDEQ(op.ResourceID), quotaoperation.ResourceTenantIDEQ(op.ResourceTenantID), quotaoperation.CreateOperationIDIsNil()).Only(ctx)
		if e != nil {
			return e
		}
		if !commandcontract.PairedActions(op.OwnerService, original.Action, op.Action) {
			return commandcontract.Invalid("invalid container delete origin")
		}
		n, e := tx.QuotaOperation.Update().Where(quotaoperation.IDEQ(op.ID), quotaoperation.LeaseGenerationEQ(generation), quotaoperation.DispatchStateEQ(quotaoperation.DispatchStateDispatching), quotaoperation.AckJSONIsNil()).SetUpdatedAt(now).SetDispatchState(quotaoperation.DispatchStateRejected).SetLastErrorCode("INSTANCE_VERSION_CONFLICT").SetRetryBlocked(false).ClearNextAttemptAt().ClearLeaseOwner().ClearLeaseUntil().Save(ctx)
		ok = n == 1
		return e
	})
	return
}
