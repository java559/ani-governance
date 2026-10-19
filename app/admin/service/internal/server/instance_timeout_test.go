package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInstanceLogTimeoutKeepsOrdinaryDeadlineAndClientCancellation(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		deadline     bool
	}{
		{"GET", "/api/v1/instances", true},
		{"GET", "/api/v1/instances/10000000-0000-4000-8000-000000000001/logs", false},
		{"POST", "/api/v1/instances/10000000-0000-4000-8000-000000000001/logs", true},
		{"GET", "/api/v1/instances/10000000-0000-4000-8000-000000000001/runtime", true},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := httptest.NewRequest(tc.method, tc.path, nil).WithContext(parent)
			instanceHTTPTimeoutFilter(10*time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				_, has := r.Context().Deadline()
				if has != tc.deadline {
					t.Fatalf("deadline present=%v", has)
				}
				cancel()
				select {
				case <-r.Context().Done():
				default:
					t.Fatal("client cancellation lost")
				}
			})).ServeHTTP(httptest.NewRecorder(), request)
		})
	}
}
