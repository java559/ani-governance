package service

import (
	"context"
	"errors"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const inferenceKeyTestSecret = "iak_0000000000000000000000000000000000000000000"

const inferenceKeyTestID = "22222222-2222-4222-8222-222222222222"

// Only the external Inference protocol is substituted. The BFF constructor,
// identity checks, validation, mapping and error/secret handling are real.
type inferenceKeyOwnerProbe struct {
	directInferenceOwnerProbe
	key           *runtimev1.InferenceAccessKey
	create        *runtimev1.CreateInferenceAccessKeyRequest
	page          *runtimev1.ListInferenceAccessKeysRequest
	tenant, actor string
	replayed      bool
	err           error
	calls         int
}

func (p *inferenceKeyOwnerProbe) observe(tenant, actor string) {
	p.calls++
	p.tenant, p.actor = tenant, actor
}
func (p *inferenceKeyOwnerProbe) CreateInferenceKeyTrusted(_ context.Context, tenant, actor string, in *runtimev1.CreateInferenceAccessKeyRequest) (*runtimev1.CreateInferenceAccessKeyResponse, error) {
	p.observe(tenant, actor)
	p.create = in
	return &runtimev1.CreateInferenceAccessKeyResponse{Key: p.key, Secret: inferenceKeyTestSecret, Replayed: p.replayed}, p.err
}
func (p *inferenceKeyOwnerProbe) ListInferenceKeysTrusted(_ context.Context, tenant, actor string, in *runtimev1.ListInferenceAccessKeysRequest) (*runtimev1.ListInferenceAccessKeysResponse, error) {
	p.observe(tenant, actor)
	p.page = in
	return &runtimev1.ListInferenceAccessKeysResponse{Items: []*runtimev1.InferenceAccessKey{p.key}, Total: 1}, p.err
}
func (p *inferenceKeyOwnerProbe) GetInferenceKeyTrusted(_ context.Context, tenant, actor string, in *runtimev1.GetInferenceAccessKeyRequest) (*runtimev1.InferenceAccessKey, error) {
	p.observe(tenant, actor)
	return p.key, p.err
}
func (p *inferenceKeyOwnerProbe) RevokeInferenceKeyTrusted(_ context.Context, tenant, actor string, in *runtimev1.RevokeInferenceAccessKeyRequest) (*runtimev1.RevokeInferenceAccessKeyResponse, error) {
	p.observe(tenant, actor)
	key := proto.Clone(p.key).(*runtimev1.InferenceAccessKey)
	key.RevokedAt = timestamppb.Now()
	return &runtimev1.RevokeInferenceAccessKeyResponse{Key: key}, p.err
}
func (p *inferenceKeyOwnerProbe) RotateInferenceKeyTrusted(_ context.Context, tenant, actor string, in *runtimev1.RotateInferenceAccessKeyRequest) (*runtimev1.RotateInferenceAccessKeyResponse, error) {
	p.observe(tenant, actor)
	return &runtimev1.RotateInferenceAccessKeyResponse{Key: p.key, Secret: inferenceKeyTestSecret, Replayed: p.replayed}, p.err
}

func inferenceKeyProbe() *inferenceKeyOwnerProbe {
	return &inferenceKeyOwnerProbe{key: &runtimev1.InferenceAccessKey{Id: inferenceKeyTestID, Name: "app", KeyPrefix: "iak_000000000000", PolicyId: "33333333-3333-4333-8333-333333333333", CreatedAt: timestamppb.Now(), Version: 1}}
}

func TestInferenceKeyTrustedManagementAndOneTimeSecret(t *testing.T) {
	owner := inferenceKeyProbe()
	s := NewInferenceServiceWithOwner(nil, owner, directInferenceTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, TenantID: 26, ID: 33})
	in := &view.CreateInferenceKeyRequest{Data: &view.CreateInferenceKeyData{Name: "app", PolicyId: "33333333-3333-4333-8333-333333333333", IdempotencyKey: "create-1"}}
	created, err := s.CreateInferenceKey(ctx, in)
	require.NoError(t, err)
	require.NotEmpty(t, created.Secret)
	require.False(t, created.Replayed)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", owner.tenant)
	require.Equal(t, "governance:user:33", owner.actor)
	require.NotEmpty(t, owner.create.RequestId)
	require.Equal(t, "create-1", owner.create.IdempotencyKey)
	owner.replayed = true
	replayed, err := s.CreateInferenceKey(ctx, in)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Empty(t, replayed.Secret, "even a malformed owner replay must not leak the returned secret")
	require.Equal(t, created.Key.Id, replayed.Key.Id)
	in.Data.Name = "different-intent"
	_, err = s.CreateInferenceKey(ctx, in)
	require.Equal(t, "INFERENCE_KEY_CONFLICT", kratoserrors.Reason(err))
	in.Data.Name = "app"
	list, err := s.ListInferenceKeys(ctx, &view.ListInferenceKeysRequest{})
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, int32(1), owner.page.Page)
	require.Equal(t, int32(20), owner.page.PageSize)
	_, err = s.GetInferenceKey(ctx, &view.GetInferenceKeyRequest{KeyId: inferenceKeyTestID})
	require.NoError(t, err)
	rotated, err := s.RotateInferenceKey(ctx, &view.RotateInferenceKeyRequest{KeyId: inferenceKeyTestID, Data: &view.RotateInferenceKeyData{IdempotencyKey: "rotate-1"}})
	require.NoError(t, err)
	require.Empty(t, rotated.Secret)
	require.True(t, rotated.Replayed)
	revoked, err := s.RevokeInferenceKey(ctx, &view.RevokeInferenceKeyRequest{KeyId: inferenceKeyTestID})
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)
}

func TestInferenceKeyReadsReturnFullSecretWithoutLeakingItInWrites(t *testing.T) {
	owner := inferenceKeyProbe()
	owner.key.Secret = proto.String(inferenceKeyTestSecret)
	s := NewInferenceServiceWithOwner(nil, owner, directInferenceTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, TenantID: 26, ID: 33})
	list, err := s.ListInferenceKeys(ctx, &view.ListInferenceKeysRequest{})
	require.NoError(t, err)
	require.Equal(t, inferenceKeyTestSecret, list.Items[0].GetSecret())
	detail, err := s.GetInferenceKey(ctx, &view.GetInferenceKeyRequest{KeyId: inferenceKeyTestID})
	require.NoError(t, err)
	require.Equal(t, inferenceKeyTestSecret, detail.GetSecret())
	redacted := proto.Clone(list).(*view.ListInferenceKeysResponse)
	redacted.Redact()
	require.Empty(t, redacted.Items[0].GetSecret(), "nested list secrets must be redacted")
	detail.Redact()
	require.Empty(t, detail.GetSecret())
	require.Equal(t, inferenceKeyTestSecret, owner.key.GetSecret(), "redaction must not mutate owner objects")
	owner.replayed = true
	replayed, err := s.RotateInferenceKey(ctx, &view.RotateInferenceKeyRequest{KeyId: inferenceKeyTestID, Data: &view.RotateInferenceKeyData{IdempotencyKey: "rotate"}})
	require.NoError(t, err)
	require.Empty(t, replayed.Secret)
	require.Nil(t, replayed.Key.Secret, "write replay must not leak a nested read secret")
	revoked, err := s.RevokeInferenceKey(ctx, &view.RevokeInferenceKeyRequest{KeyId: inferenceKeyTestID})
	require.NoError(t, err)
	require.Nil(t, revoked.Secret)
	owner.key.Secret = nil
	legacy, err := s.GetInferenceKey(ctx, &view.GetInferenceKeyRequest{KeyId: inferenceKeyTestID})
	require.NoError(t, err)
	require.Nil(t, legacy.Secret, "legacy hashes cannot be reconstructed")
	for _, secret := range []string{"", "private-owner-secret", "iak_1111111111111111111111111111111111111111111"} {
		owner.key.Secret = &secret
		_, err := s.GetInferenceKey(ctx, &view.GetInferenceKeyRequest{KeyId: inferenceKeyTestID})
		require.Equal(t, "INFERENCE_KEY_UNAVAILABLE", kratoserrors.Reason(err))
		_, err = s.ListInferenceKeys(ctx, &view.ListInferenceKeysRequest{})
		require.Equal(t, "INFERENCE_KEY_UNAVAILABLE", kratoserrors.Reason(err))
	}
}

func TestInferenceKeyRejectsMachinePlatformAndInvalidRequests(t *testing.T) {
	owner := inferenceKeyProbe()
	s := NewInferenceServiceWithOwner(nil, owner, directInferenceTenantProbe{})
	for _, principal := range []*auth.Principal{nil, {Type: auth.SubjectUser, ID: 1}, {Type: auth.SubjectAPIKey, TenantID: 26, ID: 1}} {
		ctx := context.Background()
		if principal != nil {
			ctx = auth.NewPrincipalContext(ctx, principal)
		}
		_, err := s.GetInferenceKey(ctx, &view.GetInferenceKeyRequest{KeyId: inferenceKeyTestID})
		require.Error(t, err)
	}
	require.Zero(t, owner.calls)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, TenantID: 26, ID: 33})
	for _, in := range []*view.CreateInferenceKeyRequest{nil, {}, {Data: &view.CreateInferenceKeyData{}}, {Data: &view.CreateInferenceKeyData{Name: "app", IdempotencyKey: "x"}}} {
		_, err := s.CreateInferenceKey(ctx, in)
		require.Equal(t, "INVALID_INFERENCE_KEY_REQUEST", kratoserrors.Reason(err))
	}
	for _, page := range []int32{0, -1, 101} {
		_, err := s.ListInferenceKeys(ctx, &view.ListInferenceKeysRequest{Page: &page})
		require.Equal(t, "INVALID_INFERENCE_KEY_REQUEST", kratoserrors.Reason(err))
	}
	_, err := s.ListInferenceKeys(ctx, nil)
	require.Error(t, err)
	_, err = s.GetInferenceKey(ctx, nil)
	require.Error(t, err)
	_, err = s.RevokeInferenceKey(ctx, nil)
	require.Error(t, err)
	_, err = s.RotateInferenceKey(ctx, nil)
	require.Error(t, err)
	require.Zero(t, owner.calls)
	_, err = NewInferenceService(nil).GetInferenceKey(ctx, &view.GetInferenceKeyRequest{KeyId: inferenceKeyTestID})
	require.Equal(t, "INFERENCE_KEY_UNAVAILABLE", kratoserrors.Reason(err))
}

func TestInferenceKeySanitizesOwnerErrorsAndResponses(t *testing.T) {
	owner := inferenceKeyProbe()
	s := NewInferenceServiceWithOwner(nil, owner, directInferenceTenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, TenantID: 26, ID: 33})
	for _, tc := range []struct {
		code   codes.Code
		reason string
	}{
		{codes.NotFound, "INFERENCE_KEY_NOT_FOUND"},
		{codes.InvalidArgument, "INVALID_INFERENCE_KEY_REQUEST"},
		{codes.FailedPrecondition, "INFERENCE_KEY_CONFLICT"},
		{codes.Unavailable, "INFERENCE_KEY_UNAVAILABLE"},
		{codes.DeadlineExceeded, "INFERENCE_KEY_UNAVAILABLE"},
	} {
		owner.err = status.Error(tc.code, "private-owner-address-and-secret")
		_, err := s.GetInferenceKey(ctx, &view.GetInferenceKeyRequest{KeyId: inferenceKeyTestID})
		require.Equal(t, tc.reason, kratoserrors.Reason(err))
		require.NotContains(t, kratoserrors.FromError(err).Message, "private-owner")
		require.True(t, errors.Is(err, owner.err), "original error must remain in the internal cause")
	}
	owner.err = nil
	owner.key.Id = "33333333-3333-4333-8333-333333333333"
	_, err := s.GetInferenceKey(ctx, &view.GetInferenceKeyRequest{KeyId: inferenceKeyTestID})
	require.Equal(t, "INFERENCE_KEY_UNAVAILABLE", kratoserrors.Reason(err))
	owner.key = nil
	_, err = s.GetInferenceKey(ctx, &view.GetInferenceKeyRequest{KeyId: inferenceKeyTestID})
	require.Equal(t, "INFERENCE_KEY_UNAVAILABLE", kratoserrors.Reason(err))
}
