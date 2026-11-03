package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
)

const SandboxTokenPrefix = "ani.sbx."

type SandboxTokenVerifier interface {
	VerifySandboxToken(context.Context, string, string, string) (*Principal, error)
}

// SandboxTokenAction is an exact capability allowlist: token management,
// lifecycle, checkpoints and unrelated APIs are never reachable with a token.
func SandboxTokenAction(operation, method string) (string, bool) {
	switch operation {
	case admin.OperationSandboxServiceRunSandboxCode:
		return "exec", method == http.MethodPost
	case admin.OperationSandboxServiceGetSandboxCodeRun:
		return "exec", method == http.MethodGet
	case admin.OperationSandboxServiceListSandboxFiles, admin.OperationSandboxServiceReadSandboxFile, admin.OperationSandboxServiceGetSandboxFileOperation:
		return "files", method == http.MethodGet
	case admin.OperationSandboxServiceWriteSandboxFile:
		return "files", method == http.MethodPost
	case admin.OperationSandboxServiceDeleteSandboxFile:
		return "files", method == http.MethodDelete
	default:
		return "", false
	}
}
func sandboxTokenRoute(request *http.Request, operation string) (string, string, error) {
	scope, ok := SandboxTokenAction(operation, request.Method)
	if !ok {
		return "", "", kratoserrors.Forbidden("SANDBOX_TOKEN_SCOPE_DENIED", "sandbox token cannot access this operation")
	}
	if request.URL.RawPath != "" || strings.Contains(request.URL.EscapedPath(), "%") {
		return "", "", kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid sandbox path")
	}
	parts := strings.Split(request.URL.Path, "/")
	if len(parts) < 6 || parts[0] != "" || parts[1] != "api" || parts[2] != "v1" || parts[3] != "sandbox" {
		return "", "", kratoserrors.Forbidden("SANDBOX_TOKEN_SCOPE_DENIED", "invalid sandbox path")
	}
	id, err := uuid.Parse(parts[4])
	if err != nil || id == uuid.Nil || id.String() != parts[4] {
		return "", "", kratoserrors.BadRequest("INVALID_SANDBOX_REQUEST", "invalid sandbox identity")
	}
	return parts[4], scope, nil
}
func hasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}
func SandboxTokenMatches(p *Principal, sandboxID, scope string) bool {
	return p != nil && p.Type == SubjectSandbox && p.SandboxID == sandboxID && p.SandboxTokenID != "" && p.ResourceTenantID != "" && p.TenantID > 0 && p.IssuerID > 0 && p.SandboxExpiresAt > time.Now().Unix() && hasScope(p.SandboxScopes, scope)
}
