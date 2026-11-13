package server

import (
	"context"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/modeldevbudget"
	"go-wind-admin/pkg/middleware/auth"
)

var instanceLogsHTTPPath = regexp.MustCompile(`^/api/v1/instances/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/logs$`)

// The training log stream is the second long-lived SSE channel. Its own handler
// owns the bounded lifetime, so the shared ordinary timeout must not cut it.
var modelDevLogStreamHTTPPath = regexp.MustCompile(`^/api/v1/modeldev/executions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/logs/stream$`)
var instanceMetricsHTTPPath = regexp.MustCompile(`^/api/v1/instances/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/metrics/(query|latest)$`)
var inferenceRuntimeHTTPPath = regexp.MustCompile(`^/api/v1/inference/services/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/runtime$`)
var inferenceMetricsHTTPPath = regexp.MustCompile(`^/api/v1/inference/services/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/metrics$`)
var inferenceLogsHTTPPath = regexp.MustCompile(`^/api/v1/inference/services/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/logs$`)
var inferenceLogsStreamHTTPPath = regexp.MustCompile(`^/api/v1/inference/services/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/logs:stream$`)
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
	var admissionBudget, modelImportBudget, resolutionBudget time.Duration
	// Optional owner budgets are ModelDev followed by Model. Every route also
	// inherits an earlier parent deadline and cancellation.
	if len(ownerBudgets) >= 1 && len(ownerBudgets) <= 3 && ownerBudgets[0] > 0 && ownerBudgets[0] <= 24*time.Hour {
		admissionBudget = ownerBudgets[0]
	}
	if len(ownerBudgets) >= 2 && len(ownerBudgets) <= 3 && ownerBudgets[1] > 0 && ownerBudgets[1] <= 24*time.Hour {
		modelImportBudget = ownerBudgets[1]
	}
	if len(ownerBudgets) == 3 && ownerBudgets[2] > 0 && ownerBudgets[2] <= time.Duration(math.MaxUint32)*time.Second {
		resolutionBudget = ownerBudgets[2]
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
			readiness := r.Method == http.MethodPost && r.URL.Path == "/api/v1/modeldev/finetuning/readiness/check"
			create := r.Method == http.MethodPost && r.URL.Path == "/api/v1/modeldev/executions"
			modelImport := r.Method == http.MethodPost && (r.URL.Path == "/api/v1/model-versions" || modelUploadHTTPPath.MatchString(r.URL.Path))
			clusterOperation, _, clusterMatched := auth.MatchClusterOpsHTTP(r.Method, r.URL.Path)
			if clusterMatched && clusterOperation == clusterOpsLogsOperation {
				ctx, cancel = context.WithCancel(ctx)
			} else if clusterMatched {
				ctx, cancel = context.WithTimeout(ctx, 35*time.Second)
			} else if r.Method == http.MethodPost && r.URL.Path == data.ModelDevEnsureEnvironmentPath {
				ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
			} else if r.Method == http.MethodGet && r.URL.Path == data.ModelDevGetEnvironmentPath {
				ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
			} else if r.Method == http.MethodGet && (inferenceRuntimeHTTPPath.MatchString(r.URL.Path) || inferenceLogsHTTPPath.MatchString(r.URL.Path)) {
				// Runtime probes and log reads have a fifteen-second owner budget.
				ctx, cancel = context.WithTimeout(ctx, 20*time.Second)
			} else if r.Method == http.MethodGet && inferenceMetricsHTTPPath.MatchString(r.URL.Path) {
				// Runtime identity lookup precedes two bounded Prometheus reads.
				ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
			} else if r.Method == http.MethodGet && resourceUsageHTTPPath.MatchString(r.URL.Path) {
				ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
			} else if gpuHTTPPath.MatchString(r.URL.Path) {
				ctx, cancel = context.WithTimeout(ctx, 35*time.Second)
			} else if r.URL.Path == "/api/v1/sandbox" || strings.HasPrefix(r.URL.Path, "/api/v1/sandbox/") {
				// Sandbox may synchronously access a Pod; leave room for its 30s Resource RPC.
				budget := max(ordinary, 35*time.Second)
				ctx, cancel = context.WithTimeout(ctx, budget)
			} else if modelImport && modelImportBudget > 0 {
				// CreateModelVersion verifies the entire immutable archive before
				// returning READY; keep its finite owner budget through HTTP.
				ctx, cancel = context.WithTimeout(ctx, modelImportBudget)
			} else if readiness && resolutionBudget > 0 && admissionBudget > 0 {
				ctx, cancel = context.WithTimeout(ctx, resolutionBudget+admissionBudget)
			} else if create && resolutionBudget > 0 && admissionBudget > 0 {
				ctx, cancel = context.WithTimeout(ctx, resolutionBudget+4*admissionBudget)
			} else if modeldev && admissionBudget > 0 {
				// Other ModelDev commands and artifact grants retain the short B.
				ctx, cancel = context.WithTimeout(ctx, admissionBudget)
			} else if trend {
				ctx, cancel = context.WithTimeout(ctx, 25*time.Second)
			} else if metrics {
				ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
			} else if ordinary > 0 && !(r.Method == http.MethodGet && (instanceLogsHTTPPath.MatchString(r.URL.Path) || inferenceLogsStreamHTTPPath.MatchString(r.URL.Path) || modelDevLogStreamHTTPPath.MatchString(r.URL.Path) || modelDevArtifactDownloadHTTPPath.MatchString(r.URL.Path) || platformComponentLogStreamHTTPPath.MatchString(r.URL.Path))) {
				ctx, cancel = context.WithTimeout(ctx, ordinary)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			if (readiness || create) && resolutionBudget > 0 && admissionBudget > 0 {
				overallCancel := cancel
				var preflightCancel context.CancelFunc
				ctx, preflightCancel = modeldevbudget.BeginPreflight(ctx, admissionBudget)
				cancel = func() { preflightCancel(); overallCancel() }
			}
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// imageWriteLegacyRoutes 是既有租户镜像通道的 7 条写路由（method + path 模板），
// 与 14 条新管理写路由共用同一个 B_http 预算。
var imageWriteLegacyRoutes = [][2]string{
	{"POST", "/api/v1/images/space:enable"},
	{"POST", "/api/v1/images/publisher-credential:issue"},
	{"POST", "/api/v1/images/publisher-credential:reset"},
	{"POST", "/api/v1/images/publisher-credential:disable"},
	{"POST", "/api/v1/images/registrations"},
	{"PATCH", "/api/v1/images/registrations/{image_id}"},
	{"POST", "/api/v1/images/registrations/{image_id}:unregister"},
}

// imageWritePathMatch 做整段匹配：模板里 {name} 段匹配任意非空段，其余段必须
// 字面量相等。不按 /api/v1/platform/ 或 /api/v1/images/ 前缀放行。
// “参数段 + 固定后缀”（如 {image_id}:unregister）按前后缀分别比对：整段形式
// 既不以 '{' 开头也不以 '}' 结尾，若按字面量比较，三条注销路由都永远不命中。
func imageWritePathMatch(pattern, path string) bool {
	want := strings.Split(strings.Trim(pattern, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	if len(want) != len(got) {
		return false
	}
	for i, segment := range want {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			if got[i] == "" {
				return false
			}
			continue
		}
		if prefix, suffix, ok := splitParameterSegment(segment); ok {
			if !strings.HasPrefix(got[i], prefix) || !strings.HasSuffix(got[i], suffix) || len(got[i]) <= len(prefix)+len(suffix) {
				return false
			}
			continue
		}
		if segment != got[i] {
			return false
		}
	}
	return true
}

// splitParameterSegment 把 "{image_id}:unregister" 拆成 "{image_id}/", "/unregister"
// 形式的字面前缀与后缀；不是“参数 + 固定后缀”形态时返回 false。
func splitParameterSegment(segment string) (prefix, suffix string, ok bool) {
	open := strings.Index(segment, "{")
	if open < 0 {
		return "", "", false
	}
	close := strings.Index(segment, "}")
	if close < open {
		return "", "", false
	}
	prefix, suffix = segment[:open], segment[close+1:]
	return prefix, suffix, true
}

// imageWriteRouteMatch 判断请求是否命中 21 条镜像写路由：14 条新管理写
// （来自 ImageManagementWriteActions）加 7 条旧租户写。GET 与其它业务不命中。
func imageWriteRouteMatch(method, path string) bool {
	for action := range data.ImageManagementWriteActions {
		if route, ok := data.ImageManagementActionRoute(action); ok && route.Method == method && imageWritePathMatch(route.Path, path) {
			return true
		}
	}
	for _, route := range imageWriteLegacyRoutes {
		if route[0] == method && imageWritePathMatch(route[1], path) {
			return true
		}
	}
	return false
}

// imageWriteTimeoutFilter 在普通超时之上给镜像写路由单独的预算（B_http），
// 其余请求委托给 instanceHTTPTimeoutFilter 的既有分支。imageWriteBudget <= 0
// 表示镜像分支未配置，整个过滤器退化为原过滤器，0 不会被当成无限预算。
func imageWriteTimeoutFilter(ordinary time.Duration, imageWriteBudget time.Duration, ownerBudgets ...time.Duration) func(http.Handler) http.Handler {
	inner := instanceHTTPTimeoutFilter(ordinary, ownerBudgets...)
	if imageWriteBudget <= 0 {
		return inner
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if imageWriteRouteMatch(r.Method, r.URL.Path) {
				// 既有父 deadline/客户端取消仍然优先：WithTimeout 只提前不放宽。
				ctx, cancel := context.WithTimeout(r.Context(), imageWriteBudget)
				defer cancel()
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			inner(next).ServeHTTP(w, r)
		})
	}
}
