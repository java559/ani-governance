package server

import (
	"context"
	"net/http"
	"regexp"
	"time"
)

var instanceLogsHTTPPath = regexp.MustCompile(`^/api/v1/instances/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/logs$`)

// Kratos' one server timeout also covers streaming handlers. Disable that
// global timer and retain its configured deadline here for ordinary requests.
// The dedicated log handler owns its bounded lifetime; neither path detaches
// the original request's cancellation.
func instanceHTTPTimeoutFilter(ordinary time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			var cancel context.CancelFunc
			if ordinary > 0 && !(r.Method == http.MethodGet && instanceLogsHTTPPath.MatchString(r.URL.Path)) {
				ctx, cancel = context.WithTimeout(ctx, ordinary)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
