package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/stretchr/testify/require"
	auditpb "go-wind-admin/api/gen/go/audit/service/v1"
	authpb "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/app/admin/service/tests/testutil"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	engine "go-wind-admin/pkg/localdeps/kratos-authz/engine"
	"go-wind-admin/pkg/localdeps/kratos-authz/engine/casbin"
	authz "go-wind-admin/pkg/localdeps/kratos-authz/middleware"
	"go-wind-admin/pkg/middleware/auth"
	applogging "go-wind-admin/pkg/middleware/logging"
	"go-wind-admin/pkg/permcode"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const inferenceHTTPKeyID = "22222222-2222-4222-8222-222222222222"
const inferenceHTTPPolicyID = "33333333-3333-4333-8333-333333333333"
const inferenceHTTPSecret = "iak_0000000000000000000000000000000000000000000"

// External Inference protocol responses only; no fake local credential store.
type inferenceKeyHTTPPeer struct {
	service.InferenceOwnerClient
	tenant      string
	actor       string
	calls       int
	replayed    bool
	lastRequest proto.Message
}

func (p *inferenceKeyHTTPPeer) observe(tenant, actor string, in proto.Message) error {
	p.calls++
	p.actor, p.lastRequest = actor, in
	if tenant != p.tenant {
		return status.Error(codes.NotFound, "private foreign tenant key")
	}
	return nil
}
func inferenceHTTPKey() *runtimev1.InferenceAccessKey {
	return &runtimev1.InferenceAccessKey{Id: inferenceHTTPKeyID, Name: "app", KeyPrefix: inferenceHTTPSecret[:16], PolicyId: "33333333-3333-4333-8333-333333333333", CreatedAt: timestamppb.Now(), Version: 1, Secret: proto.String(inferenceHTTPSecret)}
}
func (p *inferenceKeyHTTPPeer) CreateInferenceKeyTrusted(_ context.Context, tenant, actor string, in *runtimev1.CreateInferenceAccessKeyRequest) (*runtimev1.CreateInferenceAccessKeyResponse, error) {
	err := p.observe(tenant, actor, in)
	return &runtimev1.CreateInferenceAccessKeyResponse{Key: inferenceHTTPKey(), Secret: inferenceHTTPSecret, Replayed: p.replayed}, err
}
func (p *inferenceKeyHTTPPeer) ListInferenceKeysTrusted(_ context.Context, tenant, actor string, in *runtimev1.ListInferenceAccessKeysRequest) (*runtimev1.ListInferenceAccessKeysResponse, error) {
	err := p.observe(tenant, actor, in)
	if in.Page > 1 {
		return &runtimev1.ListInferenceAccessKeysResponse{Total: 1}, err
	}
	return &runtimev1.ListInferenceAccessKeysResponse{Items: []*runtimev1.InferenceAccessKey{inferenceHTTPKey()}, Total: 1}, err
}
func (p *inferenceKeyHTTPPeer) GetInferenceKeyTrusted(_ context.Context, tenant, actor string, in *runtimev1.GetInferenceAccessKeyRequest) (*runtimev1.InferenceAccessKey, error) {
	return inferenceHTTPKey(), p.observe(tenant, actor, in)
}
func (p *inferenceKeyHTTPPeer) RevokeInferenceKeyTrusted(_ context.Context, tenant, actor string, in *runtimev1.RevokeInferenceAccessKeyRequest) (*runtimev1.RevokeInferenceAccessKeyResponse, error) {
	err := p.observe(tenant, actor, in)
	key := inferenceHTTPKey()
	key.RevokedAt = timestamppb.Now()
	return &runtimev1.RevokeInferenceAccessKeyResponse{Key: key}, err
}
func (p *inferenceKeyHTTPPeer) RotateInferenceKeyTrusted(_ context.Context, tenant, actor string, in *runtimev1.RotateInferenceAccessKeyRequest) (*runtimev1.RotateInferenceAccessKeyResponse, error) {
	err := p.observe(tenant, actor, in)
	return &runtimev1.RotateInferenceAccessKeyResponse{Key: inferenceHTTPKey(), Secret: inferenceHTTPSecret, Replayed: p.replayed}, err
}
func httpPolicy() *runtimev1.InferenceAccessPolicy {
	return &runtimev1.InferenceAccessPolicy{Id: inferenceHTTPPolicyID, Name: "policy", AllowedModelNames: []string{"model"}, Version: 1}
}
func (p *inferenceKeyHTTPPeer) CreateInferencePolicyTrusted(_ context.Context, t, a string, in *runtimev1.CreateInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	return httpPolicy(), p.observe(t, a, in)
}
func (p *inferenceKeyHTTPPeer) ListInferencePoliciesTrusted(_ context.Context, t, a string, in *runtimev1.ListInferenceAccessPoliciesRequest) (*runtimev1.ListInferenceAccessPoliciesResponse, error) {
	return &runtimev1.ListInferenceAccessPoliciesResponse{Items: []*runtimev1.InferenceAccessPolicy{httpPolicy()}, Total: 1}, p.observe(t, a, in)
}
func (p *inferenceKeyHTTPPeer) GetInferencePolicyTrusted(_ context.Context, t, a string, in *runtimev1.GetInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	return httpPolicy(), p.observe(t, a, in)
}
func (p *inferenceKeyHTTPPeer) UpdateInferencePolicyTrusted(_ context.Context, t, a string, in *runtimev1.UpdateInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	return httpPolicy(), p.observe(t, a, in)
}
func (p *inferenceKeyHTTPPeer) DeleteInferencePolicyTrusted(_ context.Context, t, a string, in *runtimev1.DeleteInferenceAccessPolicyRequest) (*runtimev1.DeleteInferenceAccessPolicyResponse, error) {
	return &runtimev1.DeleteInferenceAccessPolicyResponse{}, p.observe(t, a, in)
}
func (p *inferenceKeyHTTPPeer) BindInferenceKeyPolicyTrusted(_ context.Context, t, a string, in *runtimev1.BindInferenceAccessKeyPolicyRequest) (*runtimev1.InferenceAccessKey, error) {
	return inferenceHTTPKey(), p.observe(t, a, in)
}

func TestInferenceKeyHTTPManagementWithRealTenantGateAndCasbin(t *testing.T) {
	ctx := testutil.NewSystemViewerCtx(context.Background())
	c := testutil.NewEntClientForTest(t)
	bctx := testutil.NewBootstrapContext(nil)
	plan, err := c.Client().Plan.Create().SetName("inference").Save(ctx)
	require.NoError(t, err)
	module, err := c.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleInference).Save(ctx)
	require.NoError(t, err)
	a, err := c.Client().Tenant.Create().SetName("a").SetCode("key-a").SetPlanID(plan.ID).Save(ctx)
	require.NoError(t, err)
	b, err := c.Client().Tenant.Create().SetName("b").SetCode("key-b").SetPlanID(plan.ID).Save(ctx)
	require.NoError(t, err)
	peer := &inferenceKeyHTTPPeer{tenant: a.ResourceTenantID}
	bff := service.NewInferenceServiceWithOwner(nil, peer, data.NewTenantRepo(bctx, c))
	policy, err := casbin.NewEngine(context.Background())
	require.NoError(t, err)
	var rules []casbin.PolicyRule
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/inference-keys"}, {"GET", "/api/v1/inference-keys"},
		{"GET", "/api/v1/inference-keys/{key_id}"}, {"DELETE", "/api/v1/inference-keys/{key_id}"},
		{"POST", "/api/v1/inference-keys/{key_id}:rotate"},
		{"POST", "/api/v1/inference-keys/{key_id}:bind-policy"},
		{"POST", "/api/v1/inference-policies"}, {"GET", "/api/v1/inference-policies"},
		{"GET", "/api/v1/inference-policies/{policy_id}"}, {"PUT", "/api/v1/inference-policies/{policy_id}"}, {"DELETE", "/api/v1/inference-policies/{policy_id}"},
	} {
		grant, err := permcode.Resolve(route.method, route.path)
		require.NoError(t, err)
		require.Equal(t, "INFERENCE", grant.BusinessModule)
		_, err = c.Client().Api.Create().SetPath(route.path).SetMethod(route.method).SetModule("InferenceKeyService").SetBusinessModule(api.BusinessModuleInference).SetStatus(api.StatusOn).Save(ctx)
		require.NoError(t, err)
		for _, tid := range []uint32{a.ID, b.ID, 0} {
			rules = append(rules, casbin.PolicyRule{PType: "p", V0: "key-manager", V1: permcode.NormalizePath(route.path), V2: route.method, V3: strconv.Itoa(int(tid))})
		}
		if route.method == "GET" {
			rules = append(rules, casbin.PolicyRule{PType: "p", V0: "key-reader", V1: permcode.NormalizePath(route.path), V2: "GET", V3: strconv.Itoa(int(a.ID))})
		}
	}
	rules = append(rules, casbin.PolicyRule{PType: "p", V0: "platform-key-only", V1: "/api/v1/auth/api-keys", V2: "GET", V3: strconv.Itoa(int(a.ID))})
	require.NoError(t, policy.SetPolicies(context.Background(), engine.PolicyMap{"policies": rules}, nil))
	checker := auth.NewAccessTokenCheckerFromFuncs(func(_ context.Context, token string, _ bool) (bool, *authpb.UserTokenPayload) {
		tid, role := a.ID, "key-manager"
		switch token {
		case "user-a":
		case "user-b":
			tid = b.ID
		case "reader":
			role = "key-reader"
		case "platform-key-only":
			role = "platform-key-only"
		case "platform":
			tid = 0
		default:
			return false, nil
		}
		return true, &authpb.UserTokenPayload{TenantId: trans.Ptr(tid), UserId: 33, Roles: []string{role}}
	}, nil)
	var auditRecords []*auditpb.ApiAuditLog
	srv := khttp.NewServer(khttp.Middleware(
		applogging.Server(applogging.WithWriteApiLogFunc(func(_ context.Context, record *auditpb.ApiAuditLog) error {
			auditRecords = append(auditRecords, record)
			return nil
		})),
		auth.CredentialHeaders(),
		auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(data.NewTenantAccessCheckerImpl(bctx, c)), auth.WithInjectMetadata(false)),
		authz.Server(policy),
	))
	RegisterInferenceHTTPServer(srv, bff)
	fire := func(method, path, body, token string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		require.Equal(t, want, w.Code, "%s %s: %s", method, path, w.Body.String())
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		var out map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	base := "/api/v1/inference-keys"
	item := base + "/" + inferenceHTTPKeyID
	body := `{"data":{"name":"app","policy_id":"33333333-3333-4333-8333-333333333333","idempotency_key":"create-1"}}`
	policyBase := "/api/v1/inference-policies"
	policyItem := policyBase + "/" + inferenceHTTPPolicyID
	policyBody := `{"data":{"name":"policy","allowed_model_names":["model"],"requests_per_second":2}}`
	createdPolicy := fire("POST", policyBase, policyBody, "user-a", 200)
	require.Equal(t, inferenceHTTPPolicyID, createdPolicy["id"])
	fire("GET", policyBase, "", "reader", 200)
	fire("GET", policyItem, "", "user-a", 200)
	fire("PUT", policyItem, policyBody, "user-a", 200)
	bound := fire("POST", item+":bind-policy", `{"data":{"policy_id":"`+inferenceHTTPPolicyID+`"}}`, "user-a", 200)
	require.Equal(t, inferenceHTTPPolicyID, bound["policy_id"])
	require.NotContains(t, bound, "secret")
	fire("DELETE", policyItem, "", "user-a", 200)
	fire("POST", policyBase, policyBody, "reader", 403)
	fire("GET", policyItem, "", "user-b", 404)
	created := fire("POST", base, body, "user-a", 200)
	require.Equal(t, inferenceHTTPSecret, created["secret"])
	require.Equal(t, "governance:user:33", peer.actor)
	require.Equal(t, "create-1", peer.lastRequest.(*runtimev1.CreateInferenceAccessKeyRequest).IdempotencyKey)
	peer.replayed = true
	replayed := fire("POST", base, body, "user-a", 200)
	require.NotEqual(t, inferenceHTTPSecret, replayed["secret"])
	require.Equal(t, true, replayed["replayed"])
	peer.replayed = false
	list := fire("GET", base+"?pageSize=2", "", "user-a", 200)
	require.Equal(t, "1", list["total"])
	require.Len(t, list["items"], 1)
	require.Equal(t, inferenceHTTPSecret, list["items"].([]any)[0].(map[string]any)["secret"])
	page := peer.lastRequest.(*runtimev1.ListInferenceAccessKeysRequest)
	require.EqualValues(t, 1, page.Page)
	require.EqualValues(t, 2, page.PageSize)
	list = fire("GET", base+"?page=3", "", "user-a", 200)
	require.Equal(t, "1", list["total"])
	require.Equal(t, []any{}, list["items"])
	key := fire("GET", item, "", "reader", 200)
	require.Equal(t, inferenceHTTPKeyID, key["id"])
	require.Equal(t, "1", key["version"])
	require.Equal(t, inferenceHTTPSecret, key["secret"])
	rotated := fire("POST", item+":rotate", `{"data":{"idempotency_key":"rotate-1"}}`, "user-a", 200)
	require.Equal(t, inferenceHTTPSecret, rotated["secret"])
	require.Equal(t, inferenceHTTPKeyID, peer.lastRequest.(*runtimev1.RotateInferenceAccessKeyRequest).Id)
	peer.replayed = true
	rotated = fire("POST", item+":rotate", `{"data":{"idempotency_key":"rotate-1"}}`, "user-a", 200)
	require.NotEqual(t, inferenceHTTPSecret, rotated["secret"])
	peer.replayed = false
	revoked := fire("DELETE", item, "", "user-a", 200)
	require.Contains(t, revoked, "revoked_at")
	before := peer.calls
	fire("GET", base, "", "", 401)
	fire("GET", base, "", "platform-key-only", 403)
	fire("GET", base, "", "platform", 403)
	fire("POST", base, body, "reader", 403)
	require.Equal(t, before, peer.calls, "denied callers must not reach Inference")
	for _, method := range []string{"GET", "DELETE", "POST"} {
		path, request := item, ""
		if method == "POST" {
			path, request = item+":rotate", `{"data":{"idempotency_key":"foreign"}}`
		}
		out := fire(method, path, request, "user-b", 404)
		require.Equal(t, "INFERENCE_KEY_NOT_FOUND", out["reason"])
		require.NotContains(t, out["message"], "foreign")
	}
	before = peer.calls
	require.NoError(t, c.Client().PlanModule.DeleteOneID(module.ID).Exec(ctx))
	fire("GET", base, "", "user-a", 403)
	require.NoError(t, c.Client().Tenant.UpdateOneID(a.ID).SetStatus(tenant.StatusFreeze).Exec(ctx))
	fire("GET", base, "", "user-a", 403)
	require.Equal(t, before, peer.calls)
	count, err := c.Client().AccessKey.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count, "inference key management must never write platform AK/SK")
	for _, record := range auditRecords {
		require.Equal(t, "[redacted]", record.GetRequestBody())
		require.Empty(t, record.GetResponse())
		require.Empty(t, record.GetReferer())
		require.NotContains(t, record.GetRequestUri(), "?")
	}
}

func TestInferenceKeyHTTPRejectsMalformedInputAndSigningCredentials(t *testing.T) {
	peer := &inferenceKeyHTTPPeer{}
	srv := khttp.NewServer(khttp.Middleware(auth.CredentialHeaders(), auth.Server()))
	RegisterInferenceHTTPServer(srv, service.NewInferenceServiceWithOwner(nil, peer, nil))
	base := "/api/v1/inference-keys"
	for _, tc := range []struct{ method, path, body string }{
		{"GET", base + "?page=0", ""}, {"GET", base + "?page=01", ""},
		{"GET", base + "?page=101", ""}, {"GET", base + "?page=1&page=2", ""},
		{"GET", base + "?pageSize=", ""}, {"GET", base + "?pageSize=101", ""},
		{"GET", base + "?tenant_id=forged", ""}, {"GET", base, `{}`},
		{"POST", base, `{"tenant_id":"forged","data":{}}`},
		{"POST", base, `{"data":{"secret":"private"}}`},
		{"POST", base, `{"data":{},"data":{}}`},
		{"POST", base + "/" + inferenceHTTPKeyID + ":rotate", `{"key_id":"different","data":{"idempotency_key":"x"}}`},
		{"DELETE", base + "/" + inferenceHTTPKeyID, `{}`},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		require.Equal(t, 400, w.Code, "%s %s: %s", tc.method, tc.path, w.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		r := httptest.NewRequest(method, base, strings.NewReader(`{"data":{"name":"app","policy_id":"33333333-3333-4333-8333-333333333333","idempotency_key":"x"}}`))
		if method == "GET" {
			r.Body, r.ContentLength = http.NoBody, 0
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Ani-Tenant-Id", "forged")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		require.Equal(t, 400, w.Code)
	}
	r := httptest.NewRequest("GET", base, nil)
	r.Header.Set("X-Access-Key", "ak-test")
	r.Header.Set("X-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	r.Header.Set("X-Signature", strings.Repeat("a", 64))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	require.Equal(t, 403, w.Code)
	require.Contains(t, w.Body.String(), "USER_REQUIRED")
	require.Zero(t, peer.calls)
}
