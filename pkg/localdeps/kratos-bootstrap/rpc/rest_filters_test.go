package rpc

import (
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdditionalHTTPFilterPreservesCORS(t *testing.T) {
	called := false
	extra := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.Header().Set("X-Boundary", "checked")
			next.ServeHTTP(w, r)
		})
	}
	cfg := &conf.Bootstrap{Server: &conf.Server{Rest: &conf.Server_REST{Cors: &conf.Server_REST_CORS{Origins: []string{"https://console.example.test"}, Methods: []string{"GET"}}}}}
	srv, err := CreateRestServerWithFilters(cfg, []khttp.FilterFunc{extra})
	if err != nil {
		t.Fatal(err)
	}
	srv.HandleFunc("/test", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	r := httptest.NewRequest("GET", "/test", nil)
	r.Header.Set("Origin", "https://console.example.test")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != 204 || !called || w.Header().Get("X-Boundary") != "checked" || w.Header().Get("Access-Control-Allow-Origin") != "https://console.example.test" {
		t.Fatal("additional filter displaced CORS", w.Code, w.Header())
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); !strings.EqualFold(got, "X-Request-ID") {
		t.Fatalf("request ID not exposed to browser: %q", got)
	}
}

func TestRESTServerDoesNotFallBackToDefaultServeMux(t *testing.T) {
	for _, cfg := range []*conf.Bootstrap{nil, {Server: &conf.Server{Rest: &conf.Server_REST{}}}} {
		srv, err := CreateRestServer(cfg)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("disabled pprof should return 404, got %d", w.Code)
		}
		srv.Route("/").GET("/method-only", func(ctx khttp.Context) error {
			return ctx.String(http.StatusOK, "ok")
		})
		w = httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/method-only", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("unsupported method should return 405, got %d", w.Code)
		}
	}
}

func TestRESTServerExplicitPprofStillWorks(t *testing.T) {
	cfg := &conf.Bootstrap{Server: &conf.Server{Rest: &conf.Server_REST{EnablePprof: true}}}
	srv, err := CreateRestServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/debug/pprof", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("explicit pprof should return 200, got %d", w.Code)
	}
}
