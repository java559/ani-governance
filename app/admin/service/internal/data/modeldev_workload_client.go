package data

import (
	"context"
	"unicode"
	"unicode/utf8"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"google.golang.org/grpc"
)

// WorkloadLayer is the plane a pods query selects. The two planes are never
// merged into one dishonest list; the caller picks, and ALL means both.
type WorkloadLayer string

const (
	WorkloadLayerAll      WorkloadLayer = "all"
	WorkloadLayerTrainer  WorkloadLayer = "trainer"
	WorkloadLayerPipeline WorkloadLayer = "pipeline"
)

func ParseWorkloadLayer(value string) (WorkloadLayer, bool) {
	switch value {
	case "", string(WorkloadLayerAll):
		return WorkloadLayerAll, true
	case string(WorkloadLayerTrainer):
		return WorkloadLayerTrainer, true
	case string(WorkloadLayerPipeline):
		return WorkloadLayerPipeline, true
	}
	return "", false
}

// EventScope selects how much of the event stream a page shows. `key` is the
// default because a driver Pod's Normal events would otherwise bury failures.
type EventScope string

const (
	EventScopeKey     EventScope = "key"
	EventScopeAll     EventScope = "all"
	EventScopeWarning EventScope = "warning"
)

func ParseEventScope(value string) (EventScope, bool) {
	switch value {
	case "", string(EventScopeKey):
		return EventScopeKey, true
	case string(EventScopeAll):
		return EventScopeAll, true
	case string(EventScopeWarning):
		return EventScopeWarning, true
	}
	return "", false
}

func (c *ModelDevClient) ListExecutionPods(ctx context.Context, scope ModelDevResolveScope, id string, layer WorkloadLayer, includeRecycled bool) (*modeldevv1.ListExecutionPodsResponse, error) {
	if !modelDevCanonicalUUID(id) {
		return nil, modelDevQueryUnavailable()
	}
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_ListExecutionPods_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	request := &modeldevv1.ListExecutionPodsRequest{ExecutionId: id}
	switch layer {
	case WorkloadLayerTrainer:
		request.Layer = modeldevv1.PodLayer_POD_LAYER_TRAINER
	case WorkloadLayerPipeline:
		request.Layer = modeldevv1.PodLayer_POD_LAYER_PIPELINE
	default:
		request.Layer = modeldevv1.PodLayer_POD_LAYER_ALL
	}
	request.IncludeRecycled = includeRecycled
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).ListExecutionPods(call, request, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || !modelDevValidTimestamp(out.ObservedAt) || len(out.Pods) > 512 {
		return nil, modelDevQueryUnavailable()
	}
	seen := make(map[string]bool, len(out.Pods))
	for _, pod := range out.Pods {
		if !modelDevValidPodView(pod, layer) || seen[pod.PodUid] {
			return nil, modelDevQueryUnavailable()
		}
		seen[pod.PodUid] = true
	}
	return out, nil
}

// modelDevValidPodView rejects a Pod the public DTO cannot describe honestly:
// a synthetic phase, an unknown availability, or a layer the request excluded.
func modelDevValidPodView(pod *modeldevv1.PodView, layer WorkloadLayer) bool {
	if pod == nil || !modelDevCanonicalUUID(pod.PodUid) || pod.PodName == "" || len(pod.PodName) > 253 ||
		!modelDevPodPhase(pod.Phase) || !modelDevPodAvailability(pod.Availability) || !modelDevPodLayer(pod.Layer) ||
		!modelDevOptionalTimestamp(pod.CreatedAt) || !modelDevOptionalTimestamp(pod.StartTime) || !modelDevOptionalTimestamp(pod.DeletionTimestamp) ||
		pod.Restarts < 0 || pod.Attempt < 0 || pod.JobIndex < 0 || (pod.LogId != "" && !modelDevCanonicalUUID(pod.LogId)) ||
		len(pod.Containers) > 64 || len(pod.InitContainers) > 64 || len(pod.EphemeralContainers) > 64 {
		return false
	}
	if layer == WorkloadLayerTrainer && pod.Layer != modeldevv1.PodLayer_POD_LAYER_TRAINER {
		return false
	}
	if layer == WorkloadLayerPipeline && pod.Layer != modeldevv1.PodLayer_POD_LAYER_PIPELINE {
		return false
	}
	for _, group := range [][]*modeldevv1.ContainerView{pod.Containers, pod.InitContainers, pod.EphemeralContainers} {
		for _, container := range group {
			if !modelDevValidContainerView(container) {
				return false
			}
		}
	}
	return true
}

func modelDevValidContainerView(container *modeldevv1.ContainerView) bool {
	if container == nil || container.Name == "" || len(container.Name) > 63 || !modelDevContainerName(container.Name) {
		return false
	}
	switch container.Type {
	case "container", "init_container", "ephemeral":
	default:
		return false
	}
	switch container.State {
	case "running", "waiting", "terminated", "unknown":
	default:
		return false
	}
	if container.RestartCount < 0 || !modelDevOptionalTimestamp(container.StartedAt) || !modelDevOptionalTimestamp(container.FinishedAt) {
		return false
	}
	if container.LastState != nil && !modelDevValidContainerView(container.LastState) {
		return false
	}
	return true
}

func modelDevContainerName(value string) bool {
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

// modelDevPodPhase accepts only a real Kubernetes phase. Recycled and
// never-created are availability evidence and must not be written here.
func modelDevPodPhase(value string) bool {
	switch value {
	case "", "Pending", "Running", "Succeeded", "Failed", "Unknown":
		return true
	}
	return false
}

func modelDevPodLayer(value modeldevv1.PodLayer) bool {
	return value == modeldevv1.PodLayer_POD_LAYER_TRAINER || value == modeldevv1.PodLayer_POD_LAYER_PIPELINE
}

func modelDevPodAvailability(value modeldevv1.PodAvailability) bool {
	switch value {
	case modeldevv1.PodAvailability_POD_AVAILABILITY_LIVE, modeldevv1.PodAvailability_POD_AVAILABILITY_RECYCLED,
		modeldevv1.PodAvailability_POD_AVAILABILITY_NEVER_CREATED, modeldevv1.PodAvailability_POD_AVAILABILITY_REJECTED,
		modeldevv1.PodAvailability_POD_AVAILABILITY_ORPHANED, modeldevv1.PodAvailability_POD_AVAILABILITY_CREATION_PENDING:
		return true
	}
	return false
}

func (c *ModelDevClient) ListExecutionEvents(ctx context.Context, scope ModelDevResolveScope, id string, eventScope EventScope) (*modeldevv1.ListExecutionEventsResponse, error) {
	if !modelDevCanonicalUUID(id) {
		return nil, modelDevQueryUnavailable()
	}
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_ListExecutionEvents_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	request := &modeldevv1.ListExecutionEventsRequest{ExecutionId: id}
	switch eventScope {
	case EventScopeAll, EventScopeWarning:
		request.Scope = string(eventScope)
	default:
		request.Scope = string(EventScopeKey)
	}
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).ListExecutionEvents(call, request, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || !modelDevValidTimestamp(out.ObservedAt) || len(out.Events) > 1000 ||
		len(out.RetentionHint) > 16 {
		return nil, modelDevQueryUnavailable()
	}
	for _, event := range out.Events {
		if !modelDevValidEventView(event) {
			return nil, modelDevQueryUnavailable()
		}
	}
	return out, nil
}

func modelDevValidEventView(event *modeldevv1.EventView) bool {
	if event == nil || (event.Type != "Normal" && event.Type != "Warning") || event.Reason == "" || event.Count < 0 ||
		!modelDevValidTimestamp(event.FirstTimestamp) || !modelDevValidTimestamp(event.LastTimestamp) || event.Message == "" ||
		!modelDevBoundedText(event.Reason, 128) || !modelDevBoundedText(event.Message, 4096) || !modelDevBoundedText(event.Source, 253) ||
		!modelDevBoundedText(event.InvolvedKind, 64) || !modelDevBoundedText(event.InvolvedName, 253) ||
		!modelDevBoundedText(event.FieldPath, 512) || !modelDevBoundedText(event.ReportingInstance, 128) {
		return false
	}
	if event.InvolvedUid != "" && !modelDevCanonicalUUID(event.InvolvedUid) {
		return false
	}
	if event.LastTimestamp.AsTime().Before(event.FirstTimestamp.AsTime()) {
		return false
	}
	return true
}

func modelDevBoundedText(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\t' {
			return false
		}
	}
	return true
}
