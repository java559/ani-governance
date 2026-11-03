package service

import (
	"context"
	"testing"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
)

type expiryOwner struct{ SandboxOwnerClient }
type expiryAuthorizer struct{ principal *auth.Principal }

func (a expiryAuthorizer) Authorize(context.Context, uint32, uint32, string, string) error {
	a.principal.SandboxExpiresAt = time.Now().Add(-time.Second).Unix()
	return nil
}

type expiryTenants struct{}

func (expiryTenants) ResourceTenantID(context.Context, uint32) (string, error) {
	return "11111111-1111-4111-8111-111111111111", nil
}
func TestSandboxTokenExpiresDuringCurrentGrantCheck(t *testing.T) {
	principal := &auth.Principal{Type: auth.SubjectSandbox, TenantID: 7, IssuerID: 9, SandboxID: "22222222-2222-4222-8222-222222222222", SandboxTokenID: "33333333-3333-4333-8333-333333333333", SandboxExpiresAt: time.Now().Add(time.Minute).Unix(), ResourceTenantID: "11111111-1111-4111-8111-111111111111", SandboxScopes: []string{"files"}}
	service := NewSandboxService(expiryOwner{}, expiryAuthorizer{principal}, expiryTenants{}, nil)
	ctx := auth.NewPrincipalContext(context.Background(), principal)
	_, _, err := service.sandboxDataScope(ctx, data.SandboxFilesContentPath, "GET", principal.SandboxID)
	require.Equal(t, 401, int(kratoserrors.Code(err)))
}
