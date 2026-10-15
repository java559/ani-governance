package data

import (
	"context"
	"unicode/utf8"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc"
)

func (c *ModelDevClient) ListExecutions(ctx context.Context, scope ModelDevResolveScope, size uint32, token string) (*modeldevv1.ListExecutionsResponse, error) {
	if size == 0 {
		size = 20
	}
	if size > 100 || len(token) > 2048 {
		return nil, modelDevQueryUnavailable()
	}
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_ListExecutions_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).ListExecutions(call, &modeldevv1.ListExecutionsRequest{Page: &modeldevv1.PageRequest{Limit: size, Cursor: token}}, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || len(out.Executions) > int(size) || len(out.NextCursor) > 2048 {
		return nil, modelDevQueryUnavailable()
	}
	seen := make(map[string]bool, len(out.Executions))
	for _, execution := range out.Executions {
		if !modelDevValidExecution(execution) || seen[execution.GetIdentity().GetExecutionId()] {
			return nil, modelDevQueryUnavailable()
		}
		seen[execution.Identity.ExecutionId] = true
	}
	return out, nil
}

func (c *ModelDevClient) GetExecutionLogs(ctx context.Context, scope ModelDevResolveScope, id string, tail, maxBytes uint32) (*modeldevv1.GetExecutionLogsResponse, error) {
	if tail == 0 {
		tail = 200
	}
	if maxBytes == 0 {
		maxBytes = 16384
	}
	if !modelDevCanonicalUUID(id) || tail > 1000 || maxBytes > 65536 {
		return nil, modelDevQueryUnavailable()
	}
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).GetExecutionLogs(call, &modeldevv1.GetExecutionLogsRequest{ExecutionId: id, TailLines: tail, MaxBytes: maxBytes}, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || out.Source == nil || !modelDevCanonicalUUID(out.Source.LogId) || out.Source.ResourceUid == "" || out.Source.ContainerName != "node" || !modelDevValidTimestamp(out.ObservedAt) || len(out.Lines) > int(tail) {
		return nil, modelDevQueryUnavailable()
	}
	bytes := 0
	for _, line := range out.Lines {
		if line == nil || !modelDevValidTimestamp(line.Timestamp) || !utf8.ValidString(line.Text) {
			return nil, modelDevQueryUnavailable()
		}
		bytes += len(line.Text)
		if bytes > int(maxBytes) {
			return nil, modelDevQueryUnavailable()
		}
	}
	return out, nil
}
