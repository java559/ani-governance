package service

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var _ admin.InferenceKeyServiceHTTPServer = (*InferenceService)(nil)

type inferenceKeyOwner interface {
	CreateInferenceKeyTrusted(context.Context, string, string, *runtimev1.CreateInferenceAccessKeyRequest) (*runtimev1.CreateInferenceAccessKeyResponse, error)
	ListInferenceKeysTrusted(context.Context, string, string, *runtimev1.ListInferenceAccessKeysRequest) (*runtimev1.ListInferenceAccessKeysResponse, error)
	GetInferenceKeyTrusted(context.Context, string, string, *runtimev1.GetInferenceAccessKeyRequest) (*runtimev1.InferenceAccessKey, error)
	RevokeInferenceKeyTrusted(context.Context, string, string, *runtimev1.RevokeInferenceAccessKeyRequest) (*runtimev1.RevokeInferenceAccessKeyResponse, error)
	RotateInferenceKeyTrusted(context.Context, string, string, *runtimev1.RotateInferenceAccessKeyRequest) (*runtimev1.RotateInferenceAccessKeyResponse, error)
}

func (s *InferenceService) inferenceKeyScope(ctx context.Context) (inferenceKeyOwner, string, string, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return nil, "", "", admin.ErrorUnauthorized("login required")
	}
	if p.Type != auth.SubjectUser || p.TenantID == 0 {
		return nil, "", "", view.ErrorInferenceKeyUserRequired("key management requires a tenant user")
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, "", "", view.ErrorInferenceKeyUnavailable("tenant mapping unavailable").WithCause(err)
	}
	if !canonicalInferenceKeyID(tenant) {
		return nil, "", "", view.ErrorInferenceKeyUnavailable("tenant mapping unavailable")
	}
	owner, ok := s.owner.(inferenceKeyOwner)
	if !ok {
		return nil, "", "", view.ErrorInferenceKeyUnavailable("inference key management unavailable")
	}
	return owner, tenant, actor, nil
}

func (s *InferenceService) CreateInferenceKey(ctx context.Context, in *view.CreateInferenceKeyRequest) (*view.InferenceKeyCredentials, error) {
	if in == nil || in.Data == nil {
		return nil, view.ErrorInvalidInferenceKeyRequest("data is required")
	}
	d := in.Data
	if strings.TrimSpace(d.Name) == "" || len(d.Name) > 256 || !validInferenceKeyIdempotency(d.IdempotencyKey) || len(d.AllowedServiceIds)+len(d.AllowedModelNames) == 0 || d.RequestsPerSecond < 0 || d.RequestsPerMinute < 0 || d.RequestsPerHour < 0 {
		return nil, view.ErrorInvalidInferenceKeyRequest("name, scope and idempotency_key are required; rate limits must be nonnegative")
	}
	if d.ExpiresAt != nil && d.ExpiresAt.CheckValid() != nil {
		return nil, view.ErrorInvalidInferenceKeyRequest("invalid expires_at")
	}
	for _, id := range d.AllowedServiceIds {
		if !canonicalInferenceKeyID(id) {
			return nil, view.ErrorInvalidInferenceKeyRequest("allowed_service_ids must contain canonical UUIDs")
		}
	}
	for _, model := range d.AllowedModelNames {
		if strings.TrimSpace(model) == "" || len(model) > 256 {
			return nil, view.ErrorInvalidInferenceKeyRequest("allowed_model_names must contain model names")
		}
	}
	owner, tenant, actor, err := s.inferenceKeyScope(ctx)
	if err != nil {
		return nil, err
	}
	request := &runtimev1.CreateInferenceAccessKeyRequest{RequestId: uuid.NewString(), Name: strings.TrimSpace(d.Name), AllowedServiceIds: normalizedInferenceKeyScope(d.AllowedServiceIds), AllowedModelNames: normalizedInferenceKeyScope(d.AllowedModelNames), RequestsPerSecond: d.RequestsPerSecond, RequestsPerMinute: d.RequestsPerMinute, RequestsPerHour: d.RequestsPerHour, ExpiresAt: d.ExpiresAt, IdempotencyKey: d.IdempotencyKey}
	result, err := owner.CreateInferenceKeyTrusted(ctx, tenant, actor, request)
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	if result == nil {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference key response")
	}
	// The owner's existing create replay is keyed by tenant + idempotency key.
	// Compare its durable intent before exposing a replay as this HTTP request.
	if result.Replayed && !sameInferenceKeyIntent(request, result.Key) {
		return nil, view.ErrorInferenceKeyConflict("idempotency_key was used for a different key intent")
	}
	return inferenceKeyCredentials(result.Key, result.Secret, result.Replayed, "")
}

func (s *InferenceService) ListInferenceKeys(ctx context.Context, in *view.ListInferenceKeysRequest) (*view.ListInferenceKeysResponse, error) {
	if in == nil {
		return nil, view.ErrorInvalidInferenceKeyRequest("request is required")
	}
	page, size := int32(1), int32(20)
	if in.Page != nil {
		page = *in.Page
	}
	if in.PageSize != nil {
		size = *in.PageSize
	}
	// The published Inference RPC currently limits both values to 100.
	if page < 1 || page > 100 || size < 1 || size > 100 {
		return nil, view.ErrorInvalidInferenceKeyRequest("page and pageSize must be within 1..100")
	}
	owner, tenant, actor, err := s.inferenceKeyScope(ctx)
	if err != nil {
		return nil, err
	}
	result, err := owner.ListInferenceKeysTrusted(ctx, tenant, actor, &runtimev1.ListInferenceAccessKeysRequest{Page: page, PageSize: size})
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	if result == nil || result.Total < 0 || len(result.Items) > int(size) || int64(len(result.Items)) > result.Total {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference key response")
	}
	out := &view.ListInferenceKeysResponse{Items: make([]*view.InferenceKey, 0, len(result.Items)), Total: result.Total}
	for _, item := range result.Items {
		key, err := projectInferenceKey(item, "", true)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, key)
	}
	return out, nil
}

func (s *InferenceService) GetInferenceKey(ctx context.Context, in *view.GetInferenceKeyRequest) (*view.InferenceKey, error) {
	if in == nil || !canonicalInferenceKeyID(in.KeyId) {
		return nil, view.ErrorInvalidInferenceKeyRequest("key_id must be a canonical UUID")
	}
	owner, tenant, actor, err := s.inferenceKeyScope(ctx)
	if err != nil {
		return nil, err
	}
	result, err := owner.GetInferenceKeyTrusted(ctx, tenant, actor, &runtimev1.GetInferenceAccessKeyRequest{Id: in.KeyId})
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	return projectInferenceKey(result, in.KeyId, true)
}

func (s *InferenceService) RevokeInferenceKey(ctx context.Context, in *view.RevokeInferenceKeyRequest) (*view.InferenceKey, error) {
	if in == nil || !canonicalInferenceKeyID(in.KeyId) {
		return nil, view.ErrorInvalidInferenceKeyRequest("key_id must be a canonical UUID")
	}
	owner, tenant, actor, err := s.inferenceKeyScope(ctx)
	if err != nil {
		return nil, err
	}
	result, err := owner.RevokeInferenceKeyTrusted(ctx, tenant, actor, &runtimev1.RevokeInferenceAccessKeyRequest{Id: in.KeyId, RequestId: uuid.NewString()})
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	if result == nil || result.GetKey().GetRevokedAt() == nil {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference key response")
	}
	return projectInferenceKey(result.Key, in.KeyId, false)
}

func (s *InferenceService) RotateInferenceKey(ctx context.Context, in *view.RotateInferenceKeyRequest) (*view.InferenceKeyCredentials, error) {
	if in == nil || in.Data == nil || !canonicalInferenceKeyID(in.KeyId) || !validInferenceKeyIdempotency(in.Data.IdempotencyKey) {
		return nil, view.ErrorInvalidInferenceKeyRequest("key_id, data and idempotency_key are required")
	}
	owner, tenant, actor, err := s.inferenceKeyScope(ctx)
	if err != nil {
		return nil, err
	}
	result, err := owner.RotateInferenceKeyTrusted(ctx, tenant, actor, &runtimev1.RotateInferenceAccessKeyRequest{Id: in.KeyId, RequestId: uuid.NewString(), IdempotencyKey: in.Data.IdempotencyKey})
	if err != nil {
		return nil, mapInferenceKeyError(err)
	}
	if result == nil {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference key response")
	}
	return inferenceKeyCredentials(result.Key, result.Secret, result.Replayed, in.KeyId)
}

func canonicalInferenceKeyID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func validInferenceKeyIdempotency(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value
}

func normalizedInferenceKeyScope(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.TrimSpace(value))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func sameInferenceKeyIntent(in *runtimev1.CreateInferenceAccessKeyRequest, key *runtimev1.InferenceAccessKey) bool {
	return key != nil && in.Name == key.Name && slices.Equal(normalizedInferenceKeyScope(in.AllowedServiceIds), normalizedInferenceKeyScope(key.AllowedServiceIds)) && slices.Equal(normalizedInferenceKeyScope(in.AllowedModelNames), normalizedInferenceKeyScope(key.AllowedModelNames)) && in.RequestsPerSecond == key.RequestsPerSecond && in.RequestsPerMinute == key.RequestsPerMinute && in.RequestsPerHour == key.RequestsPerHour && proto.Equal(in.ExpiresAt, key.ExpiresAt)
}

func projectInferenceKey(key *runtimev1.InferenceAccessKey, id string, includeSecret bool) (*view.InferenceKey, error) {
	if key == nil || !canonicalInferenceKeyID(key.Id) || (id != "" && key.Id != id) || !strings.HasPrefix(key.KeyPrefix, "iak_") || len(key.KeyPrefix) != 16 || key.Version < 1 || key.CreatedAt == nil {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference key response")
	}
	if key.CreatedAt.CheckValid() != nil || (key.ExpiresAt != nil && key.ExpiresAt.CheckValid() != nil) || (key.RevokedAt != nil && key.RevokedAt.CheckValid() != nil) || (key.LastUsedAt != nil && key.LastUsedAt.CheckValid() != nil) {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference key response")
	}
	out := &view.InferenceKey{Id: key.Id, Name: key.Name, KeyPrefix: key.KeyPrefix, AllowedServiceIds: append([]string(nil), key.AllowedServiceIds...), AllowedModelNames: append([]string(nil), key.AllowedModelNames...), RequestsPerSecond: key.RequestsPerSecond, RequestsPerMinute: key.RequestsPerMinute, RequestsPerHour: key.RequestsPerHour, ExpiresAt: key.ExpiresAt, RevokedAt: key.RevokedAt, CreatedBy: key.CreatedBy, CreatedAt: key.CreatedAt, LastUsedAt: key.LastUsedAt, Version: key.Version}
	if includeSecret && key.Secret != nil {
		secret := key.GetSecret()
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(secret, "iak_"))
		if err != nil || len(decoded) != 32 || len(secret) != 47 || !strings.HasPrefix(secret, key.KeyPrefix) {
			return nil, view.ErrorInferenceKeyUnavailable("invalid inference key response")
		}
		out.Secret = &secret
	}
	return out, nil
}

func inferenceKeyCredentials(key *runtimev1.InferenceAccessKey, secret string, replayed bool, id string) (*view.InferenceKeyCredentials, error) {
	projected, err := projectInferenceKey(key, id, false)
	if err != nil {
		return nil, err
	}
	if replayed {
		secret = ""
	} else if len(secret) <= 16 || !strings.HasPrefix(secret, projected.KeyPrefix) || !strings.HasPrefix(secret, "iak_") {
		return nil, view.ErrorInferenceKeyUnavailable("invalid inference key response")
	}
	return &view.InferenceKeyCredentials{Key: projected, Secret: secret, Replayed: replayed}, nil
}

func mapInferenceKeyError(err error) error {
	var out *kratoserrors.Error
	switch status.Code(err) {
	case codes.InvalidArgument:
		out = view.ErrorInvalidInferenceKeyRequest("invalid inference key request")
	case codes.NotFound:
		out = view.ErrorInferenceKeyNotFound("inference key not found")
	case codes.PermissionDenied:
		out = view.ErrorInferenceKeyUserRequired("inference key access denied")
	case codes.AlreadyExists, codes.FailedPrecondition, codes.Aborted:
		out = view.ErrorInferenceKeyConflict("inference key state or idempotency conflict")
	default:
		out = view.ErrorInferenceKeyUnavailable("inference key management unavailable")
	}
	return out.WithCause(err)
}
