package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testSandboxTokenID = "44444444-4444-4444-8444-444444444444"

type sandboxTokenVerifierStub struct {
	scope   string
	sandbox string
	calls   int
	failure error
}

func (v *sandboxTokenVerifierStub) VerifySandboxToken(_ context.Context, token, id, scope string) (*auth.Principal, error) {
	v.calls++
	if v.failure != nil {
		return nil, v.failure
	}
	if token != "ani.sbx.fake.signature" || id != v.sandbox || scope != v.scope {
		return nil, status.Error(codes.Unauthenticated, "SANDBOX_TOKEN_INVALID")
	}
	return &auth.Principal{Type: auth.SubjectSandbox, TenantID: 7, IssuerID: 9, SandboxID: v.sandbox, SandboxTokenID: testSandboxTokenID, SandboxExpiresAt: time.Now().Add(time.Minute).Unix(), ResourceTenantID: "33333333-3333-4333-8333-333333333333", SandboxScopes: []string{v.scope}}, nil
}

type sandboxTokenTenantChecker struct{ denied bool }

func (t *sandboxTokenTenantChecker) CheckTenantAccess(context.Context, uint32, string, string) error {
	if t.denied {
		return status.Error(codes.PermissionDenied, "TENANT_DENIED")
	}
	return nil
}

type sandboxTokenOwner struct {
	sandboxHTTPResource
	actor string
}

func (o *sandboxTokenOwner) QuerySandboxFiles(_ context.Context, tenant, actor string, r *res.QueryFilesRequest) (*res.FileResult, error) {
	o.calls++
	o.actor = actor
	if tenant != r.TenantId {
		panic("tenant mismatch")
	}
	return &res.FileResult{Path: r.Path, ContentBase64: "aGVsbG8="}, nil
}
func TestSandboxTokenAuthenticatedFileAccessAndDenials(t *testing.T) {
	verifier := &sandboxTokenVerifierStub{scope: "files", sandbox: sandboxHTTPResourceID}
	tenant := new(sandboxTokenTenantChecker)
	owner := new(sandboxTokenOwner)
	authorization := new(sandboxHTTPAuthorization)
	server := khttp.NewServer(khttp.Middleware(auth.Server(auth.WithSandboxTokenVerifier(verifier), auth.WithTenantAccessChecker(tenant))))
	RegisterSandboxHTTPServer(server, service.NewSandboxService(owner, authorization, sandboxHTTPTenants{}, sandboxHTTPAdmission{}))
	fire := func(method, path, body string, code int) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer ani.sbx.fake.signature")
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		require.Equal(t, code, w.Code, w.Body.String())
	}
	base := "/api/v1/sandbox/" + sandboxHTTPResourceID
	fire("GET", base+"/files/content?path=hello", "", 200)
	require.Equal(t, "governance:sandbox-token:"+testSandboxTokenID, owner.actor)
	fire("GET", "/api/v1/sandbox/55555555-5555-4555-8555-555555555555/files/content?path=hello", "", 401)
	fire("POST", base+"/tokens", `{"data":{"idempotency_key":"k"}}`, 403)
	fire("POST", base+":pause", `{"data":{"idempotency_key":"k"}}`, 403)
	fire("POST", base+"/code-runs", `{"data":{"idempotency_key":"k","language":"python","code":"1"}}`, 401)
	authorization.denied = true
	fire("GET", base+"/files/content?path=hello", "", 403)
	authorization.denied = false
	tenant.denied = true
	fire("GET", base+"/files/content?path=hello", "", 403)
}

func TestSandboxTokenAuthzMiddlewareOnlyBypassesForSandboxSubject(t *testing.T) {
	normalCalls, nextCalls := 0, 0
	ordinary := func(next middleware.Handler) middleware.Handler {
		return func(c context.Context, r any) (any, error) { normalCalls++; return next(c, r) }
	}
	next := func(context.Context, any) (any, error) { nextCalls++; return nil, nil }
	handler := sandboxAwareAuthorization(ordinary)(next)
	_, err := handler(auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}), nil)
	require.NoError(t, err)
	_, err = handler(auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectSandbox, SandboxTokenID: testSandboxTokenID}), nil)
	require.NoError(t, err)
	require.Equal(t, 1, normalCalls)
	require.Equal(t, 2, nextCalls)
}

type sandboxTokenPartialAuthorization struct{}

func (sandboxTokenPartialAuthorization) Authorize(_ context.Context, _, _ uint32, path, method string) error {
	if path == data.SandboxFilesMutatePath && method == "DELETE" {
		return data.ErrActionAuthorizationDenied
	}
	return nil
}
func TestSandboxTokenCannotDelegatePermissionIssuerLacks(t *testing.T) {
	owner := new(sandboxHTTPResource)
	h := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, r any) (any, error) {
			return next(auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}), r)
		}
	}))
	RegisterSandboxHTTPServer(h, service.NewSandboxService(owner, sandboxTokenPartialAuthorization{}, sandboxHTTPTenants{}, sandboxHTTPAdmission{}))
	r := httptest.NewRequest("POST", "/api/v1/sandbox/"+sandboxHTTPResourceID+"/tokens", strings.NewReader(`{"data":{"idempotency_key":"k","scopes":["files"]}}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, 403, w.Code, w.Body.String())
	require.Zero(t, owner.calls)
}
