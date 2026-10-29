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
		// 组件日志流同样自带 8 秒契约生命周期；同一路径的 POST 与状态快照
		// 路由仍受普通超时。
		{"GET", "/api/v1/platform/components/ani-governance/logs/stream", false},
		{"POST", "/api/v1/platform/components/ani-governance/logs/stream", true},
		{"GET", "/api/v1/platform/components", true},
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

func TestModelDevResolutionHTTPBudgetUsesExactPhaseSum(t *testing.T) {
	const b, resolution = 3 * time.Second, 300 * time.Second
	for _, tc := range []struct {
		method, path string
		want         time.Duration
	}{
		{"POST", "/api/v1/modeldev/finetuning/readiness/check", resolution + b},
		{"POST", "/api/v1/modeldev/executions", resolution + 4*b},
		{"GET", "/api/v1/modeldev/executions", b},
		{"POST", "/api/v1/modeldev/executions/10000000-0000-4000-8000-000000000001:resume-model-registration", b},
		{"GET", "/api/v1/modeldev/artifacts/10000000-0000-4000-8000-000000000001/content", b},
		{"POST", "/api/v1/model-versions", 1800 * time.Second},
	} {
		t.Run(tc.path+tc.method, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			instanceHTTPTimeoutFilter(b, b, 1800*time.Second, resolution)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > tc.want || time.Until(deadline) < tc.want-time.Second {
					t.Fatalf("MODELDEV_HTTP_PHASE_SUM: remaining=%v want=%v", time.Until(deadline), tc.want)
				}
			})).ServeHTTP(httptest.NewRecorder(), request)
		})
	}
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want, _ := parent.Deadline()
	request := httptest.NewRequest("POST", "/api/v1/modeldev/finetuning/readiness/check", nil).WithContext(parent)
	instanceHTTPTimeoutFilter(b, b, 1800*time.Second, resolution)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		actual, _ := r.Context().Deadline()
		if !actual.Equal(want) {
			t.Fatal("earlier caller deadline extended")
		}
	})).ServeHTTP(httptest.NewRecorder(), request)
}

func TestModelDevHTTPPreflightCannotBorrowResolutionBudget(t *testing.T) {
	for _, path := range []string{"/api/v1/modeldev/finetuning/readiness/check", "/api/v1/modeldev/executions"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest("POST", path, nil)
			instanceHTTPTimeoutFilter(time.Second, 20*time.Millisecond, 1800*time.Second, 200*time.Millisecond)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				// This handler represents an authentication/authorization stage
				// that has not handed off to source resolution.
				select {
				case <-r.Context().Done():
					if r.Context().Err() != context.DeadlineExceeded {
						t.Fatalf("preflight returned cancellation instead of deadline: %v", r.Context().Err())
					}
				case <-time.After(100 * time.Millisecond):
					t.Fatal("MODELDEV_PREFLIGHT_SHARED_B: HTTP authentication must not consume R before the B phase")
				}
			})).ServeHTTP(httptest.NewRecorder(), request)
		})
	}
}

func TestInferenceDiagnosticHTTPBudgetsOnlyApplyToExactGETRoutes(t *testing.T) {
	base := "/api/v1/inference/services/10000000-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		method, path string
		want         time.Duration
	}{
		{"GET", base + "/runtime", 20 * time.Second},
		{"GET", base + "/metrics", 30 * time.Second},
		{"POST", base + "/runtime", 3 * time.Second},
		{"POST", base + "/metrics", 3 * time.Second},
		{"GET", base + "/runtime/extra", 3 * time.Second},
		{"GET", base + "/metrics/latest", 3 * time.Second},
		{"GET", base + "/logs", 20 * time.Second},
		{"POST", base + "/logs", 3 * time.Second},
		{"GET", base + "/logs/extra", 3 * time.Second},
		{"GET", base + "/events", 3 * time.Second},
		{"GET", base + "/policy", 3 * time.Second},
		{"GET", "/api/v1/inference/services/not-a-uuid/runtime", 3 * time.Second},
		{"GET", "/api/v1/inference/services/not-a-uuid/metrics", 3 * time.Second},
		{"GET", "/api/v1/inference/services", 3 * time.Second},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			instanceHTTPTimeoutFilter(3*time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > tc.want || time.Until(deadline) < tc.want-time.Second {
					t.Fatalf("remaining budget %v want %v", time.Until(deadline), tc.want)
				}
			})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
		})
	}
	for _, suffix := range []string{"/runtime", "/metrics", "/logs"} {
		parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		instanceHTTPTimeoutFilter(3*time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			actual, _ := r.Context().Deadline()
			want, _ := parent.Deadline()
			if !actual.Equal(want) {
				t.Fatal("inference diagnostic extended caller deadline")
			}
			cancel()
			if r.Context().Err() != context.Canceled {
				t.Fatal("inference diagnostic lost caller cancellation")
			}
		})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", base+suffix, nil).WithContext(parent))
		cancel()
	}
}

func TestInferenceLogStreamHTTPBudgetPreservesHandlerLifetimeAndCaller(t *testing.T) {
	path := "/api/v1/inference/services/10000000-0000-4000-8000-000000000001/logs:stream"
	for _, tc := range []struct {
		method, path string
		stream       bool
	}{
		{"GET", path, true},
		{"POST", path, false},
		{"GET", path + "/extra", false},
		{"GET", "/api/v1/inference/services/not-a-uuid/logs:stream", false},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			instanceHTTPTimeoutFilter(3*time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				deadline, ok := r.Context().Deadline()
				if tc.stream {
					if ok {
						t.Fatal("ordinary HTTP deadline truncates the handler's bounded stream")
					}
				} else if !ok || time.Until(deadline) > 3*time.Second || time.Until(deadline) < 2*time.Second {
					t.Fatal("unrelated path lost its ordinary deadline")
				}
			})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
		})
	}
	parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	instanceHTTPTimeoutFilter(3*time.Second)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		actual, _ := r.Context().Deadline()
		want, _ := parent.Deadline()
		if !actual.Equal(want) {
			t.Fatal("stream extended caller deadline")
		}
		cancel()
		if r.Context().Err() != context.Canceled {
			t.Fatal("stream lost caller cancellation")
		}
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil).WithContext(parent))
}
