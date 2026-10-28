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
		// The training log stream owns its bounded lifetime, exactly like the
		// instance log channel; the shared ordinary timeout must not cut it.
		{"GET", "/api/v1/modeldev/executions/20000000-0000-4000-8000-000000000002/logs/stream", false},
		{"POST", "/api/v1/modeldev/executions/20000000-0000-4000-8000-000000000002/logs/stream", true},
		{"GET", "/api/v1/modeldev/executions/20000000-0000-4000-8000-000000000002/logs", true},
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

func TestInstanceMetricsTimeoutOnlyChangesTwoMethods(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         time.Duration
	}{
		{"POST", "/api/v1/instances/10000000-0000-4000-8000-000000000001/metrics/query", 10 * time.Second},
		{"GET", "/api/v1/instances/10000000-0000-4000-8000-000000000001/metrics/latest", 10 * time.Second},
		{"GET", "/api/v1/instances/10000000-0000-4000-8000-000000000001/metrics/query", 3 * time.Second},
		{"POST", "/api/v1/instances/10000000-0000-4000-8000-000000000001/metrics/latest", 3 * time.Second},
		{"GET", "/api/v1/instances/10000000-0000-4000-8000-000000000001/runtime", 3 * time.Second},
		// The training resource curve shares the monitoring budget; the POST form
		// and a shorter path are still ordinary requests.
		{"GET", "/api/v1/modeldev/executions/10000000-0000-4000-8000-000000000001/resource-metrics", 10 * time.Second},
		{"POST", "/api/v1/modeldev/executions/10000000-0000-4000-8000-000000000001/resource-metrics", 3 * time.Second},
		{"GET", "/api/v1/modeldev/executions/10000000-0000-4000-8000-000000000001/resource-metrics/", 3 * time.Second},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			instanceHTTPTimeoutFilter(3*time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > tc.want || time.Until(deadline) < tc.want-time.Second {
					t.Fatalf("deadline %v want %v", deadline, tc.want)
				}
			})).ServeHTTP(httptest.NewRecorder(), request)
		})
	}
	parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest("GET", "/api/v1/instances/10000000-0000-4000-8000-000000000001/metrics/latest", nil).WithContext(parent)
	instanceHTTPTimeoutFilter(3*time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		actual, _ := r.Context().Deadline()
		want, _ := parent.Deadline()
		if !actual.Equal(want) {
			t.Fatal("parent deadline extended")
		}
	})).ServeHTTP(httptest.NewRecorder(), request)
}

func TestModelDevAdmissionHTTPBudgetPreservesOtherRoutesAndParent(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         time.Duration
	}{
		{"POST", "/api/v1/modeldev/executions", time.Hour},
		{"GET", "/api/v1/modeldev/artifacts/10000000-0000-4000-8000-000000000001/content", time.Hour},
		{"POST", "/api/v1/modeldev/artifacts/10000000-0000-4000-8000-000000000001/content", 3 * time.Second},
		{"GET", "/api/v1/modeldev/executions", 3 * time.Second},
		{"POST", "/api/v1/modeldev/executions/", 3 * time.Second},
		{"POST", "/api/v1/modeldev/executions/10000000-0000-4000-8000-000000000001/stop", 3 * time.Second},
		{"POST", "/admin/permissions", 3 * time.Second},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			instanceHTTPTimeoutFilter(3*time.Second, time.Hour)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > tc.want || time.Until(deadline) < tc.want-time.Second {
					t.Fatalf("deadline %v want %v", deadline, tc.want)
				}
			})).ServeHTTP(httptest.NewRecorder(), request)
		})
	}
	parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest("POST", "/api/v1/modeldev/executions", nil).WithContext(parent)
	instanceHTTPTimeoutFilter(3*time.Second, time.Hour)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		actual, _ := r.Context().Deadline()
		want, _ := parent.Deadline()
		if !actual.Equal(want) {
			t.Fatal("parent deadline extended")
		}
		cancel()
		select {
		case <-r.Context().Done():
		default:
			t.Fatal("client cancellation lost")
		}
	})).ServeHTTP(httptest.NewRecorder(), request)
}

func TestModelDevAdmissionHTTPBudgetNeverDisablesDeadline(t *testing.T) {
	for _, budget := range []time.Duration{0, -1, 24*time.Hour + 1} {
		request := httptest.NewRequest("POST", "/api/v1/modeldev/executions", nil)
		instanceHTTPTimeoutFilter(3*time.Second, budget)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > 3*time.Second {
				t.Fatal("invalid admission budget removed ordinary bounded deadline")
			}
		})).ServeHTTP(httptest.NewRecorder(), request)
	}
}

func TestModelImportAndRegistrationBudgetsAreExactAndFinite(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         time.Duration
	}{
		{"POST", "/api/v1/model-versions", 2 * time.Hour},
		{"POST", "/api/v1/models/10000000-0000-4000-8000-000000000001/upload-url", 2 * time.Hour},
		{"GET", "/api/v1/model-versions", 3 * time.Second},
		{"POST", "/api/v1/model-versions/", 3 * time.Second},
		{"POST", "/api/v1/models/not-a-uuid/upload-url", 3 * time.Second},
		{"GET", "/api/v1/models/10000000-0000-4000-8000-000000000001/upload-url", 3 * time.Second},
		{"POST", "/api/v1/modeldev/executions/10000000-0000-4000-8000-000000000001:resume-model-registration", time.Hour},
		{"GET", "/api/v1/modeldev/executions/10000000-0000-4000-8000-000000000001:resume-model-registration", 3 * time.Second},
		{"POST", "/api/v1/modeldev/executions/not-a-uuid:resume-model-registration", 3 * time.Second},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			instanceHTTPTimeoutFilter(3*time.Second, time.Hour, 2*time.Hour)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > tc.want || time.Until(deadline) < tc.want-time.Second {
					t.Fatalf("deadline %v want %v", deadline, tc.want)
				}
			})).ServeHTTP(httptest.NewRecorder(), request)
		})
	}
	for _, budget := range []time.Duration{0, -1, 24*time.Hour + 1} {
		request := httptest.NewRequest("POST", "/api/v1/model-versions", nil)
		instanceHTTPTimeoutFilter(3*time.Second, time.Hour, budget)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			deadline, ok := r.Context().Deadline()
			if !ok || time.Until(deadline) > 3*time.Second {
				t.Fatal("invalid model budget removed ordinary bounded deadline")
			}
		})).ServeHTTP(httptest.NewRecorder(), request)
	}
	parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest("POST", "/api/v1/model-versions", nil).WithContext(parent)
	instanceHTTPTimeoutFilter(3*time.Second, time.Hour, 2*time.Hour)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		actual, _ := r.Context().Deadline()
		want, _ := parent.Deadline()
		if !actual.Equal(want) {
			t.Fatal("parent import deadline extended")
		}
		cancel()
		select {
		case <-r.Context().Done():
		default:
			t.Fatal("parent cancellation lost")
		}
	})).ServeHTTP(httptest.NewRecorder(), request)
}
