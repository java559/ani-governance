package data

import (
	"context"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

func (c *InferenceClient) CreateInferencePolicyTrusted(ctx context.Context, tenant, actor string, in *runtimev1.CreateInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	call, cancel, err := c.trustedKeyContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.keys.CreateInferenceAccessPolicy(call, in)
}

func (c *InferenceClient) ListInferencePoliciesTrusted(ctx context.Context, tenant, actor string, in *runtimev1.ListInferenceAccessPoliciesRequest) (*runtimev1.ListInferenceAccessPoliciesResponse, error) {
	call, cancel, err := c.trustedKeyContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.keys.ListInferenceAccessPolicies(call, in)
}

func (c *InferenceClient) GetInferencePolicyTrusted(ctx context.Context, tenant, actor string, in *runtimev1.GetInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	call, cancel, err := c.trustedKeyContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.keys.GetInferenceAccessPolicy(call, in)
}

func (c *InferenceClient) UpdateInferencePolicyTrusted(ctx context.Context, tenant, actor string, in *runtimev1.UpdateInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	call, cancel, err := c.trustedKeyContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.keys.UpdateInferenceAccessPolicy(call, in)
}

func (c *InferenceClient) DeleteInferencePolicyTrusted(ctx context.Context, tenant, actor string, in *runtimev1.DeleteInferenceAccessPolicyRequest) (*runtimev1.DeleteInferenceAccessPolicyResponse, error) {
	call, cancel, err := c.trustedKeyContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.keys.DeleteInferenceAccessPolicy(call, in)
}

func (c *InferenceClient) BindInferenceKeyPolicyTrusted(ctx context.Context, tenant, actor string, in *runtimev1.BindInferenceAccessKeyPolicyRequest) (*runtimev1.InferenceAccessKey, error) {
	call, cancel, err := c.trustedKeyContext(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	defer cancel()
	return c.keys.BindInferenceAccessKeyPolicy(call, in)
}
