package service

import (
	"context"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	commandv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/command/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/pkg/middleware/auth"
	"strconv"
)

func (s *GpuAcceptance) acceptVMDelete(ctx context.Context, p *auth.Principal, key, resource string, version int64, original *ent.QuotaOperation, replayed *bool) (*commandv1.GpuDeleteAcceptance, error) {
	binding, ok := s.binding.(*VMBinding)
	if !ok || version < 1 || original.Action != commandcontract.VMCreateAction {
		return nil, commandcontract.Invalid("invalid original VM command")
	}
	frozen, err := commandcontract.DecodeVMCanonical([]byte(original.CanonicalRequest))
	if err != nil {
		return nil, err
	}
	charges, err := s.ledger.GetChargesForOperation(ctx, p.TenantID, original.OperationID)
	if err != nil {
		return nil, err
	}
	if len(charges) != 0 {
		return nil, commandcontract.Invalid("VM command cannot carry charges")
	}
	actorID := strconv.FormatUint(uint64(p.ID), 10)
	hash, err := gpuHash(map[string]any{"owner": InstanceOwner, "action": commandcontract.VMDeleteAction, "tenant_id": original.ResourceTenantID, "actor_type": string(p.Type), "actor_id": actorID, "resource_id": resource, "create_operation_id": original.OperationID, "expected_version": version})
	if err != nil {
		return nil, err
	}
	old, err := s.ledger.FindIdempotentOperation(ctx, p.TenantID, string(p.Type), actorID, commandcontract.VMDeleteAction, key)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if old == nil {
		deleting, e := s.ledger.HasAcceptedVMDelete(ctx, p.TenantID, resource)
		if e != nil {
			return nil, e
		}
		if deleting {
			return nil, kratoserrors.Conflict("VM_OPERATION_CONFLICT", "VM final deletion already accepted")
		}
		if err = binding.ValidateGpuDelete(ctx, p, original.ResourceTenantID, resource, version); err != nil {
			return nil, err
		}
	}
	frozen.DeleteExpectedVersion = version
	raw, err := commandcontract.MarshalVMCanonical(frozen)
	if err != nil {
		return nil, err
	}
	result, err := s.ledger.CreateDeleteOperation(ctx, &data.QuotaDeleteInput{TenantID: p.TenantID, ActorType: string(p.Type), ActorID: actorID, OwnerService: InstanceOwner, Action: commandcontract.VMDeleteAction, IdempotencyKey: key, RequestHash: hash, CanonicalRequest: string(raw), ResourceID: resource})
	if err != nil {
		return nil, err
	}
	if replayed != nil {
		*replayed = result.Replayed
	}
	out := &commandv1.GpuDeleteAcceptance{DeleteOperationId: result.OperationID, ResourceId: result.ResourceID, DispatchOperationId: result.OperationID, Result: "QUEUED_FOR_OWNER"}
	if result.LocalCanceled {
		out.Result = "CANCELED_BEFORE_DISPATCH"
		out.DispatchOperationId = ""
	}
	if result.OwnerRejected {
		out.Result = "REJECTED_VERSION_CONFLICT"
		out.DispatchOperationId = ""
	}
	if s.worker != nil {
		s.worker.Notify()
	}
	return out, nil
}
