package data

import (
	"context"
	"strings"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"google.golang.org/grpc"
)

func (c *ModelDevClient) ListPresets(ctx context.Context, scope ModelDevResolveScope, page, pageSize uint32) (*modeldevv1.ListPresetsResponse, error) {
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_ListPresets_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).ListPresets(call, &modeldevv1.ListPresetsRequest{Page: &modeldevv1.PageRequest{Page: &page, PageSize: &pageSize}}, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || len(out.Presets) > 100 {
		return nil, modelDevQueryUnavailable()
	}
	seen := map[string]bool{}
	for _, preset := range out.Presets {
		if preset == nil || !modelDevCanonicalUUID(preset.PresetId) || seen[preset.PresetId] || preset.Name == "" || len(preset.Name) > 256 || strings.ContainsAny(preset.Name, "\r\n\t") || !modelDevSupportedExecutionKind(preset.Kind) || len(preset.ImageVersionIds) > 32 || len(preset.ParameterRules) > 32 {
			return nil, modelDevQueryUnavailable()
		}
		seen[preset.PresetId] = true
		for _, id := range preset.ImageVersionIds {
			if !modelDevCanonicalUUID(id) {
				return nil, modelDevQueryUnavailable()
			}
		}
		names := map[string]bool{}
		for _, rule := range preset.ParameterRules {
			if rule == nil || rule.Name == "" || len(rule.Name) > 64 || names[rule.Name] || rule.Type == modeldevv1.ParameterType_PARAMETER_TYPE_UNSPECIFIED || len(rule.AllowedValues) > 32 {
				return nil, modelDevQueryUnavailable()
			}
			names[rule.Name] = true
			values := append([]*trainingv1.Parameter{rule.DefaultValue, rule.Minimum, rule.Maximum}, rule.AllowedValues...)
			for _, value := range values {
				if value != nil && (value.Name != rule.Name || value.Value == nil) {
					return nil, modelDevQueryUnavailable()
				}
			}
		}
	}
	return out, nil
}

func (c *ModelDevClient) GetInputVersion(ctx context.Context, scope ModelDevResolveScope, id string) (*modeldevv1.GetInputVersionResponse, error) {
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_GetInputVersion_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).GetInputVersion(call, &modeldevv1.GetInputVersionRequest{InputVersionId: id}, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || !modelDevValidInput(out.InputVersion) || out.InputVersion.InputVersionId != id {
		return nil, modelDevQueryUnavailable()
	}
	return out, nil
}

func (c *ModelDevClient) ListInputVersions(ctx context.Context, scope ModelDevResolveScope, page, pageSize uint32, state *modeldevv1.InputState) (*modeldevv1.ListInputVersionsResponse, error) {
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_ListInputVersions_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).ListInputVersions(call, &modeldevv1.ListInputVersionsRequest{Page: &modeldevv1.PageRequest{Page: &page, PageSize: &pageSize}, State: state}, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || len(out.InputVersions) > 100 {
		return nil, modelDevQueryUnavailable()
	}
	seen := map[string]bool{}
	for _, input := range out.InputVersions {
		if !modelDevValidInput(input) || seen[input.InputVersionId] || (state != nil && input.State != *state) {
			return nil, modelDevQueryUnavailable()
		}
		seen[input.InputVersionId] = true
	}
	return out, nil
}

func modelDevValidInput(in *modeldevv1.InputVersionView) bool {
	if in == nil || !modelDevCanonicalUUID(in.InputVersionId) || in.State == modeldevv1.InputState_INPUT_STATE_UNSPECIFIED || in.SizeBytes <= 0 || in.SizeBytes > 32<<20 || !modelDevDigest(in.Sha256) || !modelDevValidTimestamp(in.CreatedAt) {
		return false
	}
	if in.State != modeldevv1.InputState_INPUT_STATE_READY {
		return true
	}
	// READY inputs carry a fixed verified shape per format: the CPU baseline
	// is CSV 1024x16 and the managed finetuning import is JSON Alpaca 64x0.
	switch in.Format {
	case "CSV":
		return in.RowCount == 1024 && in.FeatureCount == 16
	case "JSON":
		return in.RowCount == 64 && in.FeatureCount == 0
	default:
		return false
	}
}
