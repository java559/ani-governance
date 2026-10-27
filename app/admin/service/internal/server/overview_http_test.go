package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	inferencev1 "go-wind-admin/api/gen/go/inference/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
)

type overviewInstancesStub struct{ totals map[string]int64 }

func (s *overviewInstancesStub) ListInstances(_ context.Context, r *view.ListInstancesRequest) (*view.ListInstancesResponse, error) {
	return &view.ListInstancesResponse{Total: s.totals[r.GetObservedState()]}, nil
}

type overviewInferenceStub struct{ items []*inferencev1.InferenceView }

func (s *overviewInferenceStub) ListInference(_ context.Context, _ *inferencev1.ListInferenceRequest) (*inferencev1.ListInferenceResponse, error) {
	return &inferencev1.ListInferenceResponse{Items: s.items, Total: int64(len(s.items))}, nil
}

type overviewModelsStub struct{ totals map[string]int64 }

func (s *overviewModelsStub) ListModels(_ context.Context, in *catalogv1.ListModelsRequest) (*catalogv1.ListModelsResponse, error) {
	if in.GetStatus() == "" {
		return &catalogv1.ListModelsResponse{Total: s.totals["ready"]}, nil
	}
	return &catalogv1.ListModelsResponse{Total: s.totals[in.GetStatus()]}, nil
}

type overviewAuthorizationStub struct{ deny bool }

func (s *overviewAuthorizationStub) Authorize(context.Context, uint32, uint32, string, string) error {
	if s.deny {
		return data.ErrActionAuthorizationDenied
	}
	return nil
}

func (s *overviewAuthorizationStub) AuthorizeSource(context.Context, uint32, uint32, string, string) error {
	if s.deny {
		return data.ErrActionAuthorizationDenied
	}
	return nil
}

func overviewTestService(deny bool) *service.OverviewService {
	return service.NewOverviewService(nil,
		&overviewInstancesStub{totals: map[string]int64{"running": 2, "stopped": 1, "pending": 0}},
		&overviewInferenceStub{items: []*inferencev1.InferenceView{{DesiredState: "running", Phase: "ready"}}},
		&overviewModelsStub{totals: map[string]int64{"ready": 4}},
		&overviewAuthorizationStub{deny: deny},
	)
}

func overviewHTTP(t *testing.T, s *service.OverviewService, authenticated bool) *httptest.Server {
	t.Helper()
	srv := khttp.NewServer(khttp.Timeout(0), khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, r any) (any, error) {
			op, ok := transport.FromServerContext(ctx)
			if !ok || !strings.Contains(op.Operation(), "OverviewService/") {
				t.Error("missing operation")
			}
			if authenticated {
				ctx = auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7})
			}
			return next(ctx, r)
		}
	}))
	RegisterOverviewHTTP(srv, s)
	actual := httptest.NewServer(srv)
	t.Cleanup(actual.Close)
	return actual
}

func overviewRequest(t *testing.T, srv *httptest.Server, method, suffix, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r, _ := http.NewRequest(method, srv.URL+"/api/v1/overview"+suffix, reader)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	return response.StatusCode, object
}

func TestOverviewHTTPRejectsUnexpectedInput(t *testing.T) {
	srv := overviewHTTP(t, overviewTestService(false), true)
	for _, tc := range []struct {
		method, suffix, body string
		want                 int
	}{
		{"GET", "?a=1", "", 400},
		{"GET", "?", "", 400},
		{"GET", "", "{}", 400},
	} {
		code, _ := overviewRequest(t, srv, tc.method, tc.suffix, tc.body)
		if code != tc.want {
			t.Fatalf("%+v status=%d", tc, code)
		}
	}
}

func TestOverviewHTTPResponseContract(t *testing.T) {
	srv := overviewHTTP(t, overviewTestService(false), true)
	code, object := overviewRequest(t, srv, "GET", "", "")
	if code != 200 {
		t.Fatalf("%d %+v", code, object)
	}
	for _, key := range []string{"instances", "inference_services", "models", "knowledge_bases"} {
		card, ok := object[key].(map[string]any)
		if !ok {
			t.Fatalf("%s missing or not an object: %+v", key, object)
		}
		total, ok := card["total"].(float64)
		if !ok {
			t.Fatalf("%s.total must be a JSON number: %v", key, card["total"])
		}
		if _, ok := card["other"].(float64); !ok {
			t.Fatalf("%s.other must be a JSON number: %v", key, card["other"])
		}
		field := "by_state"
		if key != "instances" {
			field = "by_status"
		}
		counts, ok := card[field].(map[string]any)
		if !ok {
			t.Fatalf("%s.%s missing: %+v", key, field, card)
		}
		for _, value := range counts {
			if _, ok := value.(float64); !ok {
				t.Fatalf("%s.%s value must be a JSON number: %v", key, field, value)
			}
		}
		if key == "instances" && total != 3 {
			t.Fatalf("instances.total=%v want 3", total)
		}
	}
	if len(object) != 4 {
		t.Fatalf("unexpected top level keys: %+v", object)
	}
}

func TestOverviewHTTPAuthentication(t *testing.T) {
	unauthenticated := overviewHTTP(t, overviewTestService(false), false)
	if code, _ := overviewRequest(t, unauthenticated, "GET", "", ""); code != 401 {
		t.Fatalf("unauthenticated status=%d", code)
	}
	denied := overviewHTTP(t, overviewTestService(true), true)
	if code, _ := overviewRequest(t, denied, "GET", "", ""); code != 403 {
		t.Fatalf("denied status=%d", code)
	}
}
