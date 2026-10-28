package data

import (
	"context"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc"
)

// OpenExecutionLogStream opens the modeldev server stream for one container
// instance. The resume offset is the server-authoritative byte count the client
// last received; zero reads from the log head.
func (c *ModelDevClient) OpenExecutionLogStream(ctx context.Context, scope ModelDevResolveScope, id string, selection LogSelection, sinceBytes int64) (grpc.ServerStreamingClient[modeldevv1.GetExecutionLogStreamResponse], error) {
	if !modelDevCanonicalUUID(id) || !modelDevValidLogSelection(selection) || sinceBytes < 0 || sinceBytes > 1<<30 {
		return nil, modelDevQueryUnavailable()
	}
	call, err := c.streamContext(ctx, scope, modeldevv1.ModelDevQueryService_GetExecutionLogStream_FullMethodName)
	if err != nil {
		return nil, err
	}
	request := &modeldevv1.GetExecutionLogStreamRequest{ExecutionId: id}
	if selection.PodUID != "" {
		request.PodUid = &selection.PodUID
	}
	if selection.Container != "" {
		request.Container = &selection.Container
	}
	if selection.Previous {
		previous := true
		request.Previous = &previous
	}
	if sinceBytes > 0 {
		since := sinceBytes
		request.SinceBytes = &since
	}
	stream, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).GetExecutionLogStream(call, request)
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if stream == nil {
		return nil, modelDevQueryUnavailable()
	}
	return stream, nil
}
