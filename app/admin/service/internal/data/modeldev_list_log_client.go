package data

import (
	"context"
	"unicode/utf8"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/grpc"
)

// LogSelection is one already-authorized original Pod UID. An absent container keeps the
// source default. The log ID stays an opaque owner-issued UUID.
type LogSelection struct {
	LogID     string
	PodUID    string
	Container string
	Previous  bool
}

// ExecutionFilter narrows the execution list before the owner counts or pages.
// Nil fields mean "no filter", which is not the same as asking for UNSPECIFIED.
type ExecutionFilter struct {
	ComputeState  *modeldevv1.ComputeState
	DeliveryState *modeldevv1.DeliveryState
	CloseState    *modeldevv1.CloseState
	// Name is an already validated substring; the owner folds case.
	Name *string
	// Kind is the task type the caller asked for; the owner rejects the enum
	// placeholder rather than treating it as "no filter".
	Kind *trainingv1.ExecutionKind
}

func (c *ModelDevClient) ListExecutions(ctx context.Context, scope ModelDevResolveScope, page, pageSize uint32, filter ExecutionFilter) (*modeldevv1.ListExecutionsResponse, error) {
	if page == 0 || pageSize == 0 || pageSize > 100 {
		return nil, modelDevQueryUnavailable()
	}
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_ListExecutions_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	request := &modeldevv1.ListExecutionsRequest{
		Page:          &modeldevv1.PageRequest{Page: &page, PageSize: &pageSize},
		ComputeState:  filter.ComputeState,
		DeliveryState: filter.DeliveryState,
		CloseState:    filter.CloseState,
		Name:          filter.Name,
		Kind:          filter.Kind,
	}
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).ListExecutions(call, request, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || len(out.Executions) > int(pageSize) {
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

func (c *ModelDevClient) GetExecutionLogs(ctx context.Context, scope ModelDevResolveScope, id string, tail, maxBytes uint32, selection LogSelection) (*modeldevv1.GetExecutionLogsResponse, error) {
	if tail == 0 {
		tail = 200
	}
	if maxBytes == 0 {
		maxBytes = 16384
	}
	if !modelDevCanonicalUUID(id) || tail > 1000 || maxBytes > 65536 || !modelDevValidLogSelection(selection) {
		return nil, modelDevQueryUnavailable()
	}
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_GetExecutionLogs_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	request := &modeldevv1.GetExecutionLogsRequest{ExecutionId: id, TailLines: tail, MaxBytes: maxBytes}
	if selection.LogID != "" {
		request.LogId = &selection.LogID
	}
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
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).GetExecutionLogs(call, request, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || out.Source == nil || !modelDevCanonicalUUID(out.Source.LogId) || out.Source.ResourceUid == "" ||
		!modelDevValidTimestamp(out.ObservedAt) || len(out.Lines) > int(tail) || !modelDevValidLogSource(out, selection) {
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

// modelDevValidLogSelection rejects a caller-supplied selector that the owner
// would not accept. Only the container and the previous flag are free text; a
// pod UID is always a canonical UUID issued by the pods response.
func modelDevValidLogSelection(selection LogSelection) bool {
	if !modelDevCanonicalUUID(selection.PodUID) {
		return false
	}
	if len(selection.Container) > 63 || !utf8.ValidString(selection.Container) {
		return false
	}
	for _, character := range selection.Container {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

// modelDevValidLogSource binds the response back to the plane that produced it.
// A step container is `main` unless a step declared its own; a worker container
// is `node` by contract, so the BFF cannot be handed a foreign container name.
func modelDevValidLogSource(out *modeldevv1.GetExecutionLogsResponse, selection LogSelection) bool {
	if out.Source == nil || out.Source.ResourceUid != selection.PodUID || selection.LogID != "" && selection.LogID != out.Source.LogId {
		return false
	}
	switch out.SourceKind {
	case modeldevv1.LogSourceKind_LOG_SOURCE_KIND_TRAINER:
		// Training workers only ever publish a bounded tail of the fixed
		// worker container; archived tails keep the same identity.
		if out.Source.ContainerName == "" || out.ContainerName != out.Source.ContainerName {
			return false
		}
		if selection.Container != "" && selection.Container != out.ContainerName {
			return false
		}
		if out.Previous != selection.Previous {
			return false
		}
		if selection.LogID != "" && selection.LogID != out.Source.LogId {
			return false
		}
	case modeldevv1.LogSourceKind_LOG_SOURCE_KIND_KFP_STEP:
		if !modelDevCanonicalUUID(out.Source.LogId) || out.ContainerName == "" || out.ContainerName != out.Source.ContainerName {
			return false
		}
		if selection.PodUID == "" || selection.PodUID != out.Source.ResourceUid {
			return false
		}
		if selection.Container != "" && selection.Container != out.ContainerName {
			return false
		}
		if out.Previous != selection.Previous {
			return false
		}
	default:
		return false
	}
	return true
}
