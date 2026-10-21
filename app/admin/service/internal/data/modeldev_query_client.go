package data

import (
	"context"
	"encoding/hex"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
	return in != nil && in.Identity != nil && modelDevCanonicalUUID(in.Identity.ExecutionId) &&
		modelDevCanonicalUUID(in.Identity.OperationId) && modelDevDigest(in.Identity.ExecutionSpecHash) && in.States != nil &&
		modelDevSupportedExecutionKind(in.Kind) &&
		modelDevValidTimestamp(in.AcceptedAt) && modelDevValidTimestamp(in.DeadlineAt) && modelDevValidTimestamp(in.ObservedAt) &&
		in.States.ComputeState != 0 && in.States.DeliveryState != 0 && in.States.CloseState != 0 && in.States.ResourceState != 0
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
	if value == nil || !modelDevCanonicalUUID(value.ArtifactId) || !modelDevCanonicalUUID(value.ExecutionId) || value.Filename == "" || path.Base(value.Filename) != value.Filename || value.Filename == "." || value.Filename == ".." || strings.Contains(value.Filename, "\\") || value.SizeBytes < 0 || !modelDevDigest(value.Sha256) ||
		value.Role == trainingv1.FileRole_FILE_ROLE_UNSPECIFIED || value.DeliveryState != modeldevv1.DeliveryState_DELIVERY_STATE_PUBLISHED || !modelDevValidTimestamp(value.VerifiedAt) {
		return false
	}
	for _, character := range value.Filename {
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
