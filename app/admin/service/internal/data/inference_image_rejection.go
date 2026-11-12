package data

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/google/uuid"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/telemetry"
)

// InferenceImageRejection is stored on the rejected CREATE. The linked DELETE
// is its durable close intent; neither record claims physical release or refund.
type InferenceImageRejection struct {
	Reason           string `json:"reason"`
	Message          string `json:"message"`
	CloseOperationID string `json:"close_operation_id"`
}

var inferenceImageReason = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// RejectInferenceImageAndClose atomically stops a known unaccepted CREATE and
// queues its independent DELETE with the original snapshot and charge linkage.
// A lost response or process restart therefore cannot lose the close intent.
func (r *QuotaLedgerRepo) RejectInferenceImageAndClose(ctx context.Context, claim *ClaimedOperation, reason, message string) (out *QuotaDeleteResult, err error) {
	if claim == nil || claim.TenantID == 0 || claim.OwnerService != commandcontract.InferenceOwner || claim.Action != commandcontract.InferenceCreateAction || claim.CreateOperationID != "" || !inferenceImageReason.MatchString(reason) || len(message) > 512 {
		return nil, commandcontract.Invalid("invalid inference image rejection")
	}
	err = r.transaction(ctx, func(tx *ent.Tx) error {
		ctx = appViewer.NewSystemViewerContext(ctx)
		if _, e := lockQuotaTenant(ctx, tx, claim.TenantID); e != nil {
			return e
		}
		original, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(claim.TenantID), quotaoperation.OperationIDEQ(claim.OperationID)).ForUpdate().Only(ctx)
		if e != nil {
			return e
		}
		if original.OwnerService != commandcontract.InferenceOwner || original.Action != commandcontract.InferenceCreateAction || original.CreateOperationID != nil || original.ResourceTenantID != claim.ResourceTenantID || original.ResourceID != claim.ResourceID || original.CanonicalRequest != claim.CanonicalRequest {
			return commandcontract.Invalid("inference rejection origin mismatch")
		}
		if original.DispatchState == quotaoperation.DispatchStateRejected {
			var saved InferenceImageRejection
			if original.AckJSON == nil || json.Unmarshal([]byte(*original.AckJSON), &saved) != nil || saved.CloseOperationID == "" || saved.Reason != reason {
				return commandcontract.Invalid("inference rejection replay mismatch")
			}
			close, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(claim.TenantID), quotaoperation.OperationIDEQ(saved.CloseOperationID), quotaoperation.CreateOperationIDEQ(original.OperationID), quotaoperation.ActionEQ(commandcontract.InferenceDeleteAction)).Only(ctx)
			if e != nil {
				return e
			}
			out = deleteResult(close, nil, true)
			return nil
		}
		// A stale lease or an ACK cannot be converted into a known rejection.
		if original.DispatchState != quotaoperation.DispatchStateDispatching || original.LeaseGeneration != claim.LeaseGeneration {
			return nil
		}
		c, e := commandcontract.DecodeInferenceCanonical([]byte(original.CanonicalRequest))
		if e != nil {
			return e
		}
		c.DeleteExpectedGeneration = 0
		c.DeleteScope = &runtimev1.ManagedDeleteScope{ResourceClass: c.ResourceClass, ClusterId: c.ClusterID, CreateCommandDigest: c.CreateCommandDigest}
		canonical, e := commandcontract.CanonicalJSON(c)
		if e != nil {
			return e
		}
		key := uuid.NewSHA1(uuid.NameSpaceOID, []byte("inference-image-close:"+original.OperationID)).String()
		hash, e := commandcontract.Hash(map[string]any{"schema": "gov-inference-image-close-v1", "tenant_id": original.ResourceTenantID, "create_operation_id": original.OperationID, "resource_id": original.ResourceID, "create_command_digest": c.CreateCommandDigest})
		if e != nil {
			return e
		}
		databaseNow, e := quotaDatabaseNow(ctx, tx)
		if e != nil {
			return e
		}
		// Set REJECTED before ordinary deletion logic, preventing local cancel
		// from refunding a CREATE that has already had a delivery attempt.
		if e = tx.QuotaOperation.UpdateOne(original).SetUpdatedAt(databaseNow).SetDispatchState(quotaoperation.DispatchStateRejected).SetRetryBlocked(true).SetLastErrorCode(reason).ClearNextAttemptAt().ClearLeaseOwner().ClearLeaseUntil().Exec(ctx); e != nil {
			return e
		}
		in := &QuotaDeleteInput{TenantID: claim.TenantID, ActorType: original.ActorType, ActorID: original.ActorID, OwnerService: original.OwnerService, Action: commandcontract.InferenceDeleteAction, IdempotencyKey: key, RequestHash: hash, ResourceID: original.ResourceID, CanonicalRequest: string(canonical), TelemetryContext: telemetry.Capture(ctx)}
		// A concurrent authorized user close may already have committed under
		// this same tenant lock. Attach it instead of creating a second close.
		close, e := tx.QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(claim.TenantID), quotaoperation.CreateOperationIDEQ(original.OperationID), quotaoperation.OwnerServiceEQ(original.OwnerService), quotaoperation.ActionEQ(commandcontract.InferenceDeleteAction), quotaoperation.DispatchStateNEQ(quotaoperation.DispatchStateRejected)).Order(ent.Asc(quotaoperation.FieldID)).First(ctx)
		if e == nil {
			charges, err := tx.QuotaCharge.Query().Where(quotacharge.TenantIDEQ(claim.TenantID), quotacharge.OperationIDEQ(original.OperationID)).All(ctx)
			if err != nil {
				return err
			}
			if err = validateOperationCharges(close, charges); err != nil {
				return err
			}
			out = deleteResult(close, charges, true)
		} else if ent.IsNotFound(e) {
			if e = createDeleteOperationTx(ctx, tx, in, &out); e != nil {
				return e
			}
		} else {
			return e
		}
		saved, e := json.Marshal(InferenceImageRejection{Reason: reason, Message: message, CloseOperationID: out.OperationID})
		if e != nil {
			return e
		}
		return tx.QuotaOperation.UpdateOne(original).SetAckJSON(string(saved)).Exec(ctx)
	})
	return out, err
}
