package auth

import (
	"context"
	"fmt"
	authenticationV1 "go-wind-admin/api/gen/go/authentication/service/v1"
)

type SubjectType string

const (
	SubjectUser    SubjectType = "user"
	SubjectAPIKey  SubjectType = "api_key"
	SubjectSandbox SubjectType = "sandbox_token"
)

// Principal is populated only after credential verification. IDs are never interchangeable.
type Principal struct {
	Type             SubjectType
	ID               uint32
	TenantID         uint32
	Roles            []string
	Claims           *authenticationV1.UserTokenPayload
	SandboxID        string
	SandboxTokenID   string
	SandboxExpiresAt int64
	ResourceTenantID string
	IssuerID         uint32
	SandboxScopes    []string
}
type principalKey struct{}
type verifiedIdentity struct{ principal *Principal }
type verifiedIdentityKey struct{}

// WithIdentityRecorder lets an outer audit middleware observe authentication after the handler.
func WithIdentityRecorder(ctx context.Context) context.Context {
	return context.WithValue(ctx, verifiedIdentityKey{}, &verifiedIdentity{})
}
func NewPrincipalContext(ctx context.Context, p *Principal) context.Context {
	if recorder, ok := ctx.Value(verifiedIdentityKey{}).(*verifiedIdentity); ok {
		recorder.principal = p
	}
	return context.WithValue(ctx, principalKey{}, p)
}
func PrincipalFromContext(ctx context.Context) (*Principal, error) {
	if p, ok := ctx.Value(principalKey{}).(*Principal); ok && p != nil {
		return p, nil
	}
	if recorder, ok := ctx.Value(verifiedIdentityKey{}).(*verifiedIdentity); ok && recorder.principal != nil {
		return recorder.principal, nil
	}
	return nil, ErrMissingJwtToken
}
func (p *Principal) Actor() (string, error) {
	if p == nil {
		return "", ErrExtractSubjectFailed
	}
	if p.Type == SubjectSandbox && p.SandboxTokenID != "" {
		return "governance:sandbox-token:" + p.SandboxTokenID, nil
	}
	if p.ID == 0 {
		return "", ErrExtractSubjectFailed
	}
	switch p.Type {
	case SubjectUser:
		return fmt.Sprintf("governance:user:%d", p.ID), nil
	case SubjectAPIKey:
		return fmt.Sprintf("governance:access-key:%d", p.ID), nil
	default:
		return "", ErrExtractSubjectFailed
	}
}
