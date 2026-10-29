package service

import (
	"context"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const policyTestID = "33333333-3333-4333-8333-333333333333"

type policyOwnerProbe struct {
	*inferenceKeyOwnerProbe
	policy *runtimev1.InferenceAccessPolicy
	input  *runtimev1.InferenceAccessPolicy
	bind   *runtimev1.BindInferenceAccessKeyPolicyRequest
}

func (p *policyOwnerProbe) CreateInferencePolicyTrusted(_ context.Context, t, a string, in *runtimev1.CreateInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	p.observe(t, a)
	p.input = in.Policy
	return p.policy, p.err
}
func (p *policyOwnerProbe) ListInferencePoliciesTrusted(_ context.Context, t, a string, in *runtimev1.ListInferenceAccessPoliciesRequest) (*runtimev1.ListInferenceAccessPoliciesResponse, error) {
	p.observe(t, a)
	return &runtimev1.ListInferenceAccessPoliciesResponse{Items: []*runtimev1.InferenceAccessPolicy{p.policy}, Total: 1}, p.err
}
func (p *policyOwnerProbe) GetInferencePolicyTrusted(_ context.Context, t, a string, in *runtimev1.GetInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	p.observe(t, a)
	return p.policy, p.err
}
func (p *policyOwnerProbe) UpdateInferencePolicyTrusted(_ context.Context, t, a string, in *runtimev1.UpdateInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	p.observe(t, a)
	p.input = in.Policy
	return p.policy, p.err
}
func (p *policyOwnerProbe) DeleteInferencePolicyTrusted(_ context.Context, t, a string, in *runtimev1.DeleteInferenceAccessPolicyRequest) (*runtimev1.DeleteInferenceAccessPolicyResponse, error) {
	p.observe(t, a)
	return &runtimev1.DeleteInferenceAccessPolicyResponse{}, p.err
}
func (p *policyOwnerProbe) BindInferenceKeyPolicyTrusted(_ context.Context, t, a string, in *runtimev1.BindInferenceAccessKeyPolicyRequest) (*runtimev1.InferenceAccessKey, error) {
	p.observe(t, a)
	p.bind = in
	return p.key, p.err
}
func TestInferencePolicyManagement(t *testing.T) {
	owner := &policyOwnerProbe{inferenceKeyOwnerProbe: inferenceKeyProbe(), policy: &runtimev1.InferenceAccessPolicy{Id: policyTestID, Name: "policy", AllowedModelNames: []string{"a", "b"}, RequestsPerSecond: 2, Version: 1}}
	s := NewInferenceServiceWithOwner(nil, owner, directInferenceTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, TenantID: 26, ID: 33})
	data := &view.InferenceAccessPolicyData{Name: " policy ", AllowedModelNames: []string{"b", "a", "a"}, RequestsPerSecond: 2}
	p, err := s.CreateInferencePolicy(ctx, &view.CreateInferencePolicyRequest{Data: data})
	require.NoError(t, err)
	require.Equal(t, policyTestID, p.Id)
	require.Equal(t, []string{"a", "b"}, owner.input.AllowedModelNames)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", owner.tenant)
	require.Equal(t, "governance:user:33", owner.actor)
	_, err = s.GetInferencePolicy(ctx, &view.GetInferencePolicyRequest{PolicyId: policyTestID})
	require.NoError(t, err)
	list, err := s.ListInferencePolicies(ctx, &view.ListInferencePoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	_, err = s.UpdateInferencePolicy(ctx, &view.UpdateInferencePolicyRequest{PolicyId: policyTestID, Data: data})
	require.NoError(t, err)
	require.Equal(t, policyTestID, owner.input.Id)
	_, err = s.BindInferenceKeyPolicy(ctx, &view.BindInferenceKeyPolicyRequest{KeyId: inferenceKeyTestID, Data: &view.BindInferenceKeyPolicyData{PolicyId: policyTestID}})
	require.NoError(t, err)
	require.Equal(t, policyTestID, owner.bind.PolicyId)
	owner.err = status.Error(codes.FailedPrecondition, "in use")
	_, err = s.DeleteInferencePolicy(ctx, &view.DeleteInferencePolicyRequest{PolicyId: policyTestID})
	require.Equal(t, "INFERENCE_KEY_CONFLICT", kratoserrors.Reason(err))
	owner.err = status.Error(codes.NotFound, "not found")
	_, err = s.GetInferencePolicy(ctx, &view.GetInferencePolicyRequest{PolicyId: policyTestID})
	require.Equal(t, "INFERENCE_KEY_NOT_FOUND", kratoserrors.Reason(err))
}
func TestInferencePolicyRejectsUntrustedAndInvalid(t *testing.T) {
	owner := &policyOwnerProbe{inferenceKeyOwnerProbe: inferenceKeyProbe()}
	s := NewInferenceServiceWithOwner(nil, owner, directInferenceTenantProbe{})
	for _, principal := range []*auth.Principal{nil, {Type: auth.SubjectUser, ID: 1}, {Type: auth.SubjectAPIKey, TenantID: 26, ID: 1}} {
		ctx := context.Background()
		if principal != nil {
			ctx = auth.NewPrincipalContext(ctx, principal)
		}
		_, err := s.GetInferencePolicy(ctx, &view.GetInferencePolicyRequest{PolicyId: policyTestID})
		require.Error(t, err)
	}
	require.Zero(t, owner.calls)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, TenantID: 26, ID: 33})
	for _, d := range []*view.InferenceAccessPolicyData{nil, {Name: ""}, {Name: "x", RequestsPerMinute: -1}, {Name: "x", AllowedModelNames: []string{" "}}} {
		_, err := s.CreateInferencePolicy(ctx, &view.CreateInferencePolicyRequest{Data: d})
		require.Equal(t, "INVALID_INFERENCE_KEY_REQUEST", kratoserrors.Reason(err))
	}
	_, err := s.BindInferenceKeyPolicy(ctx, &view.BindInferenceKeyPolicyRequest{KeyId: inferenceKeyTestID, Data: &view.BindInferenceKeyPolicyData{PolicyId: "bad"}})
	require.Error(t, err)
	require.Zero(t, owner.calls)
}
