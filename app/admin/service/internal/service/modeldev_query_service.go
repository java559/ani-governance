package service

import (
	"context"
	stderrors "errors"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *ModelDevService) modelDevQueryScope(ctx context.Context, path string) (data.ModelDevResolveScope, error) {
	principal, err := auth.PrincipalFromContext(ctx)
	if err != nil || principal == nil || principal.ID == 0 {
		return data.ModelDevResolveScope{}, errors.Unauthorized("INVALID_LOGIN", "user login required")
	}
	if principal.Type != auth.SubjectUser || principal.TenantID == 0 {
		return data.ModelDevResolveScope{}, errors.Forbidden("FORBIDDEN", "tenant user required")
	}
	if s == nil || s.authorization == nil || s.tenants == nil || s.resolver == nil {
		return data.ModelDevResolveScope{}, modelDevQueryFailure(nil)
	}
	if err := s.authorization.AuthorizeQuery(ctx, principal.TenantID, principal.ID, path); err != nil {
		return data.ModelDevResolveScope{}, modelDevQueryFailure(err)
	}
	tenant, err := s.tenants.ResourceTenantID(ctx, principal.TenantID)
	if err != nil || !modelDevQueryUUID(tenant) {
		return data.ModelDevResolveScope{}, modelDevQueryFailure(err)
	}
	actor, err := principal.Actor()
	if err != nil {
		return data.ModelDevResolveScope{}, errors.Forbidden("FORBIDDEN", "tenant user required")
	}
	return data.ModelDevResolveScope{ResourceTenantID: tenant, Actor: actor}, nil
}

func (s *ModelDevService) GetExecution(ctx context.Context, in *modeldevv1.GetExecutionRequest) (*modeldevv1.GetExecutionResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevGetExecutionPath)
	if err != nil {
		return nil, err
	}
	if in == nil || !modelDevQueryUUID(in.ExecutionId) {
		return nil, modelDevQueryInvalid()
	}
	out, err := s.resolver.GetExecution(ctx, scope, in.ExecutionId)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	return &modeldevv1.GetExecutionResponse{Execution: modelDevPublicExecution(out.Execution)}, nil
}

func (s *ModelDevService) ListExecutions(ctx context.Context, in *modeldevv1.ListExecutionsRequest) (*modeldevv1.ListExecutionsResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevListExecutionsPath)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, modelDevQueryInvalid()
	}
	page, pageSize, err := resolveModelDevPage(in.Page, in.PageSize)
	if err != nil {
		return nil, err
	}
	filter, err := modelDevExecutionFilter(in)
	if err != nil {
		return nil, err
	}
	out, err := s.resolver.ListExecutions(ctx, scope, page, pageSize, filter)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.ListExecutionsResponse{Total: out.Total}
	for _, execution := range out.Executions {
		reply.Items = append(reply.Items, modelDevPublicExecution(execution))
	}
	return reply, nil
}

// modelDevExecutionFilter converts the public state names into the owner enums.
// The state names reuse the response vocabulary, so a typo, a lower-case value
// or the placeholder UNSPECIFIED is rejected here instead of silently
// returning an unfiltered page.
func modelDevExecutionFilter(in *modeldevv1.ListExecutionsRequest) (data.ExecutionFilter, error) {
	var filter data.ExecutionFilter
	if in.ComputeState != nil {
		value, ok := modeldevcontractv1.ComputeState_value["COMPUTE_STATE_"+*in.ComputeState]
		if !ok || value == 0 {
			return data.ExecutionFilter{}, modelDevQueryInvalid()
		}
		parsed := modeldevcontractv1.ComputeState(value)
		filter.ComputeState = &parsed
	}
	if in.DeliveryState != nil {
		value, ok := modeldevcontractv1.DeliveryState_value["DELIVERY_STATE_"+*in.DeliveryState]
		if !ok || value == 0 {
			return data.ExecutionFilter{}, modelDevQueryInvalid()
		}
		parsed := modeldevcontractv1.DeliveryState(value)
		filter.DeliveryState = &parsed
	}
	if in.CloseState != nil {
		value, ok := modeldevcontractv1.CloseState_value["CLOSE_STATE_"+*in.CloseState]
		if !ok || value == 0 {
			return data.ExecutionFilter{}, modelDevQueryInvalid()
		}
		parsed := modeldevcontractv1.CloseState(value)
		filter.CloseState = &parsed
	}
	if in.Name != nil {
		// The shape is already checked at the HTTP boundary, which is the only
		// caller; the owner folds case before matching.
		value := *in.Name
		filter.Name = &value
	}
	return filter, nil
}

func (s *ModelDevService) GetExecutionLogs(ctx context.Context, in *modeldevv1.GetExecutionLogsRequest) (*modeldevv1.GetExecutionLogsResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevGetExecutionLogsPath)
	if err != nil {
		return nil, err
	}
	if in == nil || !modelDevQueryUUID(in.ExecutionId) || in.TailLines > 1000 || in.MaxBytes > 65536 ||
		(in.LogId != nil && !modelDevQueryUUID(in.GetLogId())) || (in.PodUid != nil && !modelDevQueryUUID(in.GetPodUid())) || !modelDevQueryContainer(in.Container) {
		return nil, modelDevQueryInvalid()
	}
	selection := data.LogSelection{LogID: in.GetLogId(), PodUID: in.GetPodUid(), Container: in.GetContainer(), Previous: in.GetPrevious()}
	out, err := s.resolver.GetExecutionLogs(ctx, scope, in.ExecutionId, in.TailLines, in.MaxBytes, selection)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.GetExecutionLogsResponse{
		LogId: out.Source.LogId, Truncated: out.Truncated, ObservedAt: out.ObservedAt.AsTime().UTC().Format(time.RFC3339Nano),
		SourceKind: strings.TrimPrefix(out.SourceKind.String(), "LOG_SOURCE_KIND_"), ContainerName: out.ContainerName, Previous: out.Previous, Available: out.Available,
	}
	for _, line := range out.Lines {
		reply.Lines = append(reply.Lines, &modeldevv1.ExecutionLogLine{Timestamp: line.Timestamp.AsTime().UTC().Format(time.RFC3339Nano), Text: line.Text})
	}
	return reply, nil
}

// modelDevQueryContainer accepts an absent container and otherwise only the
// container-name alphabet Kubernetes allows.
func modelDevQueryContainer(value *string) bool {
	if value == nil {
		return true
	}
	if *value == "" || len(*value) > 63 {
		return false
	}
	for _, character := range *value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

// ListExecutionPods answers the container-group / orchestration-step tab. A
// missing layer behaves as all; an unknown value is rejected rather than
// silently widened.
func (s *ModelDevService) ListExecutionPods(ctx context.Context, in *modeldevv1.ListExecutionPodsRequest) (*modeldevv1.ListExecutionPodsResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevListExecutionPodsPath)
	if err != nil {
		return nil, err
	}
	if in == nil || !modelDevQueryUUID(in.ExecutionId) {
		return nil, modelDevQueryInvalid()
	}
	layer, valid := data.ParseWorkloadLayer(in.GetLayer())
	if !valid {
		return nil, modelDevQueryInvalid()
	}
	out, err := s.resolver.ListExecutionPods(ctx, scope, in.ExecutionId, layer, in.GetIncludeRecycled())
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.ListExecutionPodsResponse{ObservedAt: out.ObservedAt.AsTime().UTC().Format(time.RFC3339Nano), Availability: strings.TrimPrefix(out.Availability.String(), "POD_AVAILABILITY_")}
	for _, pod := range out.Pods {
		reply.Pods = append(reply.Pods, modelDevPublicPod(pod))
	}
	return reply, nil
}

// ListExecutionEvents answers the event tab. An empty list after Kubernetes'
// retention window is a normal answer, never an error.
func (s *ModelDevService) ListExecutionEvents(ctx context.Context, in *modeldevv1.ListExecutionEventsRequest) (*modeldevv1.ListExecutionEventsResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevListExecutionEventsPath)
	if err != nil {
		return nil, err
	}
	if in == nil || !modelDevQueryUUID(in.ExecutionId) {
		return nil, modelDevQueryInvalid()
	}
	eventScope, valid := data.ParseEventScope(in.GetScope())
	if !valid {
		return nil, modelDevQueryInvalid()
	}
	out, err := s.resolver.ListExecutionEvents(ctx, scope, in.ExecutionId, eventScope)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.ListExecutionEventsResponse{
		ObservedAt: out.ObservedAt.AsTime().UTC().Format(time.RFC3339Nano), RetentionHint: out.RetentionHint, Truncated: out.Truncated,
	}
	for _, event := range out.Events {
		reply.Events = append(reply.Events, &modeldevv1.ExecutionEventView{
			Type: event.Type, Reason: event.Reason, Message: event.Message,
			FirstTimestamp: event.FirstTimestamp.AsTime().UTC().Format(time.RFC3339Nano),
			LastTimestamp:  event.LastTimestamp.AsTime().UTC().Format(time.RFC3339Nano),
			Count:          event.Count, Source: event.Source, InvolvedKind: event.InvolvedKind,
			InvolvedName: event.InvolvedName, InvolvedUid: event.InvolvedUid, FieldPath: event.FieldPath,
			ReportingInstance: event.ReportingInstance,
		})
	}
	return reply, nil
}

func modelDevPublicPod(pod *modeldevcontractv1.PodView) *modeldevv1.ExecutionPodView {
	out := &modeldevv1.ExecutionPodView{
		PodName: pod.PodName, PodUid: pod.PodUid, PodIp: pod.PodIp, Phase: pod.Phase, Ready: pod.Ready,
		NodeName: pod.NodeName, Restarts: pod.Restarts, Layer: strings.TrimPrefix(pod.Layer.String(), "POD_LAYER_"),
		Availability: strings.TrimPrefix(pod.Availability.String(), "POD_AVAILABILITY_"), StepName: pod.StepName,
		Attempt: pod.Attempt, JobIndex: pod.JobIndex, LogId: pod.LogId, LogAvailable: pod.LogAvailable,
		ApiPresent: pod.ApiPresent, ExitCode: pod.ExitCode, MetricInstanceId: pod.MetricInstanceId,
	}
	if at := pod.CreatedAt; at != nil {
		out.CreatedAt = at.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if at := pod.StartTime; at != nil {
		out.StartTime = at.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if at := pod.DeletionTimestamp; at != nil {
		out.DeletionTimestamp = at.AsTime().UTC().Format(time.RFC3339Nano)
	}
	for _, container := range pod.Containers {
		out.Containers = append(out.Containers, modelDevPublicContainer(container))
	}
	for _, container := range pod.InitContainers {
		out.InitContainers = append(out.InitContainers, modelDevPublicContainer(container))
	}
	for _, container := range pod.EphemeralContainers {
		out.EphemeralContainers = append(out.EphemeralContainers, modelDevPublicContainer(container))
	}
	return out
}

func modelDevPublicContainer(container *modeldevcontractv1.ContainerView) *modeldevv1.ExecutionContainerView {
	out := &modeldevv1.ExecutionContainerView{
		Name: container.Name, Type: container.Type, State: container.State, Reason: container.Reason,
		Message: container.Message, RestartCount: container.RestartCount, ExitCode: container.ExitCode,
	}
	if at := container.StartedAt; at != nil {
		out.StartedAt = at.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if at := container.FinishedAt; at != nil {
		out.FinishedAt = at.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if container.LastState != nil {
		out.LastState = modelDevPublicContainer(container.LastState)
	}
	return out
}

func modelDevPublicExecution(execution *modeldevcontractv1.ExecutionView) *modeldevv1.ExecutionView {
	out := &modeldevv1.ExecutionView{
		OperationId: execution.Identity.OperationId, ExecutionId: execution.Identity.ExecutionId, Name: execution.Name,
		Kind: strings.TrimPrefix(execution.Kind.String(), "EXECUTION_KIND_"), ImplementationId: execution.ImplementationId, ComputePresetId: execution.ComputePresetId,
		InputVersionId: execution.InputVersionId, ImageVersionId: execution.ImageVersionId,
		ComputeState:  strings.TrimPrefix(execution.States.ComputeState.String(), "COMPUTE_STATE_"),
		DeliveryState: strings.TrimPrefix(execution.States.DeliveryState.String(), "DELIVERY_STATE_"),
		CloseState:    strings.TrimPrefix(execution.States.CloseState.String(), "CLOSE_STATE_"),
		ResourceState: strings.TrimPrefix(execution.States.ResourceState.String(), "RESOURCE_STATE_"),
		CurrentStep:   strings.TrimPrefix(execution.CurrentStep.String(), "PIPELINE_STEP_"), StopRequested: execution.StopRequested, CloseGeneration: execution.CloseGeneration,
		AcceptedAt: execution.AcceptedAt.AsTime().UTC().Format(time.RFC3339Nano), DeadlineAt: execution.DeadlineAt.AsTime().UTC().Format(time.RFC3339Nano),
		ObservedAt: execution.ObservedAt.AsTime().UTC().Format(time.RFC3339Nano),
	}
	out.ParameterContractVersion = execution.ParameterContractVersion
	out.ResolvedConfigurationSha256 = execution.ResolvedConfigurationSha256
	for _, parameter := range execution.ResolvedParameters {
		out.ResolvedParameters = append(out.ResolvedParameters, modelDevPublicParameter(parameter))
	}
	if c := execution.ComputeOrigin; c != nil {
		out.Compute = &modeldevv1.ExecutionComputeView{ComputePresetId: c.ComputePresetId, SelectionDigest: c.SelectionDigest, CpuMillis: c.CpuMillis, MemoryMib: c.MemoryMib, DeviceMode: c.DeviceMode}
	}
	if timing := execution.Timing; timing != nil {
		out.Timing = &modeldevv1.ExecutionTimingView{ExecutionTimeoutSeconds: timing.ExecutionTimeoutSeconds, ExecutionTimeoutMaxSeconds: timing.ExecutionTimeoutMaxSeconds, StepTimeoutSeconds: timing.StepTimeoutSeconds}
	}
	if p := execution.ExplicitMlp; p != nil {
		out.ExplicitParameters = &modeldevv1.ExecutionParameters{LearningRate: p.LearningRate, Epochs: p.Epochs, BatchSize: p.BatchSize, Seed: p.Seed}
	}
	if p := execution.ExplicitFinetuning; p != nil {
		out.ExplicitParameters = &modeldevv1.ExecutionParameters{LearningRate: p.LearningRate, MaxSteps: p.MaxSteps, PerDeviceTrainBatchSize: p.PerDeviceTrainBatchSize, GradientAccumulationSteps: p.GradientAccumulationSteps, CutoffLen: p.CutoffLen, LoraRank: p.LoraRank, LoraAlpha: p.LoraAlpha, LoraDropout: p.LoraDropout, Seed: p.Seed}
	}
	for _, step := range execution.Steps {
		out.Steps = append(out.Steps, modelDevPublicStep(step))
	}
	for _, metric := range execution.Metrics {
		out.Metrics = append(out.Metrics, &modeldevv1.ExecutionMetricView{Name: metric.Name, DecimalValue: metric.DecimalValue, Step: metric.Step})
	}
	if r := execution.ModelRegistration; r != nil {
		out.ModelRegistration = &modeldevv1.ModelRegistrationView{State: r.State, RegistrationId: r.RegistrationId, ModelId: r.ModelId, ModelVersionId: r.ModelVersionId, FailureCode: r.FailureCode}
	}
	if failure := execution.Failure; failure != nil {
		out.Failure = modelDevPublicFailure(failure)
	} else {
		out.Failure = modelDevDerivedFailure(out)
	}
	if managed := execution.ManagedSettings; managed != nil {
		out.ManagedSettings = modelDevPublicManagedSettings(managed)
	}
	if base := execution.BaseModel; base != nil {
		out.BaseModel = &modeldevv1.ExecutionBaseModelView{
			ModelId: base.ModelId, VersionId: base.VersionId, Family: base.Family, Format: base.Format,
		}
	}
	if h := execution.History; h != nil {
		out.History = &modeldevv1.ExecutionHistoryView{RunId: h.RunId, WorkflowUid: h.WorkflowUid, ObservedAt: h.ObservedAt.AsTime().UTC().Format(time.RFC3339Nano), WorkflowResourceVersion: h.WorkflowResourceVersion, WorkflowNodesSha256: h.WorkflowNodesSha256}
		if h.RunState != 0 {
			out.History.RunState = strings.TrimPrefix(h.RunState.String(), "KFP_RUN_TERMINAL_STATE_")
		}
		if h.WorkflowPhase != 0 {
			out.History.WorkflowPhase = strings.TrimPrefix(h.WorkflowPhase.String(), "WORKFLOW_TERMINAL_PHASE_")
		}
		if h.RunFinishedAt != nil {
			out.History.RunFinishedAt = h.RunFinishedAt.AsTime().UTC().Format(time.RFC3339Nano)
		}
		if h.WorkflowFinishedAt != nil {
			out.History.WorkflowFinishedAt = h.WorkflowFinishedAt.AsTime().UTC().Format(time.RFC3339Nano)
		}
	}

	return out
}

// modelDevPublicFailure keeps the diagnostic a failed execution already recorded.
// The owner strips credentials and storage secrets from safe_message, so the
// text is forwarded unchanged rather than rebuilt here.
func modelDevPublicFailure(in *modeldevcontractv1.ErrorDetail) *modeldevv1.ExecutionFailureView {
	out := &modeldevv1.ExecutionFailureView{SafeMessage: in.SafeMessage, CorrelationId: in.CorrelationId}
	if in.Reason != 0 {
		out.Reason = strings.TrimPrefix(in.Reason.String(), "ERROR_REASON_")
	}
	for _, violation := range in.Violations {
		out.Violations = append(out.Violations, &modeldevv1.ExecutionFieldViolationView{Field: violation.Field, Reason: violation.Reason})
	}
	return out
}

// modelDevPublicManagedSettings exposes the read-only training hyperparameters.
// The owner renders decimals as strings, so they stay strings here.
func modelDevPublicManagedSettings(in *modeldevcontractv1.FinetuningManagedSettings) *modeldevv1.ExecutionManagedSettingsView {
	return &modeldevv1.ExecutionManagedSettingsView{
		Stage: in.Stage, FinetuningType: in.FinetuningType, LoraTarget: in.LoraTarget,
		LoraRank: in.LoraRank, LoraAlpha: in.LoraAlpha, CutoffLen: in.CutoffLen,
		PerDeviceTrainBatchSize: in.PerDeviceTrainBatchSize, GradientAccumulationSteps: in.GradientAccumulationSteps,
		Seed: in.Seed, DataSeed: in.DataSeed, Device: in.Device, Fp16: in.Fp16, Bf16: in.Bf16,
		FlashAttn: in.FlashAttn, ReportTo: in.ReportTo, SaveStrategy: in.SaveStrategy, Template: in.Template,
		TrainDtype: in.TrainDtype, ExportDtype: in.ExportDtype, VerifyDtype: in.VerifyDtype,
		LrSchedulerType: in.LrSchedulerType, WarmupRatio: in.WarmupRatio, WeightDecay: in.WeightDecay,
		LoraDropout: in.LoraDropout, NumTrainEpochs: in.NumTrainEpochs, Optim: in.Optim, LoggingSteps: in.LoggingSteps,
	}
}

// modelDevDerivedFailure fills the gap the owner leaves open. The owner records
// an ErrorDetail only for an input-version rejection and an unavailable model
// registration; a training failure closes the execution with the failed step
// marked in the timeline and no free-text reason. Reporting null there would
// look like "no problem", so the failed step is turned into a minimal
// diagnostic built only from observed facts. Nothing is invented: the reason
// reuses the owner's own STEP_FAILED vocabulary and the message names the step
// the owner already marked FAILED.
func modelDevDerivedFailure(execution *modeldevv1.ExecutionView) *modeldevv1.ExecutionFailureView {
	if execution.ComputeState != "FAILED" {
		return nil
	}
	derived := &modeldevv1.ExecutionFailureView{Reason: "STEP_FAILED"}
	for _, step := range execution.Steps {
		if step.State != "FAILED" {
			continue
		}
		if derived.SafeMessage == "" {
			derived.SafeMessage = "step " + step.Role + " failed"
		}
		derived.Violations = append(derived.Violations, &modeldevv1.ExecutionFieldViolationView{Field: "steps." + step.Role, Reason: "FAILED"})
	}
	if derived.SafeMessage == "" {
		derived.SafeMessage = "the execution ended FAILED without a failed step"
	}
	return derived
}

// modelDevPublicStep keeps the timeline facts ModelDev already derived. Absent
// timestamps stay empty strings, matching every other time field here.
func modelDevPublicStep(in *modeldevcontractv1.StepProgress) *modeldevv1.ExecutionStepView {
	step := &modeldevv1.ExecutionStepView{Role: in.Role, State: in.State, ExitCode: in.ExitCode}
	if in.StartedAt != nil {
		step.StartedAt = in.StartedAt.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if in.FinishedAt != nil {
		step.FinishedAt = in.FinishedAt.AsTime().UTC().Format(time.RFC3339Nano)
	}
	if in.DeadlineAt != nil {
		step.DeadlineAt = in.DeadlineAt.AsTime().UTC().Format(time.RFC3339Nano)
	}
	return step
}

func (s *ModelDevService) ListExecutionArtifacts(ctx context.Context, in *modeldevv1.ListExecutionArtifactsRequest) (*modeldevv1.ListExecutionArtifactsResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevListArtifactsPath)
	if err != nil {
		return nil, err
	}
	if in == nil || !modelDevQueryUUID(in.ExecutionId) {
		return nil, modelDevQueryInvalid()
	}
	page, pageSize, err := resolveModelDevPage(in.Page, in.PageSize)
	if err != nil {
		return nil, err
	}
	out, err := s.resolver.ListExecutionArtifacts(ctx, scope, in.ExecutionId, page, pageSize)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.ListExecutionArtifactsResponse{Total: out.Total}
	for _, artifact := range out.Artifacts {
		reply.Items = append(reply.Items, modelDevPublicArtifact(artifact))
	}
	return reply, nil
}

// resolveModelDevPage resolves the independent page/pageSize defaults and
// validates them, mirroring the VPC page contract. A missing field keeps its
// own default (1 or 20); an explicit zero or a size above 100 is rejected
// rather than silently corrected.
func resolveModelDevPage(page, pageSize *uint32) (uint32, uint32, error) {
	p, size := uint32(1), uint32(20)
	if page != nil {
		p = *page
	}
	if pageSize != nil {
		size = *pageSize
	}
	if p == 0 || size == 0 || size > 100 {
		return 0, 0, modelDevQueryInvalid()
	}
	return p, size, nil
}

func (s *ModelDevService) AuthorizeArtifactDownload(ctx context.Context, in *modeldevv1.AuthorizeArtifactDownloadRequest) (*modeldevv1.AuthorizeArtifactDownloadResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevDownloadArtifactPath)
	if err != nil {
		return nil, err
	}
	if in == nil || !modelDevQueryUUID(in.ArtifactId) {
		return nil, modelDevQueryInvalid()
	}
	out, err := s.resolver.AuthorizeArtifactDownload(ctx, scope, in.ArtifactId)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	return &modeldevv1.AuthorizeArtifactDownloadResponse{Artifact: modelDevPublicArtifact(out.Artifact), DownloadUrl: out.DownloadUrl, ExpiresAt: out.ExpiresAt.AsTime().UTC().Format(time.RFC3339Nano)}, nil
}
func modelDevPublicArtifact(in *modeldevcontractv1.ArtifactView) *modeldevv1.ArtifactView {
	return &modeldevv1.ArtifactView{ArtifactId: in.ArtifactId, ExecutionId: in.ExecutionId, Filename: in.Filename,
		Role: strings.TrimPrefix(in.Role.String(), "FILE_ROLE_"), SizeBytes: in.SizeBytes, Sha256: in.Sha256,
		DeliveryState: strings.TrimPrefix(in.DeliveryState.String(), "DELIVERY_STATE_"), VerifiedAt: in.VerifiedAt.AsTime().UTC().Format(time.RFC3339Nano)}
}
func modelDevQueryUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
func modelDevQueryInvalid() error {
	return errors.BadRequest("INVALID_MODELDEV_QUERY", "invalid modeldev query")
}
func modelDevQueryFailure(err error) error {
	switch {
	case stderrors.Is(err, data.ErrModelDevAuthorizationDenied):
		return errors.Forbidden("FORBIDDEN", "modeldev query forbidden")
	case stderrors.Is(err, context.Canceled), status.Code(err) == codes.Canceled:
		return errors.New(499, "MODELDEV_QUERY_CANCELED", "modeldev query canceled")
	case stderrors.Is(err, context.DeadlineExceeded), status.Code(err) == codes.DeadlineExceeded:
		return errors.New(504, "MODELDEV_QUERY_TIMEOUT", "modeldev query timed out")
	}
	switch status.Code(err) {
	case codes.NotFound, codes.PermissionDenied, codes.Unauthenticated:
		return errors.NotFound("RESOURCE_NOT_FOUND", "resource unavailable")
	case codes.FailedPrecondition:
		return errors.New(412, "ARTIFACT_NOT_PUBLISHED", "artifact unavailable")
	case codes.InvalidArgument:
		return modelDevQueryInvalid()
	default:
		return errors.ServiceUnavailable("MODELDEV_QUERY_UNAVAILABLE", "modeldev query unavailable")
	}
}
