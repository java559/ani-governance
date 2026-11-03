package server

import (
	"context"
	"encoding/json"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Exercise the registered public routes with denying authentication. No tenant
// or successful environment business response is substituted by this test.
func TestModelDevEnvironmentRoutesEnterAuthenticationAndRejectGetInputs(t *testing.T) {
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/api/v1/modeldev/environment:ensure", "{}", http.StatusUnauthorized},
		{"GET", "/api/v1/modeldev/environment", "", http.StatusUnauthorized},
		{"GET", "/api/v1/modeldev/environment?tenant_id=foreign", "", http.StatusBadRequest},
		{"GET", "/api/v1/modeldev/environment", "{}", http.StatusBadRequest},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			entered := 0
			server := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
				return func(context.Context, interface{}) (interface{}, error) {
					entered++
					return nil, errors.Unauthorized("INVALID_LOGIN", "user login required")
				}
			}))
			registerModelDevHTTP(server, nil)
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "11111111-1111-4111-8111-111111111111")
			out := httptest.NewRecorder()
			server.ServeHTTP(out, r)
			if out.Code != tc.want || (entered == 1) != (tc.want == http.StatusUnauthorized) {
				t.Fatalf("status=%d auth_calls=%d, want %d", out.Code, entered, tc.want)
			}
		})
	}
}

func TestModelDevEnvironmentStrictEmptyJSONAndKey(t *testing.T) {
	const key = "11111111-1111-4111-8111-111111111111"
	for _, c := range []struct {
		name, body, key, query string
		valid                  bool
	}{
		{"empty", "{}", key, "", true}, {"whitespace", " \n { } \n", key, "", true}, {"null", "null", key, "", false}, {"array", "[]", key, "", false}, {"field", "{\"tenant_id\":\"foreign\"}", key, "", false}, {"trailing", "{}{}", key, "", false}, {"oversize", "{}" + strings.Repeat(" ", 1024), key, "", false}, {"zero-key", "{}", "00000000-0000-0000-0000-000000000000", "", false}, {"uppercase-key", "{}", "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", "", false}, {"query", "{}", key, "?tenant_id=foreign", false}, {"empty-query", "{}", key, "?", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/modeldev/environment:ensure"+c.query, strings.NewReader(c.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", c.key)
			got, err := decodeModelDevEnvironmentEnsure(r)
			if (err == nil) != c.valid || (c.valid && got != key) {
				t.Fatalf("key=%q err=%v", got, err)
			}
		})
	}
	r := httptest.NewRequest("POST", "/api/v1/modeldev/environment:ensure", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Add("Idempotency-Key", key)
	r.Header.Add("Idempotency-Key", key)
	if _, err := decodeModelDevEnvironmentEnsure(r); err == nil {
		t.Fatal("duplicate key header accepted")
	}
	r = httptest.NewRequest("POST", "/api/v1/modeldev/environment:ensure", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Idempotency-Key", key)
	if _, err := decodeModelDevEnvironmentEnsure(r); err == nil {
		t.Fatal("non-JSON accepted")
	}
}
func TestModelDevEnvironmentExactHTTPBudgetsAndParent(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         time.Duration
	}{{"POST", "/api/v1/modeldev/environment:ensure", 30 * time.Second}, {"GET", "/api/v1/modeldev/environment", 5 * time.Second}, {"GET", "/api/v1/modeldev/environment:ensure", time.Second}, {"POST", "/api/v1/modeldev/environment", time.Second}, {"POST", "/api/v1/modeldev/environment:ensure/", time.Second}, {"GET", "/api/v1/modeldev/executions", time.Second}} {
		r := httptest.NewRequest(c.method, c.path, nil)
		instanceHTTPTimeoutFilter(time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > c.want || time.Until(deadline) < c.want-200*time.Millisecond {
				t.Fatalf("%s %s deadline=%v", c.method, c.path, deadline)
			}
		})).ServeHTTP(httptest.NewRecorder(), r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("POST", "/api/v1/modeldev/environment:ensure", nil).WithContext(ctx)
	instanceHTTPTimeoutFilter(time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ := r.Context().Deadline()
		want, _ := ctx.Deadline()
		if !got.Equal(want) {
			t.Fatal("parent budget extended")
		}
	})).ServeHTTP(httptest.NewRecorder(), r)
}
func TestModelDevEnvironmentDiscardsEveryANIHeader(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/modeldev/environment", nil)
	r.Header.Set("X-Ani-Tenant-ID", "foreign")
	r.Header.Set("X-Ani-Unknown", "untrusted")
	r.Header.Set("Authorization", "Bearer user")
	stripModelDevEnvironmentIdentity(r)
	if r.Header.Get("X-Ani-Tenant-ID") != "" || r.Header.Get("X-Ani-Unknown") != "" || r.Header.Get("Authorization") == "" {
		t.Fatal("identity stripping changed authentication or retained assertions")
	}
}

func TestModelDevEnvironmentJSONKeepsScalarObservationsAndOmitsUnconfirmedFields(t *testing.T) {
	v := &modeldevv1.TenantExecutionEnvironmentView{EnvironmentId: "11111111-1111-4111-8111-111111111111", ClusterId: "22222222-2222-4222-8222-222222222222", NamespaceName: "tenant-33333333-3333-4333-8333-333333333333", State: "BLOCKED", Phase: "NAMESPACE", ReasonCode: "TENANT_NAMESPACE_NOT_FOUND", UpdatedAt: timestamppb.Now()}
	raw, err := encodeModelDevEnvironment(&modeldevv1.GetTenantExecutionEnvironmentResponse{Environment: v})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Environment map[string]json.RawMessage `json:"environment"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"namespace_uid", "binding_digest", "experiment_id", "last_verified_at"} {
		if _, found := response.Environment[key]; found {
			t.Fatalf("unconfirmed %s emitted: %s", key, raw)
		}
	}
	if string(response.Environment["retryable"]) != "false" || string(response.Environment["retry_after_seconds"]) != "0" || len(response.Environment["updated_at"]) == 0 {
		t.Fatalf("fixed scalar observations lost: %s", raw)
	}
}
