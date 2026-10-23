package data

import (
	"context"
	"strings"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AcceptExecution delivers the persisted Governance original to its owner.
// A bounded attempt never resolves new defaults or renews the frozen deadline.
func (c *ModelDevClient) AcceptExecution(ctx context.Context, envelope cpup01.AdmissionEnvelope) (ModelDevOwnerReceipt, error) {
	if err := ctx.Err(); err != nil {
		return ModelDevOwnerReceipt{}, err
	}
	request, err := modelDevDispatchRequest(envelope)
	if contextErr := ctx.Err(); contextErr != nil {
		return ModelDevOwnerReceipt{}, contextErr
	}
	if err != nil {
		return ModelDevOwnerReceipt{}, err
	}
	if c == nil || c.connection == nil || c.timeout <= 0 {
		return ModelDevOwnerReceipt{}, &ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"}
	}
	callContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	requestID := uuid.NewString()
	callContext = metadata.NewOutgoingContext(callContext, metadata.Pairs(
		"x-ani-tenant-id", envelope.TenantID, "x-ani-actor", envelope.Actor, "x-ani-request-id", requestID,
	))
	response, err := modeldevv1.NewModelDevCommandServiceClient(c.connection).AcceptExecution(callContext, request, grpc.WaitForReady(true))
	if contextErr := ctx.Err(); contextErr != nil {
		return ModelDevOwnerReceipt{}, contextErr
	}
	if callContext.Err() != nil {
		return ModelDevOwnerReceipt{}, &ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"}
	}
	if err != nil {
		return ModelDevOwnerReceipt{}, modelDevDispatchError(err, requestID)
	}
	if response == nil || response.Identity == nil || response.States == nil ||
		len(response.ProtoReflect().GetUnknown()) != 0 || len(response.Identity.ProtoReflect().GetUnknown()) != 0 ||
		len(response.States.ProtoReflect().GetUnknown()) != 0 {
		return ModelDevOwnerReceipt{}, &ModelDevDeliveryFailure{Code: "INVALID_ACK"}
	}
	receipt := ModelDevOwnerReceipt{
		OperationID: response.Identity.OperationId, ExecutionID: response.Identity.ExecutionId, ExecutionSpecHash: response.Identity.ExecutionSpecHash,
		ComputeState:  strings.TrimPrefix(response.States.ComputeState.String(), "COMPUTE_STATE_"),
		DeliveryState: strings.TrimPrefix(response.States.DeliveryState.String(), "DELIVERY_STATE_"),
		ResourceState: strings.TrimPrefix(response.States.ResourceState.String(), "RESOURCE_STATE_"),
		CloseState:    strings.TrimPrefix(response.States.CloseState.String(), "CLOSE_STATE_"),
		Revision:      response.Revision, Replayed: response.Replayed,
	}
	validationErr := validateModelDevOwnerReceipt(receipt, envelope)
	if contextErr := ctx.Err(); contextErr != nil {
		return ModelDevOwnerReceipt{}, contextErr
	}
	if validationErr != nil {
		return ModelDevOwnerReceipt{}, &ModelDevDeliveryFailure{Code: "INVALID_ACK"}
	}
	return receipt, nil
}

func modelDevDispatchRequest(envelope cpup01.AdmissionEnvelope) (*modeldevv1.AcceptExecutionRequest, error) {
	invalid := &ModelDevDeliveryFailure{Code: "INVALID_COMMAND", Permanent: true}
	if _, _, err := envelope.CanonicalPayloads(); err != nil || !validModelDevClientActor(envelope.Actor) || envelope.Snapshot.DeadlineAt.Nanosecond()%1000 != 0 {
		return nil, invalid
	}
	for _, value := range []string{envelope.TenantID, envelope.OperationID, envelope.ExecutionID} {
		if !modelDevCanonicalUUID(value) {
			return nil, invalid
		}
	}
	intent, err := contractpb.EncodeIntent(envelope.Intent)
	if err != nil {
		return nil, invalid
	}
	snapshot, err := contractpb.EncodeSnapshot(envelope.Snapshot)
	if err != nil {
		return nil, invalid
	}
	return &modeldevv1.AcceptExecutionRequest{
		Identity:         &trainingv1.ExecutionIdentity{OperationId: envelope.OperationID, ExecutionId: envelope.ExecutionID, ExecutionSpecHash: envelope.SpecHash},
		ResourceTenantId: envelope.TenantID, AdmittedActorId: envelope.Actor, IntentHash: envelope.IntentHash,
		Intent: intent, Snapshot: snapshot, AcceptedAt: timestamppb.New(envelope.AcceptedAt),
	}, nil
}

// Only the authenticated owner's clean, correlated contract failures can end
// retries. Remote text, ambiguous details and infrastructure codes are discarded.
func modelDevDispatchError(err error, requestID string) error {
	unavailable := &ModelDevDeliveryFailure{Code: "OWNER_UNAVAILABLE"}
	failure, ok := status.FromError(err)
	if !ok {
		return unavailable
	}
	wire := failure.Proto()
	if len(wire.ProtoReflect().GetUnknown()) != 0 || len(wire.Details) != 1 || wire.Details[0] == nil || len(wire.Details[0].ProtoReflect().GetUnknown()) != 0 {
		return unavailable
	}
	details := failure.Details()
	if len(details) != 1 {
		return unavailable
	}
	detail, ok := details[0].(*modeldevv1.ErrorDetail)
	if !ok || detail == nil || len(detail.ProtoReflect().GetUnknown()) != 0 || len(detail.Violations) != 0 || detail.CorrelationId != requestID {
		return unavailable
	}
	switch {
	case failure.Code() == codes.InvalidArgument && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_INVALID_ARGUMENT:
		return &ModelDevDeliveryFailure{Code: "INVALID_COMMAND", Permanent: true}
	case failure.Code() == codes.AlreadyExists && detail.Reason == modeldevv1.ErrorReason_ERROR_REASON_COMMAND_CONFLICT:
		return &ModelDevDeliveryFailure{Code: "COMMAND_CONFLICT", Permanent: true}
	default:
		return unavailable
	}
}
