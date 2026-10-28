package service

import (
	"context"
	stderrors "errors"
	"math"
	"regexp"
	"strconv"
	"sync"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	monitoring "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/monitoring/v1"
	target "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/target/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ModelDevMonitoringClient is the observability surface the training resource
// curve consumes. It is the same client the instance metrics path uses; the
// training target differs only in kind and Pod identity.
type ModelDevMonitoringClient interface {
	QueryRange(context.Context, string, string, *monitoring.QueryRangeRequest) (*monitoring.QueryRangeResponse, error)
}

const (
	// The observability contract projects CPU and memory from a recording rule
	// over cAdvisor, not from a direct cAdvisor read.
	modelDevMetricSource        = "PROMETHEUS_CADVISOR_PROJECTION"
	modelDevMetricRetentionHint = "24h"
	modelDevMetricDefaultStep   = 30
	// The source rejects above four concurrent calls per tenant. A first screen
	// (worker plus four steps) exceeds that, so the BFF waits its turn.
	modelDevMetricTenantConcurrency = 4
	// Container names are part of the external contract; renaming either
	// silently breaks the metric chain.
	modelDevWorkerContainer = "node"
	modelDevStepContainer   = "main"
)

// modelDevMetricCatalog is the training-side whitelist. The source registers
// only these two canonical records for the training kind.
var modelDevMetricCatalog = map[string]string{
	"cpu.usage_cores":          "cores",
	"memory.working_set_bytes": "bytes",
}

var modelDevMetricNamespace = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// modelDevMetricGate bounds concurrent observability calls per tenant in the
// BFF, turning a rejection into a short wait.
type modelDevMetricGate struct {
	mu    sync.Mutex
	slots map[uint32]chan struct{}
}

func (g *modelDevMetricGate) acquire(ctx context.Context, tenant uint32) (func(), error) {
	g.mu.Lock()
	if g.slots == nil {
		g.slots = make(map[uint32]chan struct{})
	}
	slot, ok := g.slots[tenant]
	if !ok {
		slot = make(chan struct{}, modelDevMetricTenantConcurrency)
		g.slots[tenant] = slot
	}
	g.mu.Unlock()
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type modelDevMetricQuery struct {
	start, end time.Time
	step       uint32
	codes      []string
}

func modelDevMetricScopeUnavailable() error {
	return kratoserrors.New(412, "MODELDEV_METRIC_SCOPE_UNAVAILABLE", "training metric scope unavailable")
}

func modelDevMetricResponseInvalid() error {
	return kratoserrors.New(502, "MODELDEV_METRIC_RESPONSE_INVALID", "invalid training metric response")
}

// modelDevParseMetricQuery applies the source's own hard bounds, so an
// impossible window is rejected before any owner or observability call.
func modelDevParseMetricQuery(in *modeldevv1.QueryExecutionMetricsRequest) (modelDevMetricQuery, error) {
	if in == nil || !modelDevQueryUUID(in.ExecutionId) || !modelDevQueryUUID(in.PodUid) || !modelDevQueryContainerName(in.Container) {
		return modelDevMetricQuery{}, modelDevQueryInvalid()
	}
	start, err := time.Parse(time.RFC3339Nano, in.Start)
	if err != nil {
		return modelDevMetricQuery{}, modelDevQueryInvalid()
	}
	end, err := time.Parse(time.RFC3339Nano, in.End)
	if err != nil {
		return modelDevMetricQuery{}, modelDevQueryInvalid()
	}
	step := uint32(modelDevMetricDefaultStep)
	if in.StepSeconds != nil {
		step = in.GetStepSeconds()
	}
	if step < 30 || step > 3600 || step%30 != 0 {
		return modelDevMetricQuery{}, modelDevQueryInvalid()
	}
	span := end.Sub(start)
	if !modelDevMetricTimestamp(start) || !modelDevMetricTimestamp(end) || end.Before(start) || span > 24*time.Hour ||
		end.After(time.Now()) || int64(span/time.Second)/int64(step)+1 > 3000 {
		return modelDevMetricQuery{}, modelDevQueryInvalid()
	}
	codes := in.MetricCodes
	if len(codes) < 1 || len(codes) > len(modelDevMetricCatalog) {
		return modelDevMetricQuery{}, modelDevQueryInvalid()
	}
	seen := make(map[string]bool, len(codes))
	for _, code := range codes {
		if seen[code] {
			return modelDevMetricQuery{}, modelDevQueryInvalid()
		}
		if _, ok := modelDevMetricCatalog[code]; !ok {
			return modelDevMetricQuery{}, modelDevQueryInvalid()
		}
		seen[code] = true
	}
	return modelDevMetricQuery{start: start.UTC(), end: end.UTC(), step: step, codes: codes}, nil
}

// modelDevQueryContainerName accepts only the Kubernetes container alphabet.
func modelDevQueryContainerName(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

// modelDevMetricTimestamp requires whole seconds: every point is a step
// multiple of the window start, and Prometheus evaluates at whole seconds.
func modelDevMetricTimestamp(value time.Time) bool {
	return value.Nanosecond() == 0
}

// modelDevMetricContainer pins each layer to its contracted container. A wrong
// name returns an empty curve downstream without an error.
func modelDevMetricContainer(layer modeldevcontractv1.PodLayer) (string, bool) {
	switch layer {
	case modeldevcontractv1.PodLayer_POD_LAYER_TRAINER:
		return modelDevWorkerContainer, true
	case modeldevcontractv1.PodLayer_POD_LAYER_PIPELINE:
		return modelDevStepContainer, true
	}
	return "", false
}

// QueryExecutionMetrics answers one Pod's CPU/memory curve inside a single
// execution. Callers page through Pods; an execution-wide aggregate is a
// frontend composition, not an owner promise.
func (s *ModelDevService) QueryExecutionMetrics(ctx context.Context, in *modeldevv1.QueryExecutionMetricsRequest) (*modeldevv1.QueryExecutionMetricsResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevExecutionMetricsPath)
	if err != nil {
		return nil, err
	}
	principal, err := auth.PrincipalFromContext(ctx)
	if err != nil || principal == nil || principal.ID == 0 || principal.TenantID == 0 {
		return nil, kratoserrors.Unauthorized("INVALID_LOGIN", "user login required")
	}
	query, err := modelDevParseMetricQuery(in)
	if err != nil {
		return nil, err
	}
	if s.monitoring == nil {
		return nil, kratoserrors.ServiceUnavailable("MODELDEV_METRIC_UNAVAILABLE", "training metrics unavailable")
	}
	pods, err := s.resolver.ListExecutionPods(ctx, scope, in.ExecutionId, data.WorkloadLayerAll, true)
	if err != nil {
		return nil, modelDevMetricFailure(err)
	}
	pod, ok := modelDevMetricPod(pods, in.PodUid)
	if !ok {
		return nil, kratoserrors.NotFound("RESOURCE_NOT_FOUND", "execution pod unavailable")
	}
	container, ok := modelDevMetricContainer(pod.GetLayer())
	if !ok || in.Container != container || !modelDevPodHasContainer(pod, in.Container) {
		return nil, modelDevQueryInvalid()
	}
	// The frozen acceptance is the tenant's own record of this execution: it
	// supplies the cluster and namespace, never the caller.
	original, err := s.acceptances.FindRegistrationSource(ctx, principal.TenantID, scope.ResourceTenantID, in.ExecutionId)
	if err != nil {
		return nil, modelDevMetricFailure(err)
	}
	if original == nil || original.ExecutionID != in.ExecutionId || original.Scope.TenantID != principal.TenantID || original.Scope.ResourceTenantID != scope.ResourceTenantID {
		return nil, modelDevMetricResponseInvalid()
	}
	environment := original.Snapshot.Environment
	if !modelDevQueryUUID(environment.ClusterID) || len(environment.NamespaceName) > 63 || !modelDevMetricNamespace.MatchString(environment.NamespaceName) {
		return nil, modelDevMetricScopeUnavailable()
	}
	deviceMode := original.Snapshot.ComputeOrigin.DeviceMode
	if deviceMode != "cpu" && deviceMode != "gpu" {
		return nil, modelDevMetricResponseInvalid()
	}
	// The Pod carries the instance it reports under, copied from the label the
	// admission webhook verified. Runtime history is only a fallback: it is
	// written asynchronously and a freshly started execution has none yet.
	instance := pod.GetMetricInstanceId()
	if !modelDevQueryUUID(instance) {
		if pod.GetLayer() != modeldevcontractv1.PodLayer_POD_LAYER_PIPELINE {
			instance = in.ExecutionId
		} else {
			execution, err := s.resolver.GetExecution(ctx, scope, in.ExecutionId)
			if err != nil {
				return nil, modelDevMetricFailure(err)
			}
			instance = execution.GetExecution().GetHistory().GetRunId()
		}
	}
	if !modelDevQueryUUID(instance) {
		return nil, modelDevMetricScopeUnavailable()
	}

	release, err := s.metricGate.acquire(ctx, principal.TenantID)
	if err != nil {
		return nil, modelDevMetricFailure(err)
	}
	defer release()
	start, end := timestamppb.New(query.start), timestamppb.New(query.end)
	reply, err := s.monitoring.QueryRange(ctx, scope.ResourceTenantID, scope.Actor, &monitoring.QueryRangeRequest{
		Target: &target.TargetScope{
			Kind: target.TargetKind_TARGET_KIND_TRAINING_EXECUTION, TenantId: scope.ResourceTenantID,
			InstanceId: instance, ClusterId: environment.ClusterID, Namespace: environment.NamespaceName,
			ContainerName: in.Container, PodUid: in.PodUid,
		},
		MetricCodes: query.codes, Start: start, End: end, StepSeconds: query.step,
	})
	if err != nil {
		return nil, modelDevMetricFailure(err)
	}
	if reply == nil || !proto.Equal(reply.Start, start) || !proto.Equal(reply.End, end) || reply.StepSeconds != query.step {
		return nil, modelDevMetricResponseInvalid()
	}
	count := int(query.end.Sub(query.start)/time.Second)/int(query.step) + 1
	series, err := modelDevMetricSeries(reply.Series, query.codes, query.start, query.step, count)
	if err != nil {
		return nil, err
	}
	return &modeldevv1.QueryExecutionMetricsResponse{
		ExecutionId: in.ExecutionId, PodUid: in.PodUid, PodName: pod.GetPodName(), Container: in.Container,
		Start: query.start.Format(time.RFC3339Nano), End: query.end.Format(time.RFC3339Nano), StepSeconds: query.step,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Source: modelDevMetricSource, RetentionHint: modelDevMetricRetentionHint,
		DeviceMode: deviceMode, GpuRequested: deviceMode == "gpu",
		// This environment has no GPU exporter series, so a requested GPU curve
		// is reported as unavailable rather than as zero usage.
		GpuAvailable: false,
		Series:       series,
	}, nil
}

func modelDevMetricPod(pods *modeldevcontractv1.ListExecutionPodsResponse, podUID string) (*modeldevcontractv1.PodView, bool) {
	for _, pod := range pods.GetPods() {
		if pod.GetPodUid() == podUID {
			return pod, true
		}
	}
	return nil, false
}

func modelDevPodHasContainer(pod *modeldevcontractv1.PodView, container string) bool {
	for _, current := range pod.GetContainers() {
		if current.GetName() == container {
			return true
		}
	}
	return false
}

// modelDevMetricSeries mirrors the source contract: one series per requested
// code, every point aligned to the requested step. A missing value carries an
// explicit gap reason, never a fabricated zero.
func modelDevMetricSeries(in []*monitoring.MetricSeries, codes []string, start time.Time, step uint32, count int) ([]*modeldevv1.ExecutionMetricSeries, error) {
	if len(in) != len(codes) {
		return nil, modelDevMetricResponseInvalid()
	}
	out := make([]*modeldevv1.ExecutionMetricSeries, 0, len(in))
	for i, series := range in {
		unit, ok := modelDevMetricCatalog[codes[i]]
		if !ok {
			return nil, modelDevMetricResponseInvalid()
		}
		if series == nil || series.MetricCode != codes[i] || series.Unit != unit || len(series.Points) != count {
			return nil, modelDevMetricResponseInvalid()
		}
		item := &modeldevv1.ExecutionMetricSeries{MetricCode: series.MetricCode, Unit: series.Unit, Points: make([]*modeldevv1.ExecutionMetricPoint, 0, count)}
		valid := 0
		for n, point := range series.Points {
			if point == nil || point.Timestamp == nil || point.Timestamp.CheckValid() != nil ||
				!modelDevMetricTimestamp(point.Timestamp.AsTime()) ||
				!point.Timestamp.AsTime().Equal(start.Add(time.Duration(n)*time.Duration(step)*time.Second)) {
				return nil, modelDevMetricResponseInvalid()
			}
			converted := &modeldevv1.ExecutionMetricPoint{Timestamp: point.Timestamp.AsTime().UTC().Format(time.RFC3339Nano)}
			if point.Value != nil {
				value := *point.Value
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || point.GapReason != monitoring.GapReason_GAP_REASON_UNSPECIFIED {
					return nil, modelDevMetricResponseInvalid()
				}
				encoded := strconv.FormatFloat(value, 'f', -1, 64)
				converted.Value = &encoded
				valid++
			} else {
				var reason string
				switch point.GapReason {
				case monitoring.GapReason_GAP_REASON_NO_DATA:
					reason = "NO_DATA"
				case monitoring.GapReason_GAP_REASON_AMBIGUOUS_SOURCE:
					reason = "AMBIGUOUS_SOURCE"
				case monitoring.GapReason_GAP_REASON_INVALID_VALUE:
					reason = "INVALID_VALUE"
				default:
					return nil, modelDevMetricResponseInvalid()
				}
				converted.GapReason = &reason
			}
			item.Points = append(item.Points, converted)
		}
		want := monitoring.SeriesStatus_SERIES_STATUS_NO_DATA
		item.Status = "NO_DATA"
		if valid == count {
			want = monitoring.SeriesStatus_SERIES_STATUS_OK
			item.Status = "OK"
		} else if valid > 0 {
			want = monitoring.SeriesStatus_SERIES_STATUS_PARTIAL
			item.Status = "PARTIAL"
		}
		if series.Status != want {
			return nil, modelDevMetricResponseInvalid()
		}
		out = append(out, item)
	}
	return out, nil
}

func modelDevMetricFailure(err error) error {
	switch {
	case stderrors.Is(err, data.ErrModelDevAuthorizationDenied):
		return kratoserrors.Forbidden("FORBIDDEN", "training metric forbidden")
	case stderrors.Is(err, data.ErrModelDevAcceptanceNotFound):
		return kratoserrors.NotFound("RESOURCE_NOT_FOUND", "execution unavailable")
	case stderrors.Is(err, context.Canceled), status.Code(err) == codes.Canceled:
		return kratoserrors.New(499, "MODELDEV_METRIC_CANCELED", "training metric canceled")
	case stderrors.Is(err, context.DeadlineExceeded), status.Code(err) == codes.DeadlineExceeded:
		return kratoserrors.New(504, "MODELDEV_METRIC_TIMEOUT", "training metric timed out")
	}
	switch status.Code(err) {
	case codes.NotFound, codes.PermissionDenied, codes.Unauthenticated:
		return kratoserrors.NotFound("RESOURCE_NOT_FOUND", "execution unavailable")
	case codes.InvalidArgument:
		return modelDevQueryInvalid()
	case codes.FailedPrecondition:
		return modelDevMetricScopeUnavailable()
	case codes.ResourceExhausted:
		return kratoserrors.New(429, "MODELDEV_METRIC_CONCURRENCY_LIMIT", "training metric concurrency limit exceeded")
	default:
		return kratoserrors.ServiceUnavailable("MODELDEV_METRIC_UNAVAILABLE", "training metrics unavailable")
	}
}
