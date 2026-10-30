package auth

import (
	"context"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestImageHTTPStrictBoundary(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
		header                   string
		want                     int
	}{
		{"issue", "POST", "/api/v1/images/publisher-credential:issue", `{"idempotencyKey":"issue-123","expectedVersion":"0"}`, "", 204},
		{"snake names", "POST", "/api/v1/images/publisher-credential:issue", `{"idempotency_key":"issue-123","expected_version":"0"}`, "", 204},
		{"forged tenant", "POST", "/api/v1/images/publisher-credential:issue", `{"tenant_id":"foreign","idempotencyKey":"issue-123"}`, "", 400},
		{"duplicate field", "POST", "/api/v1/images/publisher-credential:issue", `{"idempotencyKey":"issue-123","idempotencyKey":"other-123"}`, "", 400},
		{"forged metadata", "POST", "/api/v1/images/publisher-credential:issue", `{"idempotencyKey":"issue-123"}`, "X-Ani-Tenant-Id", 400},
		{"bad query", "GET", "/api/v1/images/space?tenant_id=foreign", "", "", 400},
		{"get body", "GET", "/api/v1/images/space", "{}", "", 400},
		{"list", "GET", "/api/v1/images/registrations?scope=tenant&purposes=container&purposes=training", "", "", 204},
		{"duplicate scope", "GET", "/api/v1/images/registrations?scope=tenant&scope=platform", "", "", 400},
		{"body id mismatch", "PATCH", "/api/v1/images/registrations/img_" + strings.Repeat("a", 32), `{"imageId":"img_` + strings.Repeat("b", 32) + `"}`, "", 400},
		{"oversized", "POST", "/api/v1/images/publisher-credential:issue", strings.Repeat("x", 16385), "", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			if test.header != "" {
				r.Header.Set(test.header, "forged")
			}
			w := httptest.NewRecorder()
			called := false
			ImageHTTPFilter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				b, err := io.ReadAll(r.Body)
				if err != nil || string(b) != test.body {
					t.Fatal("body was not replayed")
				}
				w.WriteHeader(204)
			})).ServeHTTP(w, r)
			if w.Code != test.want || called != (test.want == 204) {
				t.Fatal("wrong admission", w.Code)
			}
			if strings.Contains(test.path, "publisher-credential") && (w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Pragma") != "no-cache") {
				t.Fatal("credential response cacheable")
			}
		})
	}
}
func TestImageSignedBodyAndCanonicalQuery(t *testing.T) {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	path := "/api/v1/images/publisher-credential:issue"
	body := `{"idempotencyKey":"issue-123","expectedVersion":"0"}`
	operation := "/admin.service.v1.ImageService/IssuePublisherCredential"
	for _, test := range []struct {
		name, body, query string
		valid             bool
	}{{"valid", body, "", true}, {"changed body", `{"idempotencyKey":"issue-124","expectedVersion":"0"}`, "", false}} {
		t.Run(test.name, func(t *testing.T) {
			store := &signingStoreStub{key: &SigningKey{ID: 9, TenantID: 5, Role: "tenant:publisher", RoleAllowed: true, Secret: vectorSK}}
			r := httptest.NewRequest("POST", path, strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Access-Key", "ak-test")
			r.Header.Set("X-Timestamp", ts)
			r.Header.Set("X-Signature", ImageSignature(vectorSK, "POST", path, "", "ak-test", ts, []byte(body)))
			called := false
			ImageHTTPFilter(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				called = true
				p, err := verifySignature(context.Background(), r, operation, store, now)
				if test.valid {
					if err != nil || p == nil || p.Type != SubjectAPIKey || p.TenantID != 5 {
						t.Fatal("valid signed Image request denied", err)
					}
				} else if kerrors.Code(err) != 401 || p != nil {
					t.Fatal("tampered Image body accepted")
				}
			})).ServeHTTP(httptest.NewRecorder(), r)
			if !called {
				t.Fatal("signature validation did not run")
			}
		})
	}
	if len(ImageOperations) != 11 {
		t.Fatal("unexpected public Image methods")
	}
	for op, p := range ImageOperations {
		if !allowsAPIKey(op) {
			t.Fatal("explicit Image operation missing")
		}
		code, ok := ImagePermission(p.Method, p.Path)
		if !ok || code != p.Permission {
			t.Fatal("permission mapping missing")
		}
	}
	if allowsAPIKey("/image.v1.ImageRuntimeService/GetTenantPullMaterial") || allowsAPIKey("/image.v1.ImageRuntimeService/EnsureTenantPullSecret") || allowsAPIKey("/admin.service.v1.ImageService/IssuePublisherCredentialExtra") {
		t.Fatal("internal or wildcard key permission")
	}
}
