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
	out, err := s.resolver.ListExecutions(ctx, scope, page, pageSize)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.ListExecutionsResponse{Total: out.Total}
	for _, execution := range out.Executions {
		reply.Items = append(reply.Items, modelDevPublicExecution(execution))
	}
	return reply, nil
}

func (s *ModelDevService) GetExecutionLogs(ctx context.Context, in *modeldevv1.GetExecutionLogsRequest) (*modeldevv1.GetExecutionLogsResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevGetExecutionLogsPath)
	if err != nil {
		return nil, err
	}
	if in == nil || !modelDevQueryUUID(in.ExecutionId) || in.TailLines > 1000 || in.MaxBytes > 65536 {
		return nil, modelDevQueryInvalid()
	}
	out, err := s.resolver.GetExecutionLogs(ctx, scope, in.ExecutionId, in.TailLines, in.MaxBytes)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.GetExecutionLogsResponse{LogId: out.Source.LogId, Truncated: out.Truncated, ObservedAt: out.ObservedAt.AsTime().UTC().Format(time.RFC3339Nano)}
	for _, line := range out.Lines {
		reply.Lines = append(reply.Lines, &modeldevv1.ExecutionLogLine{Timestamp: line.Timestamp.AsTime().UTC().Format(time.RFC3339Nano), Text: line.Text})
	}
	return reply, nil
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
	if r := execution.ModelRegistration; r != nil {
		out.ModelRegistration = &modeldevv1.ModelRegistrationView{State: r.State, RegistrationId: r.RegistrationId, ModelId: r.ModelId, ModelVersionId: r.ModelVersionId, FailureCode: r.FailureCode}
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
