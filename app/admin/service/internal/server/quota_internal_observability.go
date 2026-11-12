package server

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"

	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/telemetry"
)

// quotaRPCKey carries the full method captured before dispatch. A unary
// interceptor never sees unknown methods or request-decode failures, so the
// completion record is taken from the transport stats boundary instead.
type quotaRPCKey struct{}

type quotaRPCState struct {
	method string
}

// quotaStatsHandler wraps the OTel server handler and emits exactly one
// rpc.server.completed per call, including decode failures and, together with
// UnknownServiceHandler, unknown services/methods.
type quotaStatsHandler struct {
	delegate stats.Handler
	logger   bLogger.Logger
}

func newQuotaStatsHandler(delegate stats.Handler, logger bLogger.Logger) stats.Handler {
	return &quotaStatsHandler{delegate: delegate, logger: logger}
}

func (h *quotaStatsHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	ctx = h.delegate.TagRPC(ctx, info)
	return context.WithValue(ctx, quotaRPCKey{}, &quotaRPCState{method: info.FullMethodName})
}

func (h *quotaStatsHandler) HandleRPC(ctx context.Context, s stats.RPCStats) {
	h.delegate.HandleRPC(ctx, s)
	end, ok := s.(*stats.End)
	if !ok {
		return
	}
	h.recordCompletion(ctx, end)
}

func (h *quotaStatsHandler) recordCompletion(ctx context.Context, end *stats.End) {
	// A noop tracer provider would otherwise drop trace_id from the completion
	// record; the HTTP entry uses the same fallback.
	ctx, _ = ensureSpanContext(ctx)
	state, _ := ctx.Value(quotaRPCKey{}).(*quotaRPCState)
	method := ""
	if state != nil {
		method = state.method
	}
	code := codes.OK
	if end.Error != nil {
		code = status.Code(end.Error)
	}
	level, result := quotaRPCCompletion(code)
	duration := end.EndTime.Sub(end.BeginTime)
	if duration < 0 {
		duration = 0
	}
	attrs := []any{
		"event", "rpc.server.completed",
		"rpc.method", method,
		"rpc.code", code.String(),
		"result", result,
		"duration_ms", float64(duration.Microseconds()) / 1000.0,
	}
	if end.Error != nil {
		attrs = append(attrs, telemetry.ErrorFields(end.Error)...)
	}
	logAt(h.logger, ctx, level, "inbound quota rpc completed", attrs...)
}

func (h *quotaStatsHandler) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	return h.delegate.TagConn(ctx, info)
}

func (h *quotaStatsHandler) HandleConn(ctx context.Context, s stats.ConnStats) {
	h.delegate.HandleConn(ctx, s)
}

// unknownQuotaMethod keeps the existing Unimplemented result for any method
// that is not registered on this listener, while routing it through the stats
// boundary so one completion event is still recorded.
func unknownQuotaMethod(any, grpc.ServerStream) error {
	return status.Error(codes.Unimplemented, "unknown quota release method")
}

// quotaRPCCompletion applies the shared gRPC completion rule: OK is INFO,
// request rejections are WARN, and all fault codes (including DeadlineExceeded
// and Unimplemented) are ERROR/failed.
func quotaRPCCompletion(code codes.Code) (bLogger.Level, string) {
	switch code {
	case codes.OK:
		return bLogger.LevelInfo, "succeeded"
	case codes.InvalidArgument, codes.Unauthenticated, codes.PermissionDenied,
		codes.NotFound, codes.AlreadyExists, codes.FailedPrecondition,
		codes.Aborted, codes.OutOfRange, codes.ResourceExhausted:
		return bLogger.LevelWarn, "rejected"
	default:
		return bLogger.LevelError, "failed"
	}
}
