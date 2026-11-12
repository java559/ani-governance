package data

import (
	"context"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"

	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/telemetry"
)

// grpcPeerService is the fixed logical peer name for one outbound connection.
// It is never derived from the endpoint, DNS name or user headers.
type grpcPeerService string

const (
	peerResourceService      grpcPeerService = "ani-resource-service"
	peerAcceleratorService   grpcPeerService = "ani-accelerator-service"
	peerInferenceService     grpcPeerService = "ani-inference-service"
	peerModelService         grpcPeerService = "ani-model-service"
	peerModelDevService      grpcPeerService = "ani-modeldev-service"
	peerObservabilityService grpcPeerService = "ani-observability-service"
)

// grpcObservabilityOptions returns the dial options that instrument one
// outbound connection.
//
// The standard OTel client stats handler injects W3C trace metadata at the real
// RPC boundary; the wrapper only adds one completion record. TLS, identity
// metadata, timeouts and retry suppression stay the caller's responsibility.
func grpcObservabilityOptions(peer grpcPeerService, logger bLogger.Logger) []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithStatsHandler(&rpcStatsHandler{
			delegate: otelgrpc.NewClientHandler(),
			peer:     string(peer),
			logger:   logger,
		}),
	}
}

// rpcStatsHandler wraps a standard stats handler: it delegates all
// span/tag work and emits exactly one rpc.client.completed per RPC.
type rpcStatsHandler struct {
	delegate stats.Handler
	peer     string
	logger   bLogger.Logger
}

type rpcCallKey struct{}

type rpcCallState struct {
	method    string
	beginTime time.Time
	ctx       context.Context
}

// TagRPC returns the delegate's span context so later HandleRPC sees the client
// span, and remembers the full method for the completion record.
func (h *rpcStatsHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	ctx = h.delegate.TagRPC(ctx, info)
	return context.WithValue(ctx, rpcCallKey{}, &rpcCallState{
		method:    info.FullMethodName,
		beginTime: time.Now(),
		ctx:       ctx,
	})
}

// HandleRPC records the single completion event on stats.End.
func (h *rpcStatsHandler) HandleRPC(ctx context.Context, s stats.RPCStats) {
	h.delegate.HandleRPC(ctx, s)
	end, ok := s.(*stats.End)
	if !ok {
		return
	}
	state, _ := ctx.Value(rpcCallKey{}).(*rpcCallState)
	h.recordCompletion(ctx, state, end)
}

func (h *rpcStatsHandler) recordCompletion(ctx context.Context, state *rpcCallState, end *stats.End) {
	if h.logger == nil {
		return
	}
	method := ""
	begin := time.Now()
	if state != nil {
		method = state.method
		begin = state.beginTime
	}
	code := codes.OK
	if end.Error != nil {
		code = status.Code(end.Error)
	}
	duration := end.EndTime.Sub(begin)
	if duration < 0 {
		duration = 0
	}

	level := rpcCompletionLevel(code, false)
	attrs := []any{
		"event", "rpc.client.completed",
		"peer.service", h.peer,
		"rpc.method", method,
		"rpc.code", code.String(),
		"result", rpcResult(code, false),
		"duration_ms", float64(duration.Microseconds()) / 1000.0,
	}
	if end.Error != nil {
		attrs = append(attrs, telemetry.ErrorFields(end.Error)...)
	}
	logOutbound(h.logger, ctx, level, "outbound rpc completed", attrs...)

	// Dependency faults are recorded at the same real boundary; a scheduled
	// retry elsewhere is a separate WARN and never lowers this original error.
	if isDependencyFault(code) && end.Error != nil {
		failAttrs := []any{
			"event", "dependency.failed",
			"dependency", h.peer,
			"action", rpcAction(method),
			"rpc.method", method,
			"rpc.code", code.String(),
		}
		failAttrs = append(failAttrs, telemetry.ErrorFields(end.Error)...)
		h.logger.Error(ctx, "outbound dependency failed", failAttrs...)
	}
}

func (h *rpcStatsHandler) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	return h.delegate.TagConn(ctx, info)
}

func (h *rpcStatsHandler) HandleConn(ctx context.Context, s stats.ConnStats) {
	h.delegate.HandleConn(ctx, s)
}

// outboundOptions returns the instrumentation dial options for one connection,
// bound to the process logger installed at startup.
func outboundOptions(peer grpcPeerService) []grpc.DialOption {
	return grpcObservabilityOptions(peer, bLogger.GetLogger())
}

// outboundDial builds the full dial option list: the caller's existing TLS,
// identity, timeout and retry options stay first and unchanged, and trace
// instrumentation is appended once.
func outboundDial(peer grpcPeerService, base ...grpc.DialOption) []grpc.DialOption {
	out := make([]grpc.DialOption, 0, len(base)+1)
	out = append(out, base...)
	return append(out, outboundOptions(peer)...)
}

// rpcCompletionLevel applies the fixed gRPC code-to-level rule.
func rpcCompletionLevel(code codes.Code, callerCanceled bool) bLogger.Level {
	switch code {
	case codes.OK:
		return bLogger.LevelInfo
	case codes.InvalidArgument, codes.Unauthenticated, codes.PermissionDenied,
		codes.NotFound, codes.AlreadyExists, codes.FailedPrecondition,
		codes.Aborted, codes.OutOfRange, codes.ResourceExhausted:
		return bLogger.LevelWarn
	case codes.Canceled:
		if callerCanceled {
			return bLogger.LevelInfo
		}
		return bLogger.LevelError
	default:
		return bLogger.LevelError
	}
}

// rpcResult maps a code to the contract result vocabulary.
func rpcResult(code codes.Code, callerCanceled bool) string {
	switch code {
	case codes.OK:
		return "succeeded"
	case codes.Canceled:
		if callerCanceled {
			return "canceled"
		}
		return "failed"
	case codes.InvalidArgument, codes.Unauthenticated, codes.PermissionDenied,
		codes.NotFound, codes.AlreadyExists, codes.FailedPrecondition,
		codes.Aborted, codes.OutOfRange, codes.ResourceExhausted:
		return "rejected"
	default:
		return "failed"
	}
}

// isDependencyFault reports codes that mean the peer could not serve the call.
func isDependencyFault(code codes.Code) bool {
	switch code {
	case codes.Unknown, codes.Internal, codes.Unavailable, codes.DeadlineExceeded,
		codes.DataLoss, codes.Unimplemented, codes.Canceled:
		return true
	}
	return false
}

// rpcAction renders the last method segment as the business action.
func rpcAction(method string) string {
	for i := len(method) - 1; i >= 0; i-- {
		if method[i] == '/' {
			return method[i+1:]
		}
	}
	return method
}

func logOutbound(logger bLogger.Logger, ctx context.Context, level bLogger.Level, msg string, attrs ...any) {
	switch level {
	case bLogger.LevelDebug:
		logger.Debug(ctx, msg, attrs...)
	case bLogger.LevelWarn:
		logger.Warn(ctx, msg, attrs...)
	case bLogger.LevelError:
		logger.Error(ctx, msg, attrs...)
	default:
		logger.Info(ctx, msg, attrs...)
	}
}

// dialDependencyFailed records a failure that happens while preparing an
// outbound connection (certificate/CA/client creation) before the returned
// error is replaced by a fixed local message.
func dialDependencyFailed(peer grpcPeerService, action string, err error) {
	logger := bLogger.GetLogger()
	if logger == nil || err == nil {
		return
	}
	attrs := []any{
		"event", "dependency.failed",
		"dependency", string(peer),
		"action", action,
	}
	attrs = append(attrs, telemetry.ErrorFields(err)...)
	logger.Error(context.Background(), "outbound connection preparation failed", attrs...)
}
