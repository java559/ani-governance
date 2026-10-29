package service

import (
	"context"
	"strings"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/modeldevbudget"
	"google.golang.org/protobuf/encoding/protojson"
)

func (s *ModelDevService) CheckFinetuningReadiness(ctx context.Context, in *modeldevv1.CheckFinetuningReadinessRequest) (reply *modeldevv1.CheckFinetuningReadinessResponse, result error) {
	defer func() {
		if err := ctx.Err(); err != nil {
			reply, result = nil, modelDevCreateFailure(err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, modelDevCreateFailure(err)
	}
	if in == nil || !modelDevQueryUUID(in.BaseModelVersionId) || !modelDevQueryUUID(in.ComputePresetId) || in.FinetuningTemplate != nil && cpup01.ValidateTemplateID(*in.FinetuningTemplate) != nil {
		return nil, modelDevQueryInvalid()
	}
	if s == nil || s.resolver == nil || s.compute == nil {
		return nil, modelDevCreateUnavailable()
	}
	preflight, cancel := context.WithTimeout(ctx, s.resolver.OperationTimeout())
	defer cancel()
	scope, err := s.modelDevQueryScope(preflight, data.ModelDevReadinessPath)
	if err != nil {
		return nil, err
	}
	selection, err := s.compute.ResolveComputePreset(preflight, scope.ResourceTenantID, scope.Actor, &compute.ResolveComputePresetRequest{TenantId: scope.ResourceTenantID, PresetId: in.ComputePresetId, Usage: compute.Usage_TRAINING})
	if err != nil {
		return nil, modelDevCreateResolutionFailure(err)
	}
	if compute.ValidateSelection(selection) != nil || selection.PresetId != in.ComputePresetId {
		return nil, modelDevCreateUnavailable()
	}
	if err := preflight.Err(); err != nil {
		return nil, modelDevCreateFailure(err)
	}
	if err := modeldevbudget.FinishPreflight(ctx); err != nil {
		return nil, modelDevCreateFailure(err)
	}
	cancel()
	resolved, err := s.resolver.CheckFinetuningReadiness(ctx, scope, in.BaseModelVersionId, in.FinetuningTemplate, selection)
	if err != nil {
		return nil, modelDevCreateResolutionFailure(err)
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(resolved)
	if err != nil {
		return nil, modelDevCreateUnavailable()
	}
	out := new(modeldevv1.CheckFinetuningReadinessResponse)
	if (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, out) != nil {
		return nil, modelDevCreateUnavailable()
	}
	out.Assessment = strings.TrimPrefix(resolved.Assessment.String(), "FINETUNING_ASSESSMENT_")
	out.Reason = strings.TrimPrefix(resolved.Reason.String(), "ERROR_REASON_")
	if err := ctx.Err(); err != nil {
		return nil, modelDevCreateFailure(err)
	}
	return out, nil
}
