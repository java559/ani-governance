package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

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

const sandboxHTTPResourceID = "22222222-2222-4222-8222-222222222222"

type sandboxHTTPAuthorization struct{ denied bool }

func (a *sandboxHTTPAuthorization) Authorize(context.Context, uint32, uint32, string, string) error {
	if a.denied {
		return data.ErrActionAuthorizationDenied
	}
	return nil
}

type sandboxHTTPAdmission struct{}

func (sandboxHTTPAdmission) CanCreateSandbox(context.Context, string) error { return nil }
func (sandboxHTTPAdmission) FindSandboxCreateIntent(context.Context, uint32, string) (*data.SandboxCreateIntent, error) {
	return nil, nil
}
func (sandboxHTTPAdmission) RegisterSandboxCreateIntent(_ context.Context, in data.SandboxCreateIntent) (*data.SandboxCreateIntent, bool, error) {
	return &in, false, nil
}
func (sandboxHTTPAdmission) AcknowledgeSandboxCreate(context.Context, uint32, string, string) error {
	return nil
}

type sandboxHTTPTenants struct{}

func (sandboxHTTPTenants) ResourceTenantID(context.Context, uint32) (string, error) {
	return "33333333-3333-4333-8333-333333333333", nil
}

type sandboxHTTPResource struct {
	service.SandboxOwnerClient
	calls   int
	list    *res.ListSandboxesRequest
	failure error
	create  *res.AcceptCreateRequest
	created *res.Sandbox
}

func (o *sandboxHTTPResource) ListSandboxes(_ context.Context, t, a string, r *res.ListSandboxesRequest) (*res.ListSandboxesResponse, error) {
	if t != "33333333-3333-4333-8333-333333333333" || a != "governance:user:9" {
		panic("untrusted Sandbox scope")
	}
	o.calls++
	o.list = r
	return &res.ListSandboxesResponse{Total: 3}, o.failure
}
func (o *sandboxHTTPResource) GetSandbox(_ context.Context, _ string, _ string, r *res.GetSandboxRequest) (*res.GetSandboxResponse, error) {
	o.calls++
	if o.failure != nil {
		return nil, o.failure
	}
	if o.created != nil && o.created.Id == r.SandboxId {
		return &res.GetSandboxResponse{Sandbox: o.created}, nil
	}
	return nil, status.Error(codes.NotFound, "SANDBOX_NOT_FOUND")
}
func (o *sandboxHTTPResource) ResolveSandbox(_ context.Context, _, _ string, r *res.ResolveCreateSpecRequest) (*res.ResolveCreateSpecResponse, error) {
	o.calls++
	return &res.ResolveCreateSpecResponse{Name: r.Data.Name, ImageReference: r.Data.ImageReference, PresetId: r.Data.PresetId, Workspace: &res.WorkspaceSpec{Size: "10Gi"}}, nil
}
func (o *sandboxHTTPResource) AcceptSandboxCreate(_ context.Context, _, _ string, r *res.AcceptCreateRequest) (*res.AcceptCreateResponse, error) {
	o.calls++
	o.create = r
	o.created = &res.Sandbox{TenantId: r.Context.TenantId, Id: r.Context.ResourceId, InstanceId: r.InstanceId, CreateOperationId: r.Context.OperationId, CreateIntentDigest: r.CreateIntentDigest}
	return &res.AcceptCreateResponse{Ack: &res.OperationAck{Accepted: true, ResourceId: r.Context.ResourceId, OperationId: r.Context.OperationId}}, nil
}
func (o *sandboxHTTPResource) PauseSandbox(_ context.Context, _, _ string, r *res.PauseRequest) (*res.PauseResponse, error) {
	o.calls++
	return &res.PauseResponse{Ack: &res.OperationAck{ResourceId: r.SandboxId, OperationId: "pause-op", Accepted: true, Replayed: true}}, o.failure
}
func (o *sandboxHTTPResource) ResumeSandbox(_ context.Context, _, _ string, r *res.ResumeRequest) (*res.ResumeResponse, error) {
	o.calls++
	return &res.ResumeResponse{Ack: &res.OperationAck{ResourceId: r.SandboxId, OperationId: "resume-op", Accepted: true}}, o.failure
}
func (o *sandboxHTTPResource) DeleteSandbox(_ context.Context, _, _ string, r *res.DeleteRequest) (*res.DeleteResponse, error) {
	o.calls++
	return &res.DeleteResponse{Ack: &res.OperationAck{ResourceId: r.SandboxId, OperationId: "delete-op", Accepted: true}}, o.failure
}

func TestSandboxHTTPPaginationAndCommandBoundary(t *testing.T) {
	owner := new(sandboxHTTPResource)
	authorization := new(sandboxHTTPAuthorization)
	server := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, r any) (any, error) {
			return next(auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}), r)
		}
	}))
	RegisterSandboxHTTPServer(server, service.NewSandboxService(owner, authorization, sandboxHTTPTenants{}, sandboxHTTPAdmission{}))
	collection := "/api/v1/sandbox"
	detail := collection + "/" + sandboxHTTPResourceID
	fire := func(method, path, body string, code int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		require.Equal(t, code, w.Code, w.Body.String())
		return w
	}
	fire("GET", collection, "", 200)
	require.EqualValues(t, 1, owner.list.Page)
	require.EqualValues(t, 20, owner.list.PageSize)
	fire("GET", collection+"?page=3&pageSize=2", "", 200)
	require.EqualValues(t, 3, owner.list.Page)
	require.EqualValues(t, 2, owner.list.PageSize)
	fire("GET", collection+"?pageSize=7", "", 200)
	require.EqualValues(t, 1, owner.list.Page)
	require.EqualValues(t, 7, owner.list.PageSize)
	response := fire("GET", collection+"?page=4294967295", "", 200)
	require.EqualValues(t, 4294967295, owner.list.Page)
	require.EqualValues(t, 20, owner.list.PageSize)
	var result map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Equal(t, "3", result["total"])
	require.Equal(t, []any{}, result["items"])
	for _, query := range []string{"page=0", "page=01", "page=", "page=-1", "page=1.2", "page=4294967296", "page=1&page=2", "pageSize=0", "pageSize=101", "pageSize=020", "page_size=20", "tenant_id=7", "cursor=abc"} {
		before := owner.calls
		response = fire("GET", collection+"?"+query, "", 400)
		require.Equal(t, before, owner.calls, query)
		require.Contains(t, response.Body.String(), "INVALID_SANDBOX_REQUEST")
	}
	for _, input := range []struct{ method, path, body string }{
		{"GET", collection, `{}`},
		{"POST", collection, `{"data":{"instance_id":"different","idempotency_key":"create-1"}}`},
		{"GET", detail + "?extra=1", ""},
		{"POST", detail + ":pause", `{"data":{"idempotency_key":"k","expected_version":"1","sandbox_id":"different"}}`},
		{"POST", detail + ":resume", `{"data":{"idempotency_key":"k","expected_version":"1","instance_id":"different"}}`},
		{"DELETE", detail, `{"data":{"idempotency_key":"k","expected_version":"1","tenant_id":"untrusted"}}`},
		{"POST", detail + ":pause?data.expected_version=2", `{"data":{"idempotency_key":"k","expected_version":"1"}}`},
	} {
		before := owner.calls
		fire(input.method, input.path, input.body, 400)
		require.Equal(t, before, owner.calls)
	}
	response = fire("POST", detail+":pause", `{"data":{"idempotency_key":"k","expected_version":"1"}}`, 200)
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Equal(t, true, result["replayed"])
	fire("POST", detail+":resume", `{"data":{"idempotency_key":"k","expected_version":"1"}}`, 200)
	fire("DELETE", detail, `{"data":{"idempotency_key":"k","expected_version":"1"}}`, 200)
	createBody := `{"data":{"idempotency_key":"create-1","name":"demo","preset_id":"44444444-4444-4444-8444-444444444444","image_reference":"registry.example/sandbox@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
	fire("POST", collection, createBody, 200)
	require.Empty(t, owner.create.InstanceId)
	require.NotEmpty(t, owner.create.CreateIntentDigest)
	response = fire("POST", collection, createBody, 200)
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
	require.Equal(t, true, result["replayed"])
	authorization.denied = true
	before := owner.calls
	fire("GET", collection, "", 403)
	require.Equal(t, before, owner.calls)
}

func TestSandboxHTTPErrorsPreservePublicReasons(t *testing.T) {
	owner := new(sandboxHTTPResource)
	server := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, r any) (any, error) {
			return next(auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}), r)
		}
	}))
	RegisterSandboxHTTPServer(server, service.NewSandboxService(owner, new(sandboxHTTPAuthorization), sandboxHTTPTenants{}, sandboxHTTPAdmission{}))
	for _, tc := range []struct {
		code            codes.Code
		message, reason string
		http            int
	}{
		{codes.FailedPrecondition, "SANDBOX_VERSION_CONFLICT", "SANDBOX_VERSION_CONFLICT", 409},
		{codes.AlreadyExists, "SANDBOX_IDEMPOTENCY_CONFLICT", "SANDBOX_IDEMPOTENCY_CONFLICT", 409},
		{codes.NotFound, "SANDBOX_NOT_FOUND", "SANDBOX_NOT_FOUND", 404},
		{codes.FailedPrecondition, "COMPUTE_PRESET_DISABLED", "COMPUTE_PRESET_DISABLED", 409},
		{codes.Unavailable, "postgres://private-password@database", "SANDBOX_DEPENDENCY_UNAVAILABLE", 503},
	} {
		owner.failure = status.Error(tc.code, tc.message)
		r := httptest.NewRequest("POST", "/api/v1/sandbox/"+sandboxHTTPResourceID+":pause", strings.NewReader(`{"data":{"idempotency_key":"k","expected_version":"1"}}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		require.Equal(t, tc.http, w.Code, w.Body.String())
		var result map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
		require.Equal(t, tc.reason, result["reason"])
		require.NotContains(t, w.Body.String(), "private-password")
	}
}

type sandboxHTTPFailedResolution struct {
	*sandboxHTTPResource
	fail      bool
	committed *res.Sandbox
	failure   error
}

func (o *sandboxHTTPFailedResolution) ResolveSandbox(ctx context.Context, tenant, actor string, r *res.ResolveCreateSpecRequest) (*res.ResolveCreateSpecResponse, error) {
	if !o.fail {
		return o.sandboxHTTPResource.ResolveSandbox(ctx, tenant, actor, r)
	}
	// Another first delivery commits after the preflight read, before the
	// catalog resolution fails. No acceptance call is made for this attempt.
	o.created = o.committed
	return nil, o.failure
}

func TestSandboxHTTPFailedResolutionReconcilesAcceptedIntent(t *testing.T) {
	for _, tc := range []struct {
		name             string
		failure          error
		missing, changed bool
		http             int
		reason           string
	}{
		{"disabled_winner", status.Error(codes.FailedPrecondition, "COMPUTE_PRESET_DISABLED"), false, false, 200, ""},
		{"deleted_winner", status.Error(codes.NotFound, "COMPUTE_PRESET_NOT_FOUND"), false, false, 200, ""},
		{"unavailable_winner", status.Error(codes.Unavailable, "catalog unavailable"), false, false, 200, ""},
		{"nil_resolution_winner", nil, false, false, 200, ""},
		{"changed_intent", status.Error(codes.FailedPrecondition, "COMPUTE_PRESET_DISABLED"), false, true, 409, "SANDBOX_IDEMPOTENCY_CONFLICT"},
		{"deleted_absent", status.Error(codes.NotFound, "COMPUTE_PRESET_NOT_FOUND"), true, false, 404, "COMPUTE_PRESET_NOT_FOUND"},
		{"unavailable_absent", status.Error(codes.Unavailable, "private catalog connection failed"), true, false, 503, "SANDBOX_DEPENDENCY_UNAVAILABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := &sandboxHTTPFailedResolution{sandboxHTTPResource: new(sandboxHTTPResource)}
			server := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
				return func(ctx context.Context, r any) (any, error) {
					return next(auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}), r)
				}
			}))
			RegisterSandboxHTTPServer(server, service.NewSandboxService(owner, new(sandboxHTTPAuthorization), sandboxHTTPTenants{}, sandboxHTTPAdmission{}))
			collection := "/api/v1/sandbox"
			body := `{"data":{"idempotency_key":"race-create","name":"demo","preset_id":"44444444-4444-4444-8444-444444444444","image_reference":"registry.example/sandbox@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
			fire := func(body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", collection, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				server.ServeHTTP(w, r)
				return w
			}
			// Obtain the real service's accepted identity and intent, then make
			// the first read of the raced delivery miss that concurrent record.
			seed := fire(body)
			require.Equal(t, 200, seed.Code, seed.Body.String())
			original := owner.created
			owner.created, owner.committed = nil, original
			owner.fail, owner.failure = true, tc.failure
			if tc.missing {
				owner.committed = nil
			}
			if tc.changed {
				body = strings.Replace(body, `"name":"demo"`, `"name":"changed"`, 1)
			}
			response := fire(body)
			require.Equal(t, tc.http, response.Code, response.Body.String())
			var result map[string]any
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
			if tc.http == 200 {
				require.Equal(t, true, result["accepted"])
				require.Equal(t, true, result["replayed"])
				require.Equal(t, original.Id, result["resource_id"])
				require.Equal(t, original.CreateOperationId, result["operation_id"])
			} else {
				require.Equal(t, tc.reason, result["reason"])
				require.NotContains(t, response.Body.String(), "private catalog connection")
			}
		})
	}
}
