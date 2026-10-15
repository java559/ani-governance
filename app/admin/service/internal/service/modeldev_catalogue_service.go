package service

import (
	"context"
	stderrors "errors"
	"strconv"
	"strings"
	"time"

	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
)

func (s *ModelDevService) ListPresets(ctx context.Context, in *modeldevv1.ListPresetsRequest) (*modeldevv1.ListPresetsResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevListPresetsPath)
	if err != nil {
		return nil, err
	}
	if in == nil || in.Limit > 100 || len(in.Cursor) > 2048 {
		return nil, modelDevQueryInvalid()
	}
	if s.bindings == nil {
		return nil, modelDevQueryFailure(nil)
	}
	out, err := s.resolver.ListPresets(ctx, scope, in.Limit, in.Cursor)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	principal, err := auth.PrincipalFromContext(ctx)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.ListPresetsResponse{NextCursor: out.NextCursor, Total: out.Total}
	for _, preset := range out.Presets {
		view := &modeldevv1.PresetView{PresetId: preset.PresetId, Name: preset.Name, Kind: strings.TrimPrefix(preset.Kind.String(), "EXECUTION_KIND_"), ImageVersionIds: preset.ImageVersionIds}
		binding, err := s.bindings.Get(ctx, data.ModelDevReleaseBindingScope{TenantID: principal.TenantID, ResourceTenantID: scope.ResourceTenantID, PresetID: preset.PresetId})
		if err != nil && !stderrors.Is(err, data.ErrModelDevBindingNotFound) {
			return nil, modelDevQueryFailure(err)
		}
		if binding != nil {
			view.ActiveReleaseId = binding.Target.ReleaseID
			view.BindingGeneration = binding.Generation
			view.NewSubmissionsEnabled = binding.Target.NewSubmissionsEnabled
		}
		for _, rule := range preset.ParameterRules {
			view.ParameterRules = append(view.ParameterRules, &modeldevv1.ParameterRule{Name: rule.Name, Type: strings.TrimPrefix(rule.Type.String(), "PARAMETER_TYPE_"), Required: rule.Required, DefaultValue: modelDevPublicParameter(rule.DefaultValue), Minimum: modelDevPublicParameter(rule.Minimum), Maximum: modelDevPublicParameter(rule.Maximum)})
			for _, allowed := range rule.AllowedValues {
				n := len(view.ParameterRules) - 1
				view.ParameterRules[n].AllowedValues = append(view.ParameterRules[n].AllowedValues, modelDevPublicParameter(allowed))
			}
		}
		reply.Items = append(reply.Items, view)
	}
	return reply, nil
}

func modelDevPublicParameter(in *trainingv1.Parameter) *modeldevv1.GeneralParameter {
	if in == nil {
		return nil
	}
	out := &modeldevv1.GeneralParameter{Name: in.Name}
	switch value := in.Value.(type) {
	case *trainingv1.Parameter_StringValue:
		out.Type = "STRING"
		out.Value = value.StringValue
	case *trainingv1.Parameter_IntegerValue:
		out.Type = "INTEGER"
		out.Value = strconv.FormatInt(value.IntegerValue, 10)
	case *trainingv1.Parameter_BooleanValue:
		out.Type = "BOOLEAN"
		out.Value = strconv.FormatBool(value.BooleanValue)
	case *trainingv1.Parameter_DecimalValue:
		out.Type = "DECIMAL"
		out.Value = value.DecimalValue
	}
	return out
}

func (s *ModelDevService) GetInputVersion(ctx context.Context, in *modeldevv1.GetInputVersionRequest) (*modeldevv1.GetInputVersionResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevGetInputVersionPath)
	if err != nil {
		return nil, err
	}
	if in == nil || !modelDevQueryUUID(in.InputVersionId) {
		return nil, modelDevQueryInvalid()
	}
	out, err := s.resolver.GetInputVersion(ctx, scope, in.InputVersionId)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	return &modeldevv1.GetInputVersionResponse{InputVersion: modelDevPublicInput(out.InputVersion)}, nil
}

func (s *ModelDevService) ListInputVersions(ctx context.Context, in *modeldevv1.ListInputVersionsRequest) (*modeldevv1.ListInputVersionsResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevListInputVersionsPath)
	if err != nil {
		return nil, err
	}
	if in == nil || in.Limit > 100 || len(in.Cursor) > 2048 {
		return nil, modelDevQueryInvalid()
	}
	var state *modeldevcontractv1.InputState
	if in.State != nil {
		value, ok := modeldevcontractv1.InputState_value["INPUT_STATE_"+*in.State]
		if !ok || value == 0 {
			return nil, modelDevQueryInvalid()
		}
		parsed := modeldevcontractv1.InputState(value)
		state = &parsed
	}
	out, err := s.resolver.ListInputVersions(ctx, scope, in.Limit, in.Cursor, state)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.ListInputVersionsResponse{NextCursor: out.NextCursor, Total: out.Total}
	for _, input := range out.InputVersions {
		reply.Items = append(reply.Items, modelDevPublicInput(input))
	}
	return reply, nil
}

func modelDevPublicInput(in *modeldevcontractv1.InputVersionView) *modeldevv1.InputVersionView {
	return &modeldevv1.InputVersionView{InputVersionId: in.InputVersionId, State: strings.TrimPrefix(in.State.String(), "INPUT_STATE_"), Format: in.Format, SizeBytes: in.SizeBytes, Sha256: in.Sha256, RowCount: in.RowCount, FeatureCount: in.FeatureCount, CreatedAt: in.CreatedAt.AsTime().UTC().Format(time.RFC3339Nano)}
}
