package service

import (
	"context"
	"slices"
	"strings"

	"github.com/google/uuid"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
)

type inferencePolicyOwner interface {
	CreateInferencePolicyTrusted(context.Context, string, string, *runtimev1.CreateInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error)
	ListInferencePoliciesTrusted(context.Context, string, string, *runtimev1.ListInferenceAccessPoliciesRequest) (*runtimev1.ListInferenceAccessPoliciesResponse, error)
	GetInferencePolicyTrusted(context.Context, string, string, *runtimev1.GetInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error)
	UpdateInferencePolicyTrusted(context.Context, string, string, *runtimev1.UpdateInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error)
	DeleteInferencePolicyTrusted(context.Context, string, string, *runtimev1.DeleteInferenceAccessPolicyRequest) (*runtimev1.DeleteInferenceAccessPolicyResponse, error)
	BindInferenceKeyPolicyTrusted(context.Context, string, string, *runtimev1.BindInferenceAccessKeyPolicyRequest) (*runtimev1.InferenceAccessKey, error)
}

func (s *InferenceService) accessPolicyScope(ctx context.Context) (inferencePolicyOwner, string, string, error) {
	_, tenant, actor, err := s.inferenceKeyScope(ctx)
	if err != nil {
		return nil, "", "", err
	}
	owner, ok := s.owner.(inferencePolicyOwner)
	if !ok {
		return nil, "", "", view.ErrorInferenceKeyUnavailable("inference policy management unavailable")
	}
	return owner, tenant, actor, nil
}
func policyInput(data *view.InferenceAccessPolicyData, id string) (*runtimev1.InferenceAccessPolicy, error) {
	if data == nil || strings.TrimSpace(data.Name) == "" || len(data.Name) > 256 || data.RequestsPerSecond < 0 || data.RequestsPerMinute < 0 || data.RequestsPerHour < 0 {
		return nil, view.ErrorInvalidInferenceKeyRequest("invalid policy")
	}
	models := make([]string, 0, len(data.AllowedModelNames))
	for _, model := range data.AllowedModelNames {
		model = strings.TrimSpace(model)
		if model == "" || len(model) > 256 {
			return nil, view.ErrorInvalidInferenceKeyRequest("invalid model name")
		}
		models = append(models, model)
	}
	slices.Sort(models)
	models = slices.Compact(models)
	return &runtimev1.InferenceAccessPolicy{Id: id, Name: strings.TrimSpace(data.Name), AllowedModelNames: models, RequestsPerSecond: data.RequestsPerSecond, RequestsPerMinute: data.RequestsPerMinute, RequestsPerHour: data.RequestsPerHour}, nil
}
func policyOutput(p *runtimev1.InferenceAccessPolicy, id string) (*view.InferenceAccessPolicy, error) {
	if p == nil || !canonicalInferenceKeyID(p.Id) || (id != "" && p.Id != id) || strings.TrimSpace(p.Name) == "" || p.Version < 1 || p.RequestsPerSecond < 0 || p.RequestsPerMinute < 0 || p.RequestsPerHour < 0 {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference policy response")
	}
	return &view.InferenceAccessPolicy{Id: p.Id, Name: p.Name, AllowedModelNames: append([]string{}, p.AllowedModelNames...), RequestsPerSecond: p.RequestsPerSecond, RequestsPerMinute: p.RequestsPerMinute, RequestsPerHour: p.RequestsPerHour, Version: p.Version}, nil
}
func (s *InferenceService) CreateInferencePolicy(ctx context.Context, in *view.CreateInferencePolicyRequest) (*view.InferenceAccessPolicy, error) {
	if in == nil {
		return nil, view.ErrorInvalidInferenceKeyRequest("data required")
	}
	p, err := policyInput(in.Data, "")
	if err != nil {
		return nil, err
	}
	owner, tenant, actor, err := s.accessPolicyScope(ctx)
	if err != nil {
		return nil, err
	}
	out, err := owner.CreateInferencePolicyTrusted(ctx, tenant, actor, &runtimev1.CreateInferenceAccessPolicyRequest{Policy: p, RequestId: uuid.NewString()})
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	return policyOutput(out, "")
}
func (s *InferenceService) ListInferencePolicies(ctx context.Context, in *view.ListInferencePoliciesRequest) (*view.ListInferencePoliciesResponse, error) {
	if in == nil {
		return nil, view.ErrorInvalidInferenceKeyRequest("request required")
	}
	page, size := int32(1), int32(20)
	if in.Page != nil {
		page = *in.Page
	}
	if in.PageSize != nil {
		size = *in.PageSize
	}
	if page < 1 || page > 100 || size < 1 || size > 100 {
		return nil, view.ErrorInvalidInferenceKeyRequest("invalid pagination")
	}
	owner, tenant, actor, err := s.accessPolicyScope(ctx)
	if err != nil {
		return nil, err
	}
	out, err := owner.ListInferencePoliciesTrusted(ctx, tenant, actor, &runtimev1.ListInferenceAccessPoliciesRequest{Page: page, PageSize: size})
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	if out == nil || out.Total < 0 || len(out.Items) > int(size) || int64(len(out.Items)) > out.Total {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference policy response")
	}
	result := &view.ListInferencePoliciesResponse{Items: make([]*view.InferenceAccessPolicy, 0, len(out.Items)), Total: out.Total}
	for _, p := range out.Items {
		v, e := policyOutput(p, "")
		if e != nil {
			return nil, e
		}
		result.Items = append(result.Items, v)
	}
	return result, nil
}
func (s *InferenceService) GetInferencePolicy(ctx context.Context, in *view.GetInferencePolicyRequest) (*view.InferenceAccessPolicy, error) {
	if in == nil || !canonicalInferenceKeyID(in.PolicyId) {
		return nil, view.ErrorInvalidInferenceKeyRequest("valid policy_id required")
	}
	owner, tenant, actor, err := s.accessPolicyScope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := owner.GetInferencePolicyTrusted(ctx, tenant, actor, &runtimev1.GetInferenceAccessPolicyRequest{Id: in.PolicyId})
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	return policyOutput(p, in.PolicyId)
}
func (s *InferenceService) UpdateInferencePolicy(ctx context.Context, in *view.UpdateInferencePolicyRequest) (*view.InferenceAccessPolicy, error) {
	if in == nil || !canonicalInferenceKeyID(in.PolicyId) {
		return nil, view.ErrorInvalidInferenceKeyRequest("valid policy_id required")
	}
	p, err := policyInput(in.Data, in.PolicyId)
	if err != nil {
		return nil, err
	}
	owner, tenant, actor, err := s.accessPolicyScope(ctx)
	if err != nil {
		return nil, err
	}
	out, err := owner.UpdateInferencePolicyTrusted(ctx, tenant, actor, &runtimev1.UpdateInferenceAccessPolicyRequest{Policy: p, RequestId: uuid.NewString()})
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	return policyOutput(out, in.PolicyId)
}
func (s *InferenceService) DeleteInferencePolicy(ctx context.Context, in *view.DeleteInferencePolicyRequest) (*view.DeleteInferencePolicyResponse, error) {
	if in == nil || !canonicalInferenceKeyID(in.PolicyId) {
		return nil, view.ErrorInvalidInferenceKeyRequest("valid policy_id required")
	}
	owner, tenant, actor, err := s.accessPolicyScope(ctx)
	if err != nil {
		return nil, err
	}
	out, err := owner.DeleteInferencePolicyTrusted(ctx, tenant, actor, &runtimev1.DeleteInferenceAccessPolicyRequest{Id: in.PolicyId, RequestId: uuid.NewString()})
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	if out == nil {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference policy response")
	}
	return &view.DeleteInferencePolicyResponse{}, nil
}
func (s *InferenceService) BindInferenceKeyPolicy(ctx context.Context, in *view.BindInferenceKeyPolicyRequest) (*view.InferenceKey, error) {
	if in == nil || in.Data == nil || !canonicalInferenceKeyID(in.KeyId) || !canonicalInferenceKeyID(in.Data.PolicyId) {
		return nil, view.ErrorInvalidInferenceKeyRequest("valid key_id and policy_id required")
	}
	owner, tenant, actor, err := s.accessPolicyScope(ctx)
	if err != nil {
		return nil, err
	}
	out, err := owner.BindInferenceKeyPolicyTrusted(ctx, tenant, actor, &runtimev1.BindInferenceAccessKeyPolicyRequest{KeyId: in.KeyId, PolicyId: in.Data.PolicyId, RequestId: uuid.NewString()})
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	if out == nil || out.PolicyId != in.Data.PolicyId {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference key response")
	}
	return projectInferenceKey(out, in.KeyId, false)
}
