package data

import (
	"context"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (c *ModelDevClient) ValidateRelease(ctx context.Context, scope ModelDevResolveScope, preset, release, digest string) error {
	if !modelDevCanonicalUUID(preset) || !modelDevCanonicalUUID(release) || !modelDevDigest(digest) {
		return modelDevQueryUnavailable()
	}
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevManagementService_ValidateRelease_FullMethodName)
	if err != nil {
		return err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevManagementServiceClient(c.connection).ValidateRelease(call, &modeldevv1.ValidateReleaseRequest{PresetId: preset, ReleaseId: release, ReleaseDigest: digest}, grpc.WaitForReady(true))
	if err != nil {
		return modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || out.PresetId != preset || out.ReleaseId != release || out.ReleaseDigest != digest {
		return modelDevQueryUnavailable()
	}
	return nil
}

// ManagedOperation is a closed set of authenticated platform operations. The
// CLI cannot supply an RPC method, endpoint, tenant, actor or desired state.
func (c *ModelDevClient) ManagedOperation(ctx context.Context, scope ModelDevResolveScope, in proto.Message) (proto.Message, error) {
	var method string
	var execution string
	var executionOperation bool
	switch request := in.(type) {
	case *modeldevv1.ImportReleaseRequest:
		document, digest, err := cpup01.ParseRelease(request.CanonicalRelease)
		if err != nil || document.ReleaseID != request.ReleaseId || document.PresetID != request.PresetId || digest != request.ReleaseDigest {
			return nil, modelDevQueryUnavailable()
		}
		method = modeldevv1.ModelDevManagementService_ImportRelease_FullMethodName
	case *modeldevv1.ImportCSVRequest:
		if !modelDevCanonicalUUID(request.InputVersionId) || !modelDevCanonicalUUID(request.ReleaseId) || !modelDevDigest(request.ReleaseDigest) || request.Object == nil || !modelDevValidTimestamp(request.RequestedAt) {
			return nil, modelDevQueryUnavailable()
		}
		method = modeldevv1.ModelDevManagementService_ImportCSV_FullMethodName
	case *modeldevv1.ImportFinetuningDatasetRequest:
		if !modelDevCanonicalUUID(request.InputVersionId) || !modelDevCanonicalUUID(request.ReleaseId) || !modelDevDigest(request.ReleaseDigest) || request.Object == nil || !modelDevValidTimestamp(request.RequestedAt) || request.Format != "JSON" {
			return nil, modelDevQueryUnavailable()
		}
		method = modeldevv1.ModelDevManagementService_ImportFinetuningDataset_FullMethodName
	case *modeldevv1.InspectExecutionRequest:
		method = modeldevv1.ModelDevOperationsService_InspectExecution_FullMethodName
		execution = request.ExecutionId
		executionOperation = true
	case *modeldevv1.ReconcileExecutionRequest:
		method = modeldevv1.ModelDevOperationsService_ReconcileExecution_FullMethodName
		execution = request.ExecutionId
		executionOperation = true
	case *modeldevv1.PlanExecutionCleanupRequest:
		method = modeldevv1.ModelDevOperationsService_PlanExecutionCleanup_FullMethodName
		execution = request.ExecutionId
		executionOperation = true
	case *modeldevv1.ApplyExecutionCleanupRequest:
		method = modeldevv1.ModelDevOperationsService_ApplyExecutionCleanup_FullMethodName
		execution = request.ExecutionId
		executionOperation = true
		if !modelDevDigest(request.PlanSha256) {
			return nil, modelDevQueryUnavailable()
		}
	default:
		return nil, modelDevQueryUnavailable()
	}
	if executionOperation && !modelDevCanonicalUUID(execution) {
		return nil, modelDevQueryUnavailable()
	}
	call, cancel, err := c.queryContext(ctx, scope, method)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var importIdentity string
	switch request := in.(type) {
	case *modeldevv1.ImportCSVRequest:
		importIdentity = "ani:modeldev:csv-import:v1\x00" + scope.ResourceTenantID + "\x00" + scope.Actor + "\x00" + request.InputVersionId
	case *modeldevv1.ImportFinetuningDatasetRequest:
		importIdentity = "ani:modeldev:finetuning-import:v1\x00" + scope.ResourceTenantID + "\x00" + scope.Actor + "\x00" + request.InputVersionId
	}
	if importIdentity != "" {
		// ModelDev freezes this identity with the input. A fresh CLI/client must
		// replay the same operation; changed immutable body still conflicts at
		// ModelDev. Scope was validated above and current grants remain required.
		id := uuid.NewSHA1(uuid.NameSpaceURL, []byte(importIdentity))
		delegation, _ := metadata.FromOutgoingContext(call)
		delegation.Set("x-ani-request-id", id.String())
		call = metadata.NewOutgoingContext(call, delegation)
	}
	management := modeldevv1.NewModelDevManagementServiceClient(c.connection)
	operations := modeldevv1.NewModelDevOperationsServiceClient(c.connection)
	var out proto.Message
	switch request := in.(type) {
	case *modeldevv1.ImportReleaseRequest:
		response, e := management.ImportRelease(call, request, grpc.WaitForReady(true))
		err = e
		if e == nil && (response == nil || response.PresetId != request.PresetId || response.ReleaseId != request.ReleaseId || response.ReleaseDigest != request.ReleaseDigest) {
			return nil, modelDevQueryUnavailable()
		}
		out = response
	case *modeldevv1.ImportCSVRequest:
		response, e := management.ImportCSV(call, request, grpc.WaitForReady(true))
		err = e
		if e == nil && (response == nil || !modelDevValidInput(response.InputVersion) || response.InputVersion.InputVersionId != request.InputVersionId) {
			return nil, modelDevQueryUnavailable()
		}
		out = response
	case *modeldevv1.ImportFinetuningDatasetRequest:
		response, e := management.ImportFinetuningDataset(call, request, grpc.WaitForReady(true))
		err = e
		if e == nil && (response == nil || !modelDevValidInput(response.InputVersion) || response.InputVersion.InputVersionId != request.InputVersionId) {
			return nil, modelDevQueryUnavailable()
		}
		out = response
	case *modeldevv1.InspectExecutionRequest:
		response, e := operations.InspectExecution(call, request, grpc.WaitForReady(true))
		err = e
		if e == nil && (response == nil || !modelDevValidInspect(response.Inspection, execution)) {
			return nil, modelDevQueryUnavailable()
		}
		out = response
	case *modeldevv1.ReconcileExecutionRequest:
		response, e := operations.ReconcileExecution(call, request, grpc.WaitForReady(true))
		err = e
		if e == nil && (response == nil || !modelDevValidInspect(response.Inspection, execution)) {
			return nil, modelDevQueryUnavailable()
		}
		out = response
	case *modeldevv1.PlanExecutionCleanupRequest:
		response, e := operations.PlanExecutionCleanup(call, request, grpc.WaitForReady(true))
		err = e
		if e == nil && (response == nil || response.ExecutionId != execution || !modelDevDigest(response.PlanSha256) || !modelDevCanonicalUUID(response.OperationId) || !modelDevDigest(response.ExecutionSpecHash) || !modelDevValidTimestamp(response.ObservedAt)) {
			return nil, modelDevQueryUnavailable()
		}
		out = response
	case *modeldevv1.ApplyExecutionCleanupRequest:
		response, e := operations.ApplyExecutionCleanup(call, request, grpc.WaitForReady(true))
		err = e
		if e == nil && (response == nil || response.ExecutionId != execution || response.PlanSha256 != request.PlanSha256 || response.Actor != scope.Actor || !modelDevValidTimestamp(response.StartedAt)) {
			return nil, modelDevQueryUnavailable()
		}
		out = response
	}
	if err != nil {
		return nil, modelDevManagementError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) {
		return nil, modelDevQueryUnavailable()
	}
	return out, nil
}

func modelDevManagementError(err error) error {
	switch status.Code(err) {
	case codes.AlreadyExists:
		return status.Error(codes.AlreadyExists, "immutable management operation conflict")
	case codes.FailedPrecondition:
		return status.Error(codes.FailedPrecondition, "management precondition not satisfied")
	default:
		return modelDevQueryError(err)
	}
}

func modelDevValidInspect(in *modeldevv1.ExecutionInspection, execution string) bool {
	return in != nil && in.ExecutionId == execution && modelDevCanonicalUUID(in.OperationId) && modelDevDigest(in.ExecutionSpecHash) && modelDevValidTimestamp(in.RetrievedAt) && in.ComputeState != "" && in.DeliveryState != "" && in.CloseState != "" && len(in.Resources) <= 256
}
