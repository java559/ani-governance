package data

import (
	"context"

	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent"
)

// ModelDevGPUQuotaInput is a pure translation from one frozen admission and
// its original ACC response. CPU has no ledger input, operation or zero charge.
func ModelDevGPUQuotaInput(scope ModelDevAdmissionScope, candidate ModelDevFrozenCandidate, resolved *acc.ResolvedGpuRequest) (*QuotaOccupyInput, error) {
	if err := validateModelDevAdmissionScope(scope); err != nil {
		return nil, err
	}
	_, intentHash, err := cpup01.CanonicalIntent(candidate.Intent)
	if err != nil {
		return nil, err
	}
	specHash, err := candidate.Snapshot.Digest()
	if err != nil {
		return nil, err
	}
	envelope := cpup01.AdmissionEnvelope{TenantID: scope.ResourceTenantID, Actor: scope.Actor, OperationID: candidate.OperationID, ExecutionID: candidate.ExecutionID, Intent: candidate.Intent, IntentHash: intentHash, Snapshot: candidate.Snapshot, SpecHash: specHash, AcceptedAt: candidate.AcceptedAt}
	if _, _, err := envelope.CanonicalPayloads(); err != nil {
		return nil, err
	}
	actorType, actorID, err := commandcontract.ModelDevLedgerActor(scope.Actor)
	if err != nil {
		return nil, err
	}
	if candidate.Snapshot.ComputeOrigin.DeviceMode == "cpu" {
		if resolved != nil {
			return nil, commandcontract.Invalid("CPU ModelDev contains GPU accounting")
		}
		return nil, nil
	}
	frozen, err := commandcontract.NewModelDevGPUCanonical(envelope, resolved)
	if err != nil {
		return nil, err
	}
	raw, err := frozen.Canonical()
	if err != nil {
		return nil, err
	}
	hash, err := commandcontract.ModelDevGPUCanonicalHash(raw)
	if err != nil {
		return nil, err
	}
	in := &QuotaOccupyInput{TenantID: scope.TenantID, ResourceTenantID: scope.ResourceTenantID, ResourceID: candidate.ExecutionID, OperationID: candidate.OperationID, ActorType: actorType, ActorID: actorID, OwnerService: commandcontract.ModelDevOwner, Action: commandcontract.ModelDevCreateAction, IdempotencyKey: scope.IdempotencyKey, RequestHash: hash, CanonicalRequest: string(raw)}
	for _, q := range frozen.QuotaItems {
		in.Items = append(in.Items, QuotaOccupyItem{QuotaCode: q.QuotaCode, Units: q.Units})
	}
	refs := make([]commandcontract.ChargeRef, 0, len(in.Items))
	for _, q := range in.Items {
		refs = append(refs, commandcontract.ChargeRef{QuotaCode: q.QuotaCode, ChargedUnits: q.Units})
	}
	if _, err := commandcontract.ValidateModelDevStoredCommand(commandcontract.StoredCommand{Owner: in.OwnerService, Action: in.Action, TenantID: in.ResourceTenantID, ResourceID: in.ResourceID, OperationID: in.OperationID, ActorType: in.ActorType, ActorID: in.ActorID, Canonical: raw, Charges: refs}); err != nil {
		return nil, err
	}
	return in, nil
}

// OccupyModelDevGPUInTx participates in AcceptFrozen's existing transaction.
// It cannot perform resolution, open/commit a transaction or deliver a command.
func (r *QuotaLedgerRepo) OccupyModelDevGPUInTx(ctx context.Context, tx *ent.Tx, scope ModelDevAdmissionScope, candidate ModelDevFrozenCandidate, resolved *acc.ResolvedGpuRequest) (*QuotaOccupyResult, error) {
	in, err := ModelDevGPUQuotaInput(scope, candidate, resolved)
	if err != nil || in == nil {
		return nil, err
	}
	if ctx == nil || ctx.Err() != nil || r == nil || tx == nil {
		return nil, commandcontract.Invalid("ModelDev GPU requires original acceptance transaction")
	}
	result, err := r.occupyInTx(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if result == nil || result.OperationID != candidate.OperationID || result.ResourceID != candidate.ExecutionID {
		return nil, commandcontract.Invalid("ModelDev quota operation association mismatch")
	}
	return result, nil
}
