package service

import (
	"context"

	"github.com/go-kratos/kratos/v2/errors"
	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/grpc"
)

// ExecutionLogSession carries only the authenticated tenant and the protected
// owner stream. The HTTP handler owns the bounded lifetime and cancellation.
type ExecutionLogSession struct {
	TenantID string
	Stream   grpc.ServerStreamingClient[modeldevcontractv1.GetExecutionLogStreamResponse]
}

// OpenExecutionLogStream authorizes the live training log channel. The caller
// has already resolved the execution through the same current grant the tail
// read uses; a stream never widens that authorization.
func (s *ModelDevService) OpenExecutionLogStream(ctx context.Context, in *modeldevcontractv1.GetExecutionLogStreamRequest) (*ExecutionLogSession, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevExecutionLogStreamPath)
	if err != nil {
		return nil, err
	}
	if in == nil || !modelDevQueryUUID(in.ExecutionId) ||
		(in.PodUid != nil && !modelDevQueryUUID(in.GetPodUid())) || !modelDevQueryContainer(in.Container) ||
		(in.SinceBytes != nil && (*in.SinceBytes < 0 || *in.SinceBytes > 1<<30)) {
		return nil, errors.BadRequest("INVALID_MODELDEV_QUERY", "invalid modeldev query")
	}
	// A live stream needs an existing Pod; the implicit choice may only use a
	// live TRAINER row, never a recycled one.
	if in.PodUid == nil {
		if in.Container != nil {
			return nil, errors.BadRequest("INVALID_MODELDEV_QUERY", "invalid modeldev query")
		}
		podUID, _, err := s.modelDevDefaultLogPod(ctx, scope, in.ExecutionId, false)
		if err != nil {
			return nil, err
		}
		in.PodUid = &podUID
	}
	selection := data.LogSelection{PodUID: in.GetPodUid(), Container: in.GetContainer(), Previous: in.GetPrevious()}
	stream, err := s.resolver.OpenExecutionLogStream(ctx, scope, in.ExecutionId, selection, in.GetSinceBytes())
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	if stream == nil {
		return nil, errors.ServiceUnavailable("MODELDEV_QUERY_UNAVAILABLE", "modeldev query unavailable")
	}
	return &ExecutionLogSession{TenantID: scope.ResourceTenantID, Stream: stream}, nil
}
