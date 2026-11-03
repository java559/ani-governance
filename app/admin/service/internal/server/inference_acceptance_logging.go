package server

import (
	"context"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware"
	view "go-wind-admin/api/gen/go/inference/service/v1"
)

// The async worker has no HTTP context. Record the durable operation returned
// to the client beside the HTTP request ID before that context is lost.
func inferenceAcceptanceLogging(logger log.Logger) middleware.Middleware {
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, request any) (any, error) {
			reply, err := next(ctx, request)
			if acceptance, ok := reply.(*view.InferenceAcceptance); ok && err == nil && acceptance.GetOperationId() != "" {
				_ = log.WithContext(ctx, logger).Log(log.LevelInfo, "msg", "inference operation accepted", "operation_id", acceptance.GetOperationId(), "resource_id", acceptance.GetResourceId())
			}
			return reply, err
		}
	}
}
