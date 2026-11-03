package service

import (
	"context"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/v1"
	view "go-wind-admin/api/gen/go/sandbox/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type sandboxTokenClient interface {
	CreateSandboxToken(context.Context, string, string, *res.CreateTokenRequest) (*res.SandboxAccessToken, error)
	RevokeSandboxToken(context.Context, string, string, *res.RevokeTokenRequest) error
}

func (s *SandboxService) CreateSandboxToken(ctx context.Context, r *view.CreateSandboxTokenRequest) (*view.SandboxAccessToken, error) {
	if r == nil || r.Data == nil || !sandboxUUID(r.Data.SandboxId) || sandboxKey(r.Data.IdempotencyKey) != nil || len([]rune(r.Data.IdempotencyKey)) > 128 {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid token request")
	}
	tenant, actor, err := s.scope(ctx, data.SandboxTokenCreatePath, "POST")
	if err != nil {
		return nil, err
	}
	p, err := gpuPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	duration := 15 * time.Minute
	if r.Data.ExpiresIn != "" {
		duration, err = time.ParseDuration(r.Data.ExpiresIn)
		if err != nil {
			return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid token lifetime")
		}
	}
	if duration <= 0 || duration > time.Hour || duration%time.Second != 0 {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid token lifetime")
	}
	scopes := r.Data.Scopes
	if len(scopes) == 0 {
		scopes = []string{"connect"}
	}
	for _, scope := range scopes {
		var requirements [][2]string
		switch scope {
		case "connect":
		case "exec":
			requirements = [][2]string{{data.SandboxCodeRunPath, "POST"}, {data.SandboxCodeRunGetPath, "GET"}}
		case "files":
			requirements = [][2]string{{data.SandboxFilesListPath, "GET"}, {data.SandboxFilesContentPath, "GET"}, {data.SandboxFilesMutatePath, "POST"}, {data.SandboxFilesMutatePath, "DELETE"}, {data.SandboxFilesOperationPath, "GET"}}
		case "ports":
			return nil, kratoserrors.New(501, "SANDBOX_UNSUPPORTED", "sandbox ports not available")
		default:
			return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid token scope")
		}
		for _, item := range requirements {
			if err = instanceAuthorizationError(s.authorization.Authorize(ctx, p.TenantID, p.ID, item[0], item[1])); err != nil {
				return nil, err
			}
		}
	}
	client, ok := s.client.(sandboxTokenClient)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_TOKEN_UNAVAILABLE", "sandbox token unavailable")
	}
	id := stableSandboxID(tenant, r.Data.SandboxId+"/"+r.Data.IdempotencyKey, "access-token")
	result, err := client.CreateSandboxToken(ctx, tenant, actor, &res.CreateTokenRequest{TenantId: tenant, SandboxId: r.Data.SandboxId, TokenId: id, IdempotencyKey: r.Data.IdempotencyKey, GovTenantId: p.TenantID, IssuerId: p.ID, Scopes: scopes, ExpiresInSeconds: int64(duration / time.Second)})
	if err != nil {
		return nil, err
	}
	if result == nil || result.TokenId != id || result.Value == "" || result.ExpiresAt <= time.Now().Unix() {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_INVALID_RESPONSE", "invalid token response")
	}
	return &view.SandboxAccessToken{TokenId: result.TokenId, Token: result.Value, ExpiresAt: timestamppb.New(time.Unix(result.ExpiresAt, 0)), Scopes: result.Scopes, Replayed: result.Replayed}, nil
}
func (s *SandboxService) RevokeSandboxToken(ctx context.Context, r *view.RevokeSandboxTokenRequest) (*view.RevokeSandboxTokenResponse, error) {
	if r == nil || !sandboxUUID(r.SandboxId) || !sandboxUUID(r.TokenId) {
		return nil, kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid token request")
	}
	tenant, actor, err := s.scope(ctx, data.SandboxTokenRevokePath, "DELETE")
	if err != nil {
		return nil, err
	}
	client, ok := s.client.(sandboxTokenClient)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("SANDBOX_TOKEN_UNAVAILABLE", "sandbox token unavailable")
	}
	if err = client.RevokeSandboxToken(ctx, tenant, actor, &res.RevokeTokenRequest{TenantId: tenant, SandboxId: r.SandboxId, TokenId: r.TokenId}); err != nil {
		return nil, err
	}
	return &view.RevokeSandboxTokenResponse{Revoked: true}, nil
}
