package service

import (
	"context"
	"encoding/json"
	"strconv"

	commandv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/command/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	quota "go-wind-admin/api/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/pkg/middleware/auth"
)

func (s *GpuAcceptance) acceptInstanceCPUDelete(ctx context.Context, p *auth.Principal, key, resource string, version int64, original *ent.QuotaOperation) (*commandv1.GpuDeleteAcceptance, error) {
	b, ok := s.binding.(*InstanceGpuBinding)
	if !ok || version < 1 || !data.AllowsInstanceCPUCommand(original.OwnerService, original.Action, original.CanonicalRequest) {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid original CPU instance command")
	}
	c, e := data.DecodeInstanceCPUCanonical([]byte(original.CanonicalRequest))
	if e != nil {
		return nil, e
	}
	charges, e := s.ledger.GetChargesForOperation(ctx, p.TenantID, original.OperationID)
	if e != nil {
		return nil, e
	}
	if len(charges) != 0 {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "CPU instance cannot carry GPU charges")
	}
	actorID := strconv.FormatUint(uint64(p.ID), 10)
	hash, e := gpuHash(map[string]any{"schema": "gov-instance-cpu-delete-v1", "tenant_id": original.ResourceTenantID, "actor_type": string(p.Type), "actor_id": actorID, "owner": InstanceOwner, "action": InstanceDeleteAction, "resource_id": resource, "create_operation_id": original.OperationID, "expected_version": version})
	if e != nil {
		return nil, e
	}
	old, e := s.ledger.FindIdempotentOperation(ctx, p.TenantID, string(p.Type), actorID, InstanceDeleteAction, key)
	if e != nil && !ent.IsNotFound(e) {
		return nil, e
	}
	if old == nil {
		if e = b.ValidateGpuDelete(ctx, p, original.ResourceTenantID, resource, version); e != nil {
			return nil, e
		}
	}
	c.DeleteExpectedVersion = version
	raw, e := data.MarshalInstanceCPUCanonical(c)
	if e != nil {
		return nil, e
	}
	result, e := s.ledger.CreateDeleteOperation(ctx, &data.QuotaDeleteInput{TenantID: p.TenantID, ActorType: string(p.Type), ActorID: actorID, OwnerService: InstanceOwner, Action: InstanceDeleteAction, IdempotencyKey: key, RequestHash: hash, CanonicalRequest: string(raw), ResourceID: resource})
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

func (b *InstanceGpuBinding) dispatchCPU(ctx context.Context, cmd *QuotaDispatchCommand) ([]byte, error) {
	if cmd == nil || len(cmd.Charges) != 0 || !data.AllowsInstanceCPUCommand(InstanceOwner, cmd.Action, string(cmd.CanonicalRequest)) || cmd.Actor.Type != "user" || cmd.Actor.ID == "" || cmd.RequestHash == "" {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid CPU instance command")
	}
	frozen, e := data.DecodeInstanceCPUCanonical(cmd.CanonicalRequest)
	if e != nil {
		return nil, e
	}
	ctx = data.WithInstanceCommandActor(ctx, "governance:"+cmd.Actor.Type+":"+cmd.Actor.ID)
	commandContext := &res.CommandContext{TenantId: cmd.ResourceTenantID, ResourceId: cmd.ResourceID, OperationId: cmd.OperationID}
	var reply *res.OperationResponse
	switch cmd.Action {
	case InstanceCreateAction:
		if cmd.CreateOperationID != "" {
			return nil, quota.ErrorInvalidQuotaRequest("%s", "CPU create origin mismatch")
		}
		business, e := res.DecodeBusinessPayload(frozen.BusinessPayload)
		if e != nil {
			return nil, e
		}
		commandContext.RequestDigest, e = res.CommandDigest(commandContext, business)
		if e != nil {
			return nil, e
		}
		reply, e = b.client.Create(ctx, &res.AcceptCreateRequest{Context: commandContext, Spec: business})
		if e != nil {
			return nil, e
		}
	case InstanceDeleteAction:
		if cmd.CreateOperationID == "" {
			return nil, quota.ErrorInvalidQuotaRequest("%s", "CPU delete origin missing")
		}
		request := &res.AcceptDeleteRequest{Context: commandContext, CreateOperationId: cmd.CreateOperationID, ExpectedVersion: frozen.DeleteExpectedVersion, InstanceKind: "container", ComputeType: "cpu"}
		commandContext.RequestDigest, e = res.DeleteCommandDigest(request)
		if e != nil {
			return nil, e
		}
		reply, e = b.client.Delete(ctx, request)
		if e != nil {
			return nil, e
		}
	default:
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid CPU instance action")
	}
	if reply == nil || reply.DurableOwnerAck == nil || !reply.DurableOwnerAck.Accepted || reply.DurableOwnerAck.OperationId != cmd.OperationID || reply.DurableOwnerAck.ResourceId != cmd.ResourceID {
		return nil, quota.ErrorInvalidQuotaRequest("%s", "invalid instance durable ACK")
	}
	raw, e := json.Marshal(map[string]any{"operation_id": cmd.OperationID, "resource_id": cmd.ResourceID, "accepted": true})
	if e == nil {
		e = ValidateDurableOwnerAck(cmd, raw)
	}
	return raw, e
}
