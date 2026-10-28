package data

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Query methods receive scope only after the BFF's current database grant.
// Outgoing metadata is rebuilt, never appended to caller-supplied metadata.
func (c *ModelDevClient) queryContext(ctx context.Context, scope ModelDevResolveScope, method string) (context.Context, context.CancelFunc, error) {
	if c == nil || c.connection == nil || c.timeout <= 0 {
		return nil, nil, modelDevQueryUnavailable()
	}
	if !modelDevCanonicalUUID(scope.ResourceTenantID) || !validModelDevClientActor(scope.Actor) {
		return nil, nil, modelDevQueryUnavailable()
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", scope.ResourceTenantID, "x-ani-actor", scope.Actor,
		"x-ani-request-id", uuid.NewString(), "x-ani-authorized-method", method, "x-ani-data-scope", "tenant-all")), cancel, nil
}

// streamContext rebuilds outgoing metadata for a long-lived stream. Unlike a
// unary call it installs no total deadline: the caller owns the bounded
// lifetime and the upstream must be able to idle between log lines.
func (c *ModelDevClient) streamContext(ctx context.Context, scope ModelDevResolveScope, method string) (context.Context, error) {
	if c == nil || c.connection == nil {
		return nil, modelDevQueryUnavailable()
	}
	if !modelDevCanonicalUUID(scope.ResourceTenantID) || !validModelDevClientActor(scope.Actor) {
		return nil, modelDevQueryUnavailable()
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", scope.ResourceTenantID, "x-ani-actor", scope.Actor,
		"x-ani-request-id", uuid.NewString(), "x-ani-authorized-method", method, "x-ani-data-scope", "tenant-all")), nil
}

func (c *ModelDevClient) GetExecution(ctx context.Context, scope ModelDevResolveScope, id string) (*modeldevv1.GetExecutionResponse, error) {
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_GetExecution_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).GetExecution(call, &modeldevv1.GetExecutionRequest{ExecutionId: id}, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || !modelDevValidExecution(out.Execution) || out.Execution.Identity.ExecutionId != id {
		return nil, modelDevQueryUnavailable()
	}
	return out, nil
}

func modelDevValidExecution(in *modeldevv1.ExecutionView) bool {
	return in != nil && modelDevValidExecutionPlan(in) && in.Identity != nil && modelDevCanonicalUUID(in.Identity.ExecutionId) &&
		modelDevCanonicalUUID(in.Identity.OperationId) && modelDevDigest(in.Identity.ExecutionSpecHash) && in.States != nil &&
		modelDevSupportedExecutionKind(in.Kind) &&
		modelDevValidTimestamp(in.AcceptedAt) && modelDevValidTimestamp(in.DeadlineAt) && modelDevValidTimestamp(in.ObservedAt) &&
		in.States.ComputeState != 0 && in.States.DeliveryState != 0 && in.States.CloseState != 0 && in.States.ResourceState != 0 &&
		modelDevValidSteps(in.Steps) && modelDevValidMetrics(in.Metrics) && modelDevValidModelRegistration(in) && modelDevValidExecutionHistory(in)
}

// The timeline and curve are derived facts, but they still reach a public DTO.
// Unknown roles, states or an unbounded list fail closed like every other field.
func modelDevValidSteps(steps []*modeldevv1.StepProgress) bool {
	if len(steps) > 16 {
		return false
	}
	for _, step := range steps {
		if step == nil || !modelDevStepRole(step.Role) || !modelDevStepState(step.State) || step.ExitCode < 0 ||
			!modelDevOptionalTimestamp(step.StartedAt) || !modelDevOptionalTimestamp(step.FinishedAt) || !modelDevOptionalTimestamp(step.DeadlineAt) {
			return false
		}
	}
	return true
}

func modelDevStepRole(role string) bool {
	switch role {
	case "prepare", "train-wait", "export", "verify", "collect", "publish", "register", "close":
		return true
	}
	return false
}

func modelDevStepState(state string) bool {
	switch state {
	case "PENDING", "RUNNING", "SUCCEEDED", "FAILED", "STOPPED":
		return true
	}
	return false
}

func modelDevValidMetrics(metrics []*trainingv1.Metric) bool {
	if len(metrics) > 10000 {
		return false
	}
	for _, metric := range metrics {
		if metric == nil || metric.Name == "" || len(metric.Name) > 128 || metric.DecimalValue == "" || len(metric.DecimalValue) > 64 {
			return false
		}
		if _, err := strconv.ParseFloat(metric.DecimalValue, 64); err != nil {
			return false
		}
	}
	return true
}

// modelDevOptionalTimestamp accepts an absent observation; a present one must
// still be a valid, non-zero instant.
func modelDevOptionalTimestamp(value *timestamppb.Timestamp) bool {
	return value == nil || modelDevValidTimestamp(value)
}

// Registration is an owner observation, never a conclusion inferred from
// successful transport or artifact publication. No arbitrary backend error is
// passed to the public DTO.
func modelDevValidModelRegistration(in *modeldevv1.ExecutionView) bool {
	r := in.ModelRegistration
	if r == nil {
		return true
	}
	if in.Kind != trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING {
		return false
	}
	noIDs := r.RegistrationId == "" && r.ModelId == "" && r.ModelVersionId == ""
	switch r.State {
	case "PENDING":
		return noIDs && r.FailureCode == ""
	case "DEPENDENCY_UNAVAILABLE":
		return noIDs && r.FailureCode == "MODEL_DEPENDENCY_UNAVAILABLE"
	case "READY":
		return r.FailureCode == "" && modelDevCanonicalUUID(r.RegistrationId) && modelDevCanonicalUUID(r.ModelId) && modelDevCanonicalUUID(r.ModelVersionId)
	default:
		return false
	}
}

// History is the typed projection of the original owner's persisted close
// evidence. A managed close may precede terminal controller evidence; absence
// of terminal fields never implies a successful run or Workflow.
func modelDevValidExecutionHistory(in *modeldevv1.ExecutionView) bool {
	h := in.History
	if h == nil {
		return true
	}
	if !modelDevValidTimestamp(h.ObservedAt) || h.ObservedAt.AsTime().Before(in.AcceptedAt.AsTime()) {
		return false
	}
	hasIDs := h.RunId != "" || h.WorkflowUid != ""
	if hasIDs && (!modelDevCanonicalUUID(h.RunId) || !modelDevCanonicalUUID(h.WorkflowUid)) {
		return false
	}
	if h.RunState == 0 && h.WorkflowPhase == 0 {
		return h.RunFinishedAt == nil && h.WorkflowFinishedAt == nil && h.WorkflowResourceVersion == "" && h.WorkflowNodesSha256 == ""
	}
	if !hasIDs || h.RunState < modeldevv1.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_SUCCEEDED || h.RunState > modeldevv1.KfpRunTerminalState_KFP_RUN_TERMINAL_STATE_CANCELED || h.WorkflowPhase < modeldevv1.WorkflowTerminalPhase_WORKFLOW_TERMINAL_PHASE_SUCCEEDED || h.WorkflowPhase > modeldevv1.WorkflowTerminalPhase_WORKFLOW_TERMINAL_PHASE_ERROR || !modelDevValidTimestamp(h.RunFinishedAt) || !modelDevValidTimestamp(h.WorkflowFinishedAt) || h.WorkflowResourceVersion == "" || (h.WorkflowNodesSha256 != "" && !modelDevDigest(h.WorkflowNodesSha256)) {
		return false
	}
	for _, r := range h.WorkflowResourceVersion {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	accepted, observed := in.AcceptedAt.AsTime(), h.ObservedAt.AsTime()
	return !h.RunFinishedAt.AsTime().Before(accepted) && !h.WorkflowFinishedAt.AsTime().Before(accepted) && !h.RunFinishedAt.AsTime().After(observed) && !h.WorkflowFinishedAt.AsTime().After(observed)
}

// Historical details carry the original frozen compute/timing observation.
// Current catalogue defaults never repair an incomplete owner response.
func modelDevValidExecutionPlan(in *modeldevv1.ExecutionView) bool {
	if in == nil || in.ComputeOrigin == nil || in.Timing == nil || !modelDevValidTimestamp(in.Timing.AcceptedAt) || !modelDevValidTimestamp(in.Timing.ExecutionDeadlineAt) || !modelDevValidTimestamp(in.AcceptedAt) || !modelDevValidTimestamp(in.DeadlineAt) {
		return false
	}
	if (in.Kind == trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING && in.ImplementationId != "builtin.mlp") || (in.Kind == trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING && in.ImplementationId != "builtin.sft_lora") {
		return false
	}
	origin := in.ComputeOrigin
	var selection compute.ComputeSelection
	if protojson.Unmarshal(origin.SelectionCanonical, &selection) != nil {
		return false
	}
	selection.SelectionDigest = origin.SelectionDigest
	if compute.ValidateSelection(&selection) != nil {
		return false
	}
	canonical, err := compute.CanonicalSelection(&selection)
	if err != nil || !bytes.Equal(canonical, origin.SelectionCanonical) || selection.PresetId != in.ComputePresetId || origin.ComputePresetId != selection.PresetId || origin.CpuMillis != selection.CpuMillis || origin.MemoryMib != selection.MemoryMib || (selection.Gpu == nil && origin.DeviceMode != "cpu") || (selection.Gpu != nil && origin.DeviceMode != "gpu") {
		return false
	}
	timing := cpup01.Timing{AcceptedAt: in.Timing.AcceptedAt.AsTime(), ExecutionTimeoutSeconds: in.Timing.ExecutionTimeoutSeconds, ExecutionTimeoutMaxSeconds: in.Timing.ExecutionTimeoutMaxSeconds, StepTimeoutSeconds: in.Timing.StepTimeoutSeconds, ExecutionDeadlineAt: in.Timing.ExecutionDeadlineAt.AsTime()}
	return timing.Validate() == nil && timing.AcceptedAt.Equal(in.AcceptedAt.AsTime()) && timing.ExecutionDeadlineAt.Equal(in.DeadlineAt.AsTime())
}

// The BFF passes through the shared execution contract unchanged; only the two
// published CPU kinds are accepted so an unknown enum cannot widen the surface.
func modelDevSupportedExecutionKind(kind trainingv1.ExecutionKind) bool {
	return kind == trainingv1.ExecutionKind_EXECUTION_KIND_GENERAL_TRAINING || kind == trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING
}

func (c *ModelDevClient) ListExecutionArtifacts(ctx context.Context, scope ModelDevResolveScope, id string, page, pageSize uint32) (*modeldevv1.ListExecutionArtifactsResponse, error) {
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_ListExecutionArtifacts_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).ListExecutionArtifacts(call, &modeldevv1.ListExecutionArtifactsRequest{ExecutionId: id, Page: &modeldevv1.PageRequest{Page: &page, PageSize: &pageSize}}, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || len(out.Artifacts) > 100 {
		return nil, modelDevQueryUnavailable()
	}
	seen := map[string]bool{}
	for _, artifact := range out.Artifacts {
		if !modelDevValidArtifact(artifact) || artifact.ExecutionId != id || seen[artifact.ArtifactId] {
			return nil, modelDevQueryUnavailable()
		}
		seen[artifact.ArtifactId] = true
	}
	return out, nil
}

func (c *ModelDevClient) AuthorizeArtifactDownload(ctx context.Context, scope ModelDevResolveScope, id string) (*modeldevv1.AuthorizeArtifactDownloadResponse, error) {
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_AuthorizeArtifactDownload_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).AuthorizeArtifactDownload(call, &modeldevv1.AuthorizeArtifactDownloadRequest{ArtifactId: id}, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || !modelDevValidArtifact(out.Artifact) || out.Artifact.ArtifactId != id || !modelDevValidTimestamp(out.ExpiresAt) {
		return nil, modelDevQueryUnavailable()
	}
	endpoint, err := url.Parse(out.DownloadUrl)
	expires, now := out.ExpiresAt.AsTime(), time.Now()
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" || len(out.DownloadUrl) > 8192 ||
		strings.ContainsAny(out.DownloadUrl, "\r\n\t ") || !expires.After(now) || expires.After(now.Add(15*time.Minute)) {
		return nil, modelDevQueryUnavailable()
	}
	return out, nil
}

func modelDevQueryUnavailable() error {
	return status.Error(codes.Unavailable, "modeldev query unavailable")
}
func modelDevQueryError(err error) error {
	switch status.Code(err) {
	case codes.Canceled:
		return status.Error(codes.Canceled, "modeldev query canceled")
	case codes.DeadlineExceeded:
		return status.Error(codes.DeadlineExceeded, "modeldev query timed out")
	case codes.NotFound:
		return status.Error(codes.NotFound, "resource unavailable")
	case codes.PermissionDenied, codes.Unauthenticated:
		return status.Error(codes.PermissionDenied, "resource unavailable")
	case codes.FailedPrecondition:
		return status.Error(codes.FailedPrecondition, "artifact unavailable")
	case codes.InvalidArgument:
		return status.Error(codes.InvalidArgument, "invalid query")
	default:
		return modelDevQueryUnavailable()
	}
}
func modelDevCanonicalUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
func modelDevDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && strings.ToLower(value) == value
}
func modelDevValidTimestamp(value *timestamppb.Timestamp) bool {
	return value != nil && value.CheckValid() == nil && !value.AsTime().IsZero()
}
func modelDevValidArtifact(value *modeldevv1.ArtifactView) bool {
	if value == nil || !modelDevCanonicalUUID(value.ArtifactId) || !modelDevCanonicalUUID(value.ExecutionId) || !modelDevArtifactRelativePath(value.Filename) || value.SizeBytes < 0 || !modelDevDigest(value.Sha256) ||
		value.Role == trainingv1.FileRole_FILE_ROLE_UNSPECIFIED || value.DeliveryState != modeldevv1.DeliveryState_DELIVERY_STATE_PUBLISHED || !modelDevValidTimestamp(value.VerifiedAt) {
		return false
	}
	return true
}

// Published artifacts keep the owner's canonical, slash-separated relative path.
func modelDevArtifactRelativePath(value string) bool {
	if value == "" || !utf8.ValidString(value) || path.IsAbs(value) || path.Clean(value) != value || strings.Contains(value, "\\") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// The public DTO mapping is a whitelist; unknown wire fields and enum values
// also fail closed so additions cannot silently alter the accepted contract.
func modelDevUnknownFields(message protoreflect.Message) bool {
	if len(message.GetUnknown()) != 0 {
		return true
	}
	bad := false
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		check := func(v protoreflect.Value) {
			if field.Kind() == protoreflect.MessageKind && modelDevUnknownFields(v.Message()) {
				bad = true
			}
			if field.Kind() == protoreflect.EnumKind && field.Enum().Values().ByNumber(v.Enum()) == nil {
				bad = true
			}
		}
		if field.IsList() {
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				check(list.Get(i))
			}
		} else {
			check(value)
		}
		return !bad
	})
	return bad
}
