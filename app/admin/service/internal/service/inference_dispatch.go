package service

import (
	"context"
	"encoding/json"

	contractv1 "github.com/liangzai006/ani-inference-service/api/inference/contract/v1"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (b *InferenceGpuBinding) dispatchManaged(ctx context.Context, command *QuotaDispatchCommand) ([]byte, error) {
	facts, e := validateDispatchCommand(commandcontract.InferenceOwner, command)
	if e != nil {
		return nil, e
	}
	if b == nil || b.client == nil {
		return nil, status.Error(codes.Unavailable, "INFERENCE_ADAPTER_UNAVAILABLE")
	}
	trusted, ok := b.client.(inferenceTrustedOwnerClient)
	if !ok {
		return nil, status.Error(codes.Unavailable, "INFERENCE_TRUSTED_OWNER_UNAVAILABLE")
	}
	actor := quotaActorString(command.Actor)
	if actor == "" {
		return nil, commandcontract.Invalid("invalid inference actor")
	}
	c := facts.Inference
	var reply *runtimev1.OperationResponse
	switch command.Action {
	case InferenceCreateAction:
		request, e := contractv1.DecodeBusinessPayload(c.BusinessPayload)
		if e != nil {
			return nil, e
		}
		request.RequestId = command.OperationID
		request.Context = &runtimev1.CommandContext{TenantId: command.ResourceTenantID, ResourceId: command.ResourceID, OperationId: command.OperationID, RequestDigest: c.CreateCommandDigest}
		reply, e = trusted.CreateTrusted(ctx, command.ResourceTenantID, actor, request)
		if e != nil {
			return nil, e
		}
	case InferenceDeleteAction:
		request := &runtimev1.DeleteInferenceServiceRequest{RequestId: command.OperationID, ResourceId: command.ResourceID, CreateOperationId: command.CreateOperationID, ExpectedGeneration: c.DeleteExpectedGeneration, ManagedDeleteScope: c.DeleteScope, Context: &runtimev1.CommandContext{TenantId: command.ResourceTenantID, ResourceId: command.ResourceID, OperationId: command.OperationID}}
		hash, e := contractv1.ManagedDeleteCommandHash(request, command.ResourceTenantID, command.Actor.Type, command.Actor.ID)
		if e != nil {
			return nil, e
		}
		request.Context.RequestDigest = hash
		reply, e = trusted.DeleteTrusted(ctx, command.ResourceTenantID, actor, request)
		if e != nil {
			return nil, e
		}
	default:
		return nil, commandcontract.Invalid("invalid inference command action")
	}
	if e = validateInferenceDurableAck(command, reply); e != nil {
		return nil, e
	}
	raw, e := json.Marshal(map[string]any{"operation_id": command.OperationID, "resource_id": command.ResourceID, "accepted": true})
	if e != nil {
		return nil, e
	}
	return raw, ValidateDurableOwnerAck(command, raw)
}
