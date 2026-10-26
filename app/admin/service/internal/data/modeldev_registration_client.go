package data

import (
	"context"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ModelDevResumeModelRegistrationPath = "/api/v1/modeldev/executions/{execution_id}:resume-model-registration"

// Resume sends one request for the original execution. An unknown outcome must
// be read back by execution ID; this client never generates a registration key
// or automatically repeats the command.
func (c *ModelDevClient) ResumeModelRegistration(ctx context.Context, scope ModelDevResolveScope, id string) (*modeldevv1.GetExecutionResponse, error) {
	if !modelDevCanonicalUUID(id) {
		return nil, status.Error(codes.InvalidArgument, "invalid original execution")
	}
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevOperationsService_ResumeModelRegistration_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevOperationsServiceClient(c.connection).ResumeModelRegistration(call, &modeldevv1.ResumeModelRegistrationRequest{ExecutionId: id}, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevRegistrationError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || !modelDevValidExecution(out.Execution) || out.Execution.Identity.ExecutionId != id || out.Execution.ModelRegistration == nil || out.Execution.States.CloseState != modeldevv1.CloseState_CLOSE_STATE_CLOSED {
		return nil, modelDevQueryUnavailable()
	}
	return out, nil
}

func modelDevRegistrationError(err error) error {
	switch status.Code(err) {
	case codes.Canceled:
		return status.Error(codes.Canceled, "model registration canceled")
	case codes.DeadlineExceeded:
		return status.Error(codes.DeadlineExceeded, "model registration timed out")
	case codes.NotFound:
		return status.Error(codes.NotFound, "execution unavailable")
	case codes.PermissionDenied, codes.Unauthenticated:
		return status.Error(codes.PermissionDenied, "execution unavailable")
	case codes.FailedPrecondition:
		return status.Error(codes.FailedPrecondition, "model registration unavailable")
	case codes.InvalidArgument:
		return status.Error(codes.InvalidArgument, "invalid original execution")
	default:
		return status.Error(codes.Unavailable, "model registration unavailable")
	}
}
