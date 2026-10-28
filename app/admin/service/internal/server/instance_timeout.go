package server

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"go-wind-admin/app/admin/service/internal/data"
)

var instanceLogsHTTPPath = regexp.MustCompile(`^/api/v1/instances/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/logs$`)

// The training log stream is the second long-lived SSE channel. Its own handler
// owns the bounded lifetime, so the shared ordinary timeout must not cut it.
var modelDevLogStreamHTTPPath = regexp.MustCompile(`^/api/v1/modeldev/executions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/logs/stream$`)
var instanceMetricsHTTPPath = regexp.MustCompile(`^/api/v1/instances/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/metrics/(query|latest)$`)
var modelDevMetricsHTTPPath = regexp.MustCompile(`^/api/v1/modeldev/executions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/resource-metrics$`)
var modelDevArtifactHTTPPath = regexp.MustCompile(`^/api/v1/modeldev/artifacts/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/content$`)

// The byte relay streams a checkpoint of up to a few GiB, so the ordinary
// request timer must not cut it. Its handler owns a bounded lifetime.
var modelDevArtifactDownloadHTTPPath = regexp.MustCompile(`^/api/v1/modeldev/artifacts/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/download$`)
var modelDevRegistrationHTTPPath = regexp.MustCompile(`^/api/v1/modeldev/executions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:resume-model-registration$`)
var modelUploadHTTPPath = regexp.MustCompile(`^/api/v1/models/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/upload-url$`)

// 组件日志流是第三类 SSE 通道：它自己持有 8 秒契约生命周期，普通 1 秒
// HTTP 超时不能覆盖它。只豁免这一个 GET 路径，不按 /api/v1/platform/ 前缀放行。
var platformComponentLogStreamHTTPPath = regexp.MustCompile(`^/api/v1/platform/components/[^/]+/logs/stream$`)

// Kratos' one server timeout also covers streaming handlers. Disable that
// global timer and retain its configured deadline here for ordinary requests.
// The dedicated log handler owns its bounded lifetime; neither path detaches
// the original request's cancellation.
func instanceHTTPTimeoutFilter(ordinary time.Duration, ownerBudgets ...time.Duration) func(http.Handler) http.Handler {
	var admissionBudget, modelImportBudget time.Duration
	// Optional owner budgets are ModelDev followed by Model. Every route also
	// inherits an earlier parent deadline and cancellation.
	if len(ownerBudgets) >= 1 && len(ownerBudgets) <= 2 && ownerBudgets[0] > 0 && ownerBudgets[0] <= 24*time.Hour {
		admissionBudget = ownerBudgets[0]
	}
	if len(ownerBudgets) == 2 && ownerBudgets[1] > 0 && ownerBudgets[1] <= 24*time.Hour {
		modelImportBudget = ownerBudgets[1]
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			var cancel context.CancelFunc
			metrics := instanceMetricsHTTPPath.MatchString(r.URL.Path) && (r.Method == http.MethodPost && r.URL.Path[len(r.URL.Path)-6:] == "/query" || r.Method == http.MethodGet && r.URL.Path[len(r.URL.Path)-7:] == "/latest") ||
				r.Method == http.MethodGet && modelDevMetricsHTTPPath.MatchString(r.URL.Path)
			// 首页资源趋势：30d 档 obs 批次预算 15s，gov 客户端 20s，HTTP 预算留余量到 25s。
			trend := r.Method == http.MethodGet && r.URL.Path == data.ObservabilityResourceTrendPath
			modeldev := r.Method == http.MethodPost && (r.URL.Path == "/api/v1/modeldev/executions" || modelDevRegistrationHTTPPath.MatchString(r.URL.Path)) || r.Method == http.MethodGet && modelDevArtifactHTTPPath.MatchString(r.URL.Path)
			modelImport := r.Method == http.MethodPost && (r.URL.Path == "/api/v1/model-versions" || modelUploadHTTPPath.MatchString(r.URL.Path))
			if r.Method == http.MethodGet && resourceUsageHTTPPath.MatchString(r.URL.Path) {
				ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
			} else if gpuHTTPPath.MatchString(r.URL.Path) {
				ctx, cancel = context.WithTimeout(ctx, 35*time.Second)
			} else if modelImport && modelImportBudget > 0 {
				// CreateModelVersion verifies the entire immutable archive before
				// returning READY; keep its finite owner budget through HTTP.
				ctx, cancel = context.WithTimeout(ctx, modelImportBudget)
			} else if modeldev && admissionBudget > 0 {
				// Prepare may verify an immutable base-model archive. Its finite
				// owner RPC budget also bounds short-lived S3 download grants.
				ctx, cancel = context.WithTimeout(ctx, admissionBudget)
			} else if trend {
				ctx, cancel = context.WithTimeout(ctx, 25*time.Second)
			} else if metrics {
				ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
			} else if ordinary > 0 && !(r.Method == http.MethodGet && (instanceLogsHTTPPath.MatchString(r.URL.Path) || modelDevLogStreamHTTPPath.MatchString(r.URL.Path) || modelDevArtifactDownloadHTTPPath.MatchString(r.URL.Path) || platformComponentLogStreamHTTPPath.MatchString(r.URL.Path))) {
				ctx, cancel = context.WithTimeout(ctx, ordinary)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
