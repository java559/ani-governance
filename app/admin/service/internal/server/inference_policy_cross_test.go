//go:build policybridge

package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/stretchr/testify/require"
	authpb "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/app/admin/service/tests/testutil"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	engine "go-wind-admin/pkg/localdeps/kratos-authz/engine"
	"go-wind-admin/pkg/localdeps/kratos-authz/engine/casbin"
	authz "go-wind-admin/pkg/localdeps/kratos-authz/middleware"
	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/pkg/permcode"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type policyBridgePKI struct {
	root   *x509.Certificate
	key    ed25519.PrivateKey
	dir    string
	serial int64
}

func (p *policyBridgePKI) cert(t *testing.T, name string, spec *x509.Certificate) (string, string) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	p.serial++
	spec.SerialNumber = big.NewInt(p.serial)
	spec.Subject = pkix.Name{CommonName: name}
	spec.NotBefore = time.Now().Add(-time.Hour)
	spec.NotAfter = time.Now().Add(time.Hour)
	spec.KeyUsage |= x509.KeyUsageDigitalSignature
	parent, sign := p.root, p.key
	if parent == nil {
		parent, sign = spec, key
	}
	der, err := x509.CreateCertificate(rand.Reader, spec, parent, pub, sign)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	if p.root == nil {
		p.root, p.key = leaf, key
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certFile, keyFile := filepath.Join(p.dir, name+".crt"), filepath.Join(p.dir, name+".key")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600))
	return certFile, keyFile
}

// Opt-in: real Governance auth, tenant and HTTP handlers, real mTLS client,
// real Inference receiver and PostgreSQL. No model engine or Higress is run.
func TestInferencePolicyCrossBackend(t *testing.T) {
	if os.Getenv("ANI_POLICY_BRIDGE_ENABLE") != "1" {
		t.Fatal("ANI_POLICY_BRIDGE_ENABLE=1 required for this explicit integration test")
	}
	dsn, source := os.Getenv("ANI_POLICY_BRIDGE_DSN"), os.Getenv("ANI_POLICY_BRIDGE_SOURCE")
	if dsn == "" || source == "" || os.Getenv("ANI_POLICY_BRIDGE_DEDICATED_DB") != "1" {
		t.Fatal("explicit dedicated Inference DB and source path required")
	}
	ctx := testutil.NewSystemViewerCtx(context.Background())
	c := testutil.NewEntClientForTest(t)
	bctx := testutil.NewBootstrapContext(nil)
	plan, err := c.Client().Plan.Create().SetName("policy-bridge").Save(ctx)
	require.NoError(t, err)
	_, err = c.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleInference).Save(ctx)
	require.NoError(t, err)
	tenant, err := c.Client().Tenant.Create().SetName("bridge").SetCode("policy-bridge").SetPlanID(plan.ID).Save(ctx)
	require.NoError(t, err)
	other, err := c.Client().Tenant.Create().SetName("other").SetCode("policy-bridge-other").SetPlanID(plan.ID).Save(ctx)
	require.NoError(t, err)
	model := "bridge-model-" + tenant.ResourceTenantID
	pki := &policyBridgePKI{dir: t.TempDir()}
	caFile, _ := pki.cert(t, "ca", &x509.Certificate{IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign})
	serverCert, serverKey := pki.cert(t, "server", &x509.Certificate{DNSNames: []string{"ani-inference.ani.internal"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	govCert, govKey := pki.cert(t, "governance", &x509.Certificate{DNSNames: []string{"ani-governance"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	gatewayCert, gatewayKey := pki.cert(t, "gateway", &x509.Certificate{DNSNames: []string{"ani-inference-gateway.ani.internal"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	ready := filepath.Join(pki.dir, "ready")
	logFile, err := os.Create(filepath.Join(pki.dir, "inference.log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = logFile.Close() })
	proc := exec.Command("go", "test", "-tags=policybridge", "./cmd/ani-inference-service", "-run", "^TestInferenceAccessPolicyBridgeFixture$", "-count=1", "-timeout=3m")
	proc.Dir = source
	proc.Stdout = logFile
	proc.Stderr = logFile
	proc.Env = append(os.Environ(), "ANI_POLICY_BRIDGE_ENABLE=1", "ANI_POLICY_BRIDGE_DEDICATED_DB=1", "ANI_POLICY_BRIDGE_DSN="+dsn, "ANI_POLICY_BRIDGE_TENANT="+tenant.ResourceTenantID, "ANI_POLICY_BRIDGE_MODEL="+model, "ANI_POLICY_BRIDGE_READY="+ready, "ANI_SERVER_TLS_CA_FILE="+caFile, "ANI_SERVER_TLS_CERT_FILE="+serverCert, "ANI_SERVER_TLS_KEY_FILE="+serverKey)
	require.NoError(t, proc.Start())
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	t.Cleanup(func() {
		if err := os.WriteFile(ready+".stop", []byte("stop"), 0600); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil {
				b, _ := os.ReadFile(logFile.Name())
				t.Errorf("Inference fixture failed: %v: %s", err, b)
			}
		case <-time.After(15 * time.Second):
			_ = proc.Process.Kill()
			<-done
			t.Error("Inference fixture did not stop")
		}
	})
	var address string
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if b, e := os.ReadFile(ready); e == nil {
			address = string(b)
			break
		}
		select {
		case e := <-done:
			done <- e // Cleanup must still observe the already-finished process.
			b, _ := os.ReadFile(logFile.Name())
			t.Fatalf("Inference fixture exited: %v: %s", e, b)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotEmpty(t, address, "Inference fixture did not become ready")
	owner, closeOwner, err := data.NewInferenceClient(data.InferenceClientConfig{Address: address, ServerName: "ani-inference.ani.internal", CAFile: caFile, CertFile: govCert, KeyFile: govKey, Timeout: 3 * time.Second})
	require.NoError(t, err)
	t.Cleanup(closeOwner)
	_, err = owner.GetInferencePolicyTrusted(context.Background(), tenant.ResourceTenantID, "governance:user:33", &runtimev1.GetInferenceAccessPolicyRequest{Id: "11111111-1111-4111-8111-111111111111"})
	require.Equal(t, codes.NotFound, status.Code(err), "direct mTLS policy call failed: %v", err)
	bff := service.NewInferenceServiceWithOwner(nil, owner, data.NewTenantRepo(bctx, c))
	authzEngine, err := casbin.NewEngine(context.Background())
	require.NoError(t, err)
	routes := []struct{ method, path string }{{"POST", "/api/v1/inference-policies"}, {"GET", "/api/v1/inference-policies/{policy_id}"}, {"PUT", "/api/v1/inference-policies/{policy_id}"}, {"DELETE", "/api/v1/inference-policies/{policy_id}"}, {"POST", "/api/v1/inference-keys"}, {"POST", "/api/v1/inference-keys/{key_id}:bind-policy"}}
	var rules []casbin.PolicyRule
	for _, r := range routes {
		grant, e := permcode.Resolve(r.method, r.path)
		require.NoError(t, e)
		require.Equal(t, "INFERENCE", grant.BusinessModule)
		_, e = c.Client().Api.Create().SetPath(r.path).SetMethod(r.method).SetModule("InferenceKeyService").SetBusinessModule(api.BusinessModuleInference).SetStatus(api.StatusOn).Save(ctx)
		require.NoError(t, e)
		for _, id := range []uint32{tenant.ID, other.ID} {
			rules = append(rules, casbin.PolicyRule{PType: "p", V0: "manager", V1: permcode.NormalizePath(r.path), V2: r.method, V3: fmt.Sprint(id)})
		}
	}
	require.NoError(t, authzEngine.SetPolicies(context.Background(), engine.PolicyMap{"policies": rules}, nil))
	checker := auth.NewAccessTokenCheckerFromFuncs(func(_ context.Context, token string, _ bool) (bool, *authpb.UserTokenPayload) {
		id := tenant.ID
		if token == "other" {
			id = other.ID
		} else if token != "owner" {
			return false, nil
		}
		return true, &authpb.UserTokenPayload{TenantId: trans.Ptr(id), UserId: 33, Roles: []string{"manager"}}
	}, nil)
	srv := khttp.NewServer(khttp.Middleware(auth.CredentialHeaders(), auth.Server(auth.WithAccessTokenChecker(checker), auth.WithTenantAccessChecker(data.NewTenantAccessCheckerImpl(bctx, c)), auth.WithInjectMetadata(false)), authz.Server(authzEngine)))
	RegisterInferenceHTTPServer(srv, bff)
	fire := func(method, path, body, identity string, want int) map[string]any {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+identity)
		out := httptest.NewRecorder()
		srv.ServeHTTP(out, request)
		require.Equal(t, want, out.Code, "%s %s: %s", method, path, out.Body.String())
		var result map[string]any
		require.NoError(t, json.Unmarshal(out.Body.Bytes(), &result))
		return result
	}
	policyBody := fmt.Sprintf(`{"data":{"name":"policy","allowed_model_names":[%q],"requests_per_hour":1}}`, model)
	policy := fire("POST", "/api/v1/inference-policies", policyBody, "owner", 200)
	policyID := policy["id"].(string)
	createKey := func(name string) map[string]any {
		return fire("POST", "/api/v1/inference-keys", fmt.Sprintf(`{"data":{"name":%q,"policy_id":%q,"idempotency_key":%q}}`, name, policyID, name), "owner", 200)
	}
	key1, key2 := createKey("app-1"), createKey("app-2")
	gatewayPair, err := tls.LoadX509KeyPair(gatewayCert, gatewayKey)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(pki.root)
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "ani-inference.ani.internal", Certificates: []tls.Certificate{gatewayPair}})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	gateway := runtimev1.NewInferenceAccessKeyServiceClient(conn)
	check := func(key map[string]any, want string) {
		t.Helper()
		call, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		out, e := gateway.CheckInferenceAccess(call, &runtimev1.CheckInferenceAccessRequest{ApiKey: key["secret"].(string), ServedModelName: model})
		require.NoError(t, e)
		if want == "" {
			require.True(t, out.Allowed)
		} else {
			require.False(t, out.Allowed)
			require.Equal(t, want, out.DenialCode)
		}
	}
	// Avoid crossing the real UTC hour boundary between the two quota checks.
	now := time.Now().UTC()
	if wait := now.Truncate(time.Hour).Add(time.Hour).Sub(now); wait < 5*time.Second {
		time.Sleep(wait + 100*time.Millisecond)
	}
	check(key1, "")
	check(key1, "rate_limited")
	check(key2, "")
	fire("GET", "/api/v1/inference-policies/"+policyID, "", "other", 404)
	fire("DELETE", "/api/v1/inference-policies/"+policyID, "", "owner", 409)
	fire("PUT", "/api/v1/inference-policies/"+policyID, `{"data":{"name":"policy","allowed_model_names":[]}}`, "owner", 200)
	check(key2, "scope_denied")
	secondPolicy := fire("POST", "/api/v1/inference-policies", fmt.Sprintf(`{"data":{"name":"new-policy","allowed_model_names":[%q]}}`, model), "owner", 200)
	secondPolicyID := secondPolicy["id"].(string)
	keyID := key1["key"].(map[string]any)["id"].(string)
	bindBody := fmt.Sprintf(`{"data":{"policy_id":%q}}`, secondPolicyID)
	fire("POST", "/api/v1/inference-keys/"+keyID+":bind-policy", bindBody, "other", 404)
	bound := fire("POST", "/api/v1/inference-keys/"+keyID+":bind-policy", bindBody, "owner", 200)
	require.Equal(t, secondPolicyID, bound["policy_id"])
	check(key1, "")
	check(key2, "scope_denied")
}
