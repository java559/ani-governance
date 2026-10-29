package data

import (
	"context"
	"encoding/json"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// CheckFinetuningReadiness delegates only a currently authorized source and a
// compute selection returned by Resource. It neither accepts nor starts work.
func (c *ModelDevClient) CheckFinetuningReadiness(ctx context.Context, scope ModelDevResolveScope, version string, template *string, selection *compute.ComputeSelection) (*modeldevv1.CheckFinetuningReadinessResponse, error) {
	if !modelDevCanonicalUUID(version) || compute.ValidateSelection(selection) != nil || template != nil && cpup01.ValidateTemplateID(*template) != nil {
		return nil, modelDevQueryUnavailable()
	}
	canonical, err := compute.CanonicalSelection(selection)
	if err != nil {
		return nil, modelDevQueryUnavailable()
	}
	origin, err := contractpb.DecodeComputeSelection(canonical, selection.SelectionDigest)
	if err != nil {
		return nil, modelDevQueryUnavailable()
	}
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_CheckFinetuningReadiness_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).CheckFinetuningReadiness(call, &modeldevv1.CheckFinetuningReadinessRequest{
		BaseModelVersionId: version, ComputePresetId: origin.ComputePresetID, FinetuningTemplate: template,
		ComputeOrigin: &modeldevv1.ComputeOrigin{ComputePresetId: origin.ComputePresetID, SelectionDigest: origin.SelectionDigest, SelectionCanonical: canonical, CpuMillis: origin.CPUMillis, MemoryMib: origin.MemoryMiB, DeviceMode: origin.DeviceMode},
	}, grpc.WaitForReady(true))
	if err != nil {
		md, _ := metadata.FromOutgoingContext(call)
		return nil, modelDevResolutionError(err, md.Get("x-ani-request-id")[0])
	}
	if !modelDevValidReadiness(out, version) {
		return nil, modelDevQueryUnavailable()
	}
	return out, nil
}

func modelDevValidReadiness(out *modeldevv1.CheckFinetuningReadinessResponse, version string) bool {
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || !out.RuntimeValidationRequired || out.RuntimeCapabilities == nil || (cpup01.ContentLimits{MaxContentBytes: out.Limits.GetMaxContentBytes()}).Validate() != nil || out.StructureLimits == nil {
		return false
	}
	var capabilities cpup01.RuntimeCapabilities
	raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(out.RuntimeCapabilities)
	if err != nil || json.Unmarshal(raw, &capabilities) != nil {
		return false
	}
	digest, err := capabilities.Digest()
	if err != nil || digest != out.RuntimeCapabilitiesSha256 || len(out.TemplateCandidates) != len(out.RuntimeCapabilities.RegisteredTemplates) {
		return false
	}
	for i, candidate := range out.TemplateCandidates {
		if !proto.Equal(candidate, out.RuntimeCapabilities.RegisteredTemplates[i]) {
			return false
		}
	}
	expected := &modeldevv1.StructureLimits{
		MaxModelFiles: cpup01.MaxModelFiles, MaxOutputFiles: cpup01.MaxOutputFiles, MaxArchiveEntries: cpup01.MaxArchiveEntries,
		MaxRelativePathBytes: cpup01.MaxRelativePathBytes, MaxStorageKeyBytes: cpup01.MaxStorageKeyBytes, MaxManifestBytes: cpup01.MaxManifestBytes,
		MaxModelConfigBytes: cpup01.MaxModelConfigBytes, MaxMaterialBytes: cpup01.MaxMaterialBytes, MaxArchiveMetadataBytes: cpup01.MaxArchiveMetadataBytes,
		MaxRecordBytes: cpup01.MaxRecordBytes, MaxJsonDepth: cpup01.MaxJSONDepth, MaxControlRpcBytes: cpup01.MaxControlRPCBytes,
		MaxPublicationCandidateBytes: cpup01.MaxPublicationCandidateBytes, MaxPublicationRefBytes: cpup01.MaxPublicationRefBytes,
	}
	if !proto.Equal(expected, out.StructureLimits) || ((out.ModelId == "") != (out.ModelVersionId == "")) || out.ModelId != "" && (!modelDevCanonicalUUID(out.ModelId) || out.ModelVersionId != version) {
		return false
	}
	switch out.Assessment {
	case modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_ELIGIBLE:
		if out.Reason != modeldevv1.ErrorReason_ERROR_REASON_UNSPECIFIED || out.ModelId == "" || out.ModelType == "" || out.ContextLimit <= 0 || out.TemplatePlan == nil {
			return false
		}
		var plan cpup01.TemplatePlan
		raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(out.TemplatePlan)
		return err == nil && json.Unmarshal(raw, &plan) == nil && plan.ValidateFor(capabilities) == nil
	case modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_TEMPLATE_REQUIRED:
		return out.Reason == modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_REQUIRED && out.TemplatePlan == nil
	case modeldevv1.FinetuningAssessment_FINETUNING_ASSESSMENT_UNSUPPORTED:
		return modelDevFinetuningBusinessReason(out.Reason) && out.Reason != modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_REQUIRED && out.TemplatePlan == nil
	default:
		return false
	}
}

func modelDevFinetuningBusinessReason(reason modeldevv1.ErrorReason) bool {
	switch reason {
	case modeldevv1.ErrorReason_ERROR_REASON_BASE_MODEL_INVALID, modeldevv1.ErrorReason_ERROR_REASON_BASE_MODEL_UNSUPPORTED,
		modeldevv1.ErrorReason_ERROR_REASON_CONTEXT_LIMIT_EXCEEDED, modeldevv1.ErrorReason_ERROR_REASON_CONTEXT_LIMIT_UNKNOWN,
		modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_REQUIRED, modeldevv1.ErrorReason_ERROR_REASON_FINETUNING_TEMPLATE_UNSUPPORTED,
		modeldevv1.ErrorReason_ERROR_REASON_CONTENT_LIMIT_EXCEEDED, modeldevv1.ErrorReason_ERROR_REASON_STRUCTURE_LIMIT_EXCEEDED,
		modeldevv1.ErrorReason_ERROR_REASON_DATASET_RECORD_TOO_LARGE, modeldevv1.ErrorReason_ERROR_REASON_DATASET_INVALID:
		return true
	default:
		return false
	}
}
