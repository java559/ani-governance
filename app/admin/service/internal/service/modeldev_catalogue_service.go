package service

import (
	"context"
	"strconv"
	"strings"
	"time"

	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
)

func (s *ModelDevService) ListImplementations(ctx context.Context, in *modeldevv1.ListImplementationsRequest) (*modeldevv1.ListImplementationsResponse, error) {
	scope, err := s.modelDevQueryScope(ctx, data.ModelDevListImplementationsPath)
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
	var kind *trainingv1.ExecutionKind
	if in.Kind != nil {
		value, ok := trainingv1.ExecutionKind_value["EXECUTION_KIND_"+*in.Kind]
		if !ok || value == 0 {
			return nil, modelDevQueryInvalid()
		}
		parsed := trainingv1.ExecutionKind(value)
		kind = &parsed
	}
	out, err := s.resolver.ListImplementations(ctx, scope, page, pageSize, kind)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.ListImplementationsResponse{Total: out.Total, Items: []*modeldevv1.ImplementationView{}}
	for _, preset := range out.Implementations {
		view := &modeldevv1.ImplementationView{ImplementationId: preset.ImplementationId, Name: preset.Name, Kind: strings.TrimPrefix(preset.Kind.String(), "EXECUTION_KIND_"), Version: preset.Version, ParameterContractVersion: preset.ParameterContractVersion, SupportedDeviceModes: preset.SupportedDeviceModes, Available: preset.Available, ExecutionTimeoutDefaultSeconds: preset.ExecutionTimeoutDefaultSeconds, ExecutionTimeoutMaxSeconds: preset.ExecutionTimeoutMaxSeconds, StepTimeoutSeconds: preset.StepTimeoutSeconds}
		for _, rule := range preset.ParameterRules {
			view.ParameterRules = append(view.ParameterRules, modelDevPublicParameterRule(rule))
			for _, allowed := range rule.AllowedValues {
				n := len(view.ParameterRules) - 1
				view.ParameterRules[n].AllowedValues = append(view.ParameterRules[n].AllowedValues, modelDevPublicParameter(allowed))
			}
		}
		reply.Items = append(reply.Items, view)
	}
	return reply, nil
}

// modelDevPublicParameterRule maps one catalogue rule. The inclusive flags stay
// pointers: a missing upper bound must not be read as "closed", because the
// owner omits maximum entirely for open-ended parameters.
func modelDevPublicParameterRule(rule *modeldevcontractv1.ParameterRule) *modeldevv1.ParameterRule {
	return &modeldevv1.ParameterRule{
		Name:                  rule.Name,
		Type:                  strings.TrimPrefix(rule.Type.String(), "PARAMETER_TYPE_"),
		Required:              rule.Required,
		DefaultValue:          modelDevPublicParameter(rule.DefaultValue),
		Minimum:               modelDevPublicParameter(rule.Minimum),
		Maximum:               modelDevPublicParameter(rule.Maximum),
		NumericRepresentation: rule.NumericRepresentation,
		ValidationDescription: rule.ValidationDescription,
		MinimumInclusive:      rule.MinimumInclusive,
		MaximumInclusive:      rule.MaximumInclusive,
		Editable:              rule.Editable,
		NonEditableReason:     rule.NonEditableReason,
	}
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
	if in == nil {
		return nil, modelDevQueryInvalid()
	}
	page, pageSize, err := resolveModelDevPage(in.Page, in.PageSize)
	if err != nil {
		return nil, err
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
	// Only the formats the owner verifies are accepted. Rejecting an unknown
	// value keeps it from becoming an empty page that looks like an empty
	// tenant.
	format, ok := modelDevInputFormat(in.Format)
	if !ok {
		return nil, modelDevQueryInvalid()
	}
	out, err := s.resolver.ListInputVersions(ctx, scope, page, pageSize, state, format)
	if err != nil {
		return nil, modelDevQueryFailure(err)
	}
	reply := &modeldevv1.ListInputVersionsResponse{Total: out.Total}
	for _, input := range out.InputVersions {
		reply.Items = append(reply.Items, modelDevPublicInput(input))
	}
	return reply, nil
}

// modelDevInputFormat keeps only the two formats the owner verifies. The
// boolean is false for any other value, so the caller can reject instead of
// forwarding a filter the upstream would answer with an empty page.
func modelDevInputFormat(in *string) (*string, bool) {
	if in == nil {
		return nil, true
	}
	switch *in {
	case "CSV", "JSON":
		value := *in
		return &value, true
	default:
		return nil, false
	}
}

func modelDevPublicInput(in *modeldevcontractv1.InputVersionView) *modeldevv1.InputVersionView {
	return &modeldevv1.InputVersionView{InputVersionId: in.InputVersionId, State: strings.TrimPrefix(in.State.String(), "INPUT_STATE_"), Format: in.Format, SizeBytes: in.SizeBytes, Sha256: in.Sha256, RowCount: in.RowCount, FeatureCount: in.FeatureCount, CreatedAt: in.CreatedAt.AsTime().UTC().Format(time.RFC3339Nano)}
}
