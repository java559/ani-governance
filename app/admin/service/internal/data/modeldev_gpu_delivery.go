package data

import (
	"bytes"
	"context"
	"fmt"
	"math"

	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
)

// Only ModelDev's fenced delivery can acknowledge this accounting operation.
// The generic quota worker cannot claim it. The immutable owner receipt and
// original operation transition together in the acceptance transaction.
func ackModelDevGPUInTx(ctx context.Context, tx *ent.Tx, row *ent.ModelDevAcceptance, accepted *ModelDevAcceptance, receipt []byte) error {
	if accepted.Snapshot.GPU == nil {
		return nil
	}
	op, err := tx.QuotaOperation.Query().Where(
		quotaoperation.TenantIDEQ(accepted.Scope.TenantID),
		quotaoperation.ResourceTenantIDEQ(accepted.Scope.ResourceTenantID),
		quotaoperation.OwnerServiceEQ(commandcontract.ModelDevOwner),
		quotaoperation.OperationIDEQ(accepted.OperationID),
		quotaoperation.ResourceIDEQ(accepted.ExecutionID),
		quotaoperation.CreateOperationIDIsNil(),
	).ForUpdate().Only(ctx)
	if err != nil {
		return err
	}
	charges, err := tx.QuotaCharge.Query().Where(quotacharge.TenantIDEQ(accepted.Scope.TenantID), quotacharge.OperationIDEQ(accepted.OperationID)).All(ctx)
	if err != nil {
		return err
	}
	if err := validateOperationCharges(op, charges); err != nil {
		return err
	}
	original, err := commandcontract.DecodeModelDevGPUCanonical([]byte(op.CanonicalRequest))
	if err != nil {
		return err
	}
	intent, snapshot, err := accepted.Envelope().CanonicalPayloads()
	if err != nil || !bytes.Equal(intent, original.IntentCanonical) || !bytes.Equal(snapshot, original.SnapshotCanonical) || original.ExecutionSpecHash != accepted.ExecutionSpecHash {
		return fmt.Errorf("invalid ModelDev original quota association")
	}
	if op.DispatchState == quotaoperation.DispatchStateAcked {
		if op.AckJSON == nil || *op.AckJSON != string(receipt) {
			return fmt.Errorf("conflicting ModelDev original quota acknowledgement")
		}
		return nil
	}
	if op.DispatchState != quotaoperation.DispatchStateQueued || row.AttemptCount > math.MaxInt {
		return fmt.Errorf("invalid ModelDev original quota delivery state")
	}
	now, err := quotaDatabaseNow(ctx, tx)
	if err != nil {
		return err
	}
	_, err = tx.QuotaOperation.UpdateOne(op).Where(
		quotaoperation.TenantIDEQ(accepted.Scope.TenantID), quotaoperation.OwnerServiceEQ(commandcontract.ModelDevOwner), quotaoperation.DispatchStateEQ(quotaoperation.DispatchStateQueued),
	).SetUpdatedAt(now).SetDispatchState(quotaoperation.DispatchStateAcked).SetAckJSON(string(receipt)).SetAttemptCount(int(row.AttemptCount)).SetLeaseGeneration(row.LeaseGeneration).Save(ctx)
	return err
}
