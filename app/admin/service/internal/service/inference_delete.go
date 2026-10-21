package service

import (
	"context"
	"strconv"

	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	commandv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/command/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/pkg/middleware/auth"
)

func (s *GpuAcceptance) acceptInferenceDelete(ctx context.Context, p *auth.Principal, key, resource string, generation int64, original *ent.QuotaOperation) (*commandv1.GpuDeleteAcceptance, error) {
	c, e := commandcontract.DecodeInferenceCanonical([]byte(original.CanonicalRequest))
	if e != nil {
		return nil, e
	}
	charges, e := s.ledger.GetChargesForOperation(ctx, p.TenantID, original.OperationID)
	if e != nil {
		return nil, e
	}
	refs := make([]QuotaChargeRef, 0, len(charges))
	for _, q := range charges {
		refs = append(refs, QuotaChargeRef{ChargeID: q.ChargeID, QuotaCode: q.QuotaCode, ChargedUnits: q.OriginalUnits})
	}
	if _, e = validateDispatchCommand(original.OwnerService, &QuotaDispatchCommand{OperationID: original.OperationID, ResourceID: original.ResourceID, ResourceTenantID: original.ResourceTenantID, Actor: QuotaActor{Type: original.ActorType, ID: original.ActorID}, Action: original.Action, CanonicalRequest: []byte(original.CanonicalRequest), Charges: refs}); e != nil {
		return nil, e
	}
	actorID := strconv.FormatUint(uint64(p.ID), 10)
	c.DeleteExpectedGeneration = generation
	c.DeleteScope = &runtimev1.ManagedDeleteScope{ResourceClass: c.ResourceClass, ClusterId: c.ClusterID, CreateCommandDigest: c.CreateCommandDigest}
	raw, e := commandcontract.CanonicalJSON(c)
	if e != nil {
		return nil, e
	}
	hash, e := commandcontract.Hash(map[string]any{"schema": "gov-inference-delete-intent-v2", "tenant_id": original.ResourceTenantID, "actor_type": string(p.Type), "actor_id": actorID, "owner": original.OwnerService, "action": InferenceDeleteAction, "resource_id": resource, "create_operation_id": original.OperationID, "create_command_digest": c.CreateCommandDigest, "expected_generation": generation})
	if e != nil {
		return nil, e
	}
	result, e := s.ledger.CreateDeleteOperation(ctx, &data.QuotaDeleteInput{TenantID: p.TenantID, ActorType: string(p.Type), ActorID: actorID, OwnerService: original.OwnerService, Action: InferenceDeleteAction, IdempotencyKey: key, RequestHash: hash, CanonicalRequest: string(raw), ResourceID: resource})
	if e != nil {
		return nil, e
	}
	out := &commandv1.GpuDeleteAcceptance{DeleteOperationId: result.OperationID, ResourceId: result.ResourceID, DispatchOperationId: result.OperationID, Result: "QUEUED_FOR_OWNER"}
	if result.LocalCanceled {
		out.Result = "CANCELED_BEFORE_DISPATCH"
		out.DispatchOperationId = ""
	}
	if s.worker != nil {
		s.worker.Notify()
	}
	return out, nil
}
