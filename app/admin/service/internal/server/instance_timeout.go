package server

import (
	"context"
	"net/http"
	"regexp"
	"time"
)

var instanceLogsHTTPPath = regexp.MustCompile(`^/api/v1/instances/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/logs$`)
var instanceMetricsHTTPPath = regexp.MustCompile(`^/api/v1/instances/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/metrics/(query|latest)$`)
var modelDevArtifactHTTPPath = regexp.MustCompile(`^/api/v1/modeldev/artifacts/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/content$`)
var modelDevRegistrationHTTPPath = regexp.MustCompile(`^/api/v1/modeldev/executions/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:resume-model-registration$`)
var modelUploadHTTPPath = regexp.MustCompile(`^/api/v1/models/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/upload-url$`)

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
			metrics := instanceMetricsHTTPPath.MatchString(r.URL.Path) && (r.Method == http.MethodPost && r.URL.Path[len(r.URL.Path)-6:] == "/query" || r.Method == http.MethodGet && r.URL.Path[len(r.URL.Path)-7:] == "/latest")
			modeldev := r.Method == http.MethodPost && (r.URL.Path == "/api/v1/modeldev/executions" || modelDevRegistrationHTTPPath.MatchString(r.URL.Path)) || r.Method == http.MethodGet && modelDevArtifactHTTPPath.MatchString(r.URL.Path)
			modelImport := r.Method == http.MethodPost && (r.URL.Path == "/api/v1/model-versions" || modelUploadHTTPPath.MatchString(r.URL.Path))
			if modelImport && modelImportBudget > 0 {
				// CreateModelVersion verifies the entire immutable archive before
				// returning READY; keep its finite owner budget through HTTP.
				ctx, cancel = context.WithTimeout(ctx, modelImportBudget)
			} else if modeldev && admissionBudget > 0 {
				// Prepare may verify an immutable base-model archive. Its finite
				// owner RPC budget also bounds short-lived S3 download grants.
				ctx, cancel = context.WithTimeout(ctx, admissionBudget)
			} else if metrics {
				ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
			} else if ordinary > 0 && !(r.Method == http.MethodGet && instanceLogsHTTPPath.MatchString(r.URL.Path)) {
				ctx, cancel = context.WithTimeout(ctx, ordinary)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
