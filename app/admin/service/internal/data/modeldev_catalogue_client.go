package data

import (
	"context"
	"encoding/json"
	"strings"

	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

func (c *ModelDevClient) ListImplementations(ctx context.Context, scope ModelDevResolveScope, page, pageSize uint32, kind *trainingv1.ExecutionKind, deviceMode string) (*modeldevv1.ListImplementationsResponse, error) {
	if deviceMode != "cpu" && deviceMode != "gpu" {
		return nil, modelDevQueryUnavailable()
	}
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_ListImplementations_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).ListImplementations(call, &modeldevv1.ListImplementationsRequest{Page: &modeldevv1.PageRequest{Page: &page, PageSize: &pageSize}, Kind: kind, DeviceMode: &deviceMode}, grpc.WaitForReady(true))
	if err != nil {
		return nil, modelDevQueryError(err)
	}
	if out == nil || modelDevUnknownFields(out.ProtoReflect()) || len(out.Implementations) > 100 {
		return nil, modelDevQueryUnavailable()
	}
	seen := map[string]bool{}
	for _, preset := range out.Implementations {
		// A filtered page must not carry another kind. An owner that ignores the
		// new field would otherwise silently widen a narrowed request.
		if preset == nil || (preset.ImplementationId != "builtin.mlp" && preset.ImplementationId != "builtin.sft_lora") || seen[preset.ImplementationId] || preset.Name == "" || len(preset.Name) > 256 || strings.ContainsAny(preset.Name, "\r\n\t") || !modelDevSupportedExecutionKind(preset.Kind) || (kind != nil && preset.Kind != *kind) || len(preset.ParameterRules) > 32 {
			return nil, modelDevQueryUnavailable()
		}
		if len(preset.SupportedDeviceModes) > 1 || len(preset.SupportedDeviceModes) == 1 && preset.SupportedDeviceModes[0] != deviceMode {
			return nil, modelDevQueryUnavailable()
		}
		if preset.RuntimeCapabilities == nil {
			if preset.RuntimeCapabilitiesSha256 != "" || preset.Available && preset.Kind == trainingv1.ExecutionKind_EXECUTION_KIND_FINETUNING {
				return nil, modelDevQueryUnavailable()
			}
		} else {
			var capabilities cpup01.RuntimeCapabilities
			raw, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(preset.RuntimeCapabilities)
			if err != nil || json.Unmarshal(raw, &capabilities) != nil {
				return nil, modelDevQueryUnavailable()
			}
			digest, err := capabilities.Digest()
			if err != nil || digest != preset.RuntimeCapabilitiesSha256 {
				return nil, modelDevQueryUnavailable()
			}
		}
		seen[preset.ImplementationId] = true
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

func (c *ModelDevClient) ListInputVersions(ctx context.Context, scope ModelDevResolveScope, page, pageSize uint32, state *modeldevv1.InputState, format *string) (*modeldevv1.ListInputVersionsResponse, error) {
	call, cancel, err := c.queryContext(ctx, scope, modeldevv1.ModelDevQueryService_ListInputVersions_FullMethodName)
	if err != nil {
		return nil, err
	}
	defer cancel()
	out, err := modeldevv1.NewModelDevQueryServiceClient(c.connection).ListInputVersions(call, &modeldevv1.ListInputVersionsRequest{Page: &modeldevv1.PageRequest{Page: &page, PageSize: &pageSize}, State: state, Format: format}, grpc.WaitForReady(true))
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
	if in == nil || !modelDevCanonicalUUID(in.InputVersionId) || in.State == modeldevv1.InputState_INPUT_STATE_UNSPECIFIED || in.SizeBytes <= 0 || !modelDevDigest(in.Sha256) || !modelDevValidTimestamp(in.CreatedAt) {
		return false
	}
	if in.State != modeldevv1.InputState_INPUT_STATE_READY {
		return true
	}
	// READY inputs preserve the importer-verified nonempty shape. Baseline
	// sample counts are ordinary test data and cannot gate legitimate inputs.
	switch in.Format {
	case "CSV":
		return in.RowCount > 0 && in.FeatureCount == 16
	case "JSON":
		return in.RowCount > 0 && in.FeatureCount == 0
	default:
		return false
	}
}
