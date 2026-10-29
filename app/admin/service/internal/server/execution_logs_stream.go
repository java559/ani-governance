package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// executionLogOpener is the one capability the transport needs: authorize and
// open the upstream frame stream. Narrowing it keeps the HTTP/SSE code testable
// with a substituted upstream while the authorization stays in the service.
type executionLogOpener interface {
	OpenExecutionLogStream(context.Context, *modeldevcontractv1.GetExecutionLogStreamRequest) (*service.ExecutionLogSession, error)
}

// executionLogLimits reuses the instance-log transport protection. Only the
// gate key differs: a training log stream is counted per execution, not per
// instance, because one execution may span several worker Pods.
type executionLogHTTP struct {
	opener  executionLogOpener
	limits  InstanceLogLimits
	mu      sync.Mutex
	active  int
	opening int
	tenants map[string]int
	targets map[string]int
	tenant  func(context.Context) (uint32, error)
}

// RegisterExecutionLogStreamHTTP exposes the training log SSE channel. It is a
// separate route from the instance channel so each source keeps its own gate.
func RegisterExecutionLogStreamHTTP(server *khttp.Server, s *service.ModelDevService, limits InstanceLogLimits) {
	if err := limits.validate(); err != nil {
		panic(err)
	}
	h := &executionLogHTTP{limits: limits, tenants: map[string]int{}, targets: map[string]int{}}
	if s != nil {
		h.opener = s
	}
	server.Route("/").GET(data.ModelDevExecutionLogStreamPath, h.handle)
}

func decodeExecutionLogStreamQuery(r *http.Request) (*modeldevcontractv1.GetExecutionLogStreamRequest, error) {
	invalid := kratoserrors.BadRequest("INVALID_MODELDEV_QUERY", "invalid modeldev query")
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalid
	}
	out := new(modeldevcontractv1.GetExecutionLogStreamRequest)
	for key, list := range values {
		if len(list) != 1 {
			return nil, invalid
		}
		value := list[0]
		switch key {
		case "pod_uid":
			if !modelDevQueryCanonicalUUID(value) {
				return nil, invalid
			}
			podUID := value
			out.PodUid = &podUID
		case "container":
			container, err := modelDevQueryContainerPointer(value)
			if err != nil {
				return nil, invalid
			}
			out.Container = container
		case "previous":
			previous, err := modelDevQueryBoolPointer(value)
			if err != nil {
				return nil, invalid
			}
			out.Previous = previous
		case "sinceBytes":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 || n > 1<<30 || strconv.FormatInt(n, 10) != value {
				return nil, invalid
			}
			if n > 0 {
				since := n
				out.SinceBytes = &since
			}
		default:
			return nil, invalid
		}
	}
	if out.PodUid == nil {
		return nil, invalid
	}
	return out, nil
}

func (h *executionLogHTTP) handle(ctx khttp.Context) error {
	khttp.SetOperation(ctx, "/admin.service.v1.ModelDevService/StreamExecutionLogs")
	// A log stream is never cacheable, including its error responses.
	ctx.Response().Header().Set("Cache-Control", "no-store")
	ctx.Response().Header().Set("Pragma", "no-cache")
	ctx.Response().Header().Set("Referrer-Policy", "no-referrer")
	request, err := decodeExecutionLogStreamQuery(ctx.Request())
	if err != nil {
		return err
	}
	path := new(modeldevcontractv1.GetExecutionLogStreamRequest)
	if err = ctx.BindVars(path); err != nil {
		return err
	}
	request.ExecutionId = path.ExecutionId
	_, err = ctx.Middleware(func(call context.Context, in any) (any, error) {
		return nil, h.stream(call, ctx.Response(), in.(*modeldevcontractv1.GetExecutionLogStreamRequest))
	})(ctx, request)
	if err != nil {
		return executionLogHTTPError(err)
	}
	return nil
}

// executionLogHTTPError keeps a pre-stream failure an HTTP status the client can
// act on. Only the reviewed reasons reach the body; an upstream message never
// does.
func executionLogHTTPError(err error) error {
	switch status.Code(err) {
	case codes.ResourceExhausted:
		return kratoserrors.New(429, "EXECUTION_LOG_CONNECTION_LIMIT", "execution log connection limit reached")
	case codes.FailedPrecondition:
		return kratoserrors.New(412, executionLogPublicReason(err), "execution log unavailable")
	case codes.InvalidArgument:
		return kratoserrors.BadRequest("INVALID_MODELDEV_QUERY", "invalid modeldev query")
	case codes.NotFound:
		return kratoserrors.NotFound("RESOURCE_NOT_FOUND", "resource unavailable")
	default:
		return err
	}
}

func (h *executionLogHTTP) reserveOpen() (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active+h.opening >= h.limits.ProcessConnections {
		return nil, status.Error(codes.ResourceExhausted, "EXECUTION_LOG_CONNECTION_LIMIT")
	}
	h.opening++
	var once sync.Once
	return func() { once.Do(func() { h.mu.Lock(); defer h.mu.Unlock(); h.opening-- }) }, nil
}

func (h *executionLogHTTP) acquire(tenant, execution string) (func(), error) {
	key := tenant + "/" + execution
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active >= h.limits.ProcessConnections || h.tenants[tenant] >= h.limits.TenantConnections || h.targets[key] >= h.limits.InstanceConnections {
		return nil, status.Error(codes.ResourceExhausted, "EXECUTION_LOG_CONNECTION_LIMIT")
	}
	h.active++
	h.tenants[tenant]++
	h.targets[key]++
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.active--
		h.tenants[tenant]--
		h.targets[key]--
		if h.tenants[tenant] == 0 {
			delete(h.tenants, tenant)
		}
		if h.targets[key] == 0 {
			delete(h.targets, key)
		}
	}, nil
}

type executionLogRead struct {
	frame *modeldevcontractv1.GetExecutionLogStreamResponse
	err   error
}

func (h *executionLogHTTP) stream(parent context.Context, w http.ResponseWriter, r *modeldevcontractv1.GetExecutionLogStreamRequest) error {
	if h.opener == nil {
		return status.Error(codes.Unavailable, "EXECUTION_LOG_UPSTREAM_UNAVAILABLE")
	}
	releaseOpening, err := h.reserveOpen()
	if err != nil {
		return err
	}
	defer releaseOpening()
	call, cancel := context.WithTimeout(parent, h.limits.Lifetime)
	defer cancel()
	var expired atomic.Bool
	opening := time.AfterFunc(h.limits.OpenTimeout, func() { expired.Store(true); cancel() })
	defer opening.Stop()
	session, err := h.opener.OpenExecutionLogStream(call, r)
	if err != nil {
		if expired.Load() {
			return status.Error(codes.Unavailable, "EXECUTION_LOG_OPEN_TIMEOUT")
		}
		// Nothing has been written to the client yet, so an upstream failure
		// must stay an HTTP error and must not echo an upstream message.
		return executionLogOpenError(err)
	}
	release, err := h.acquire(session.TenantID, r.ExecutionId)
	releaseOpening()
	if err != nil {
		return err
	}
	defer release()
	capacity := h.limits.MaxBufferedBytes/h.limits.MaxFrameBytes - 6
	if capacity > 8 {
		capacity = 8
	}
	reads := make(chan executionLogRead, capacity)
	go func() {
		for {
			frame, err := session.Stream.Recv()
			if err == nil && (frame == nil || len(frame.Data) > h.limits.MaxFrameBytes) {
				err = status.Error(codes.Internal, "EXECUTION_LOG_UPSTREAM_PROTOCOL")
			}
			select {
			case reads <- executionLogRead{frame, err}:
			case <-call.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	// The first frame is the upstream READY. Nothing is written to the client
	// before it, so an unreachable Pod still produces a real HTTP error.
	select {
	case first := <-reads:
		if expired.Load() {
			return status.Error(codes.Unavailable, "EXECUTION_LOG_OPEN_TIMEOUT")
		}
		if first.err != nil {
			if errors.Is(first.err, io.EOF) {
				return status.Error(codes.Unavailable, "EXECUTION_LOG_UPSTREAM_PROTOCOL")
			}
			return executionLogOpenError(first.err)
		}
		if first.frame.Kind == modeldevcontractv1.FrameKind_FRAME_KIND_REJECTED {
			return status.Error(codes.FailedPrecondition, executionLogReason(first.frame.Reason))
		}
		if first.frame.Kind != modeldevcontractv1.FrameKind_FRAME_KIND_READY || len(first.frame.Data) != 0 || first.frame.Reason != "" {
			return status.Error(codes.Unavailable, "EXECUTION_LOG_UPSTREAM_PROTOCOL")
		}
	case <-call.Done():
		if expired.Load() {
			return status.Error(codes.Unavailable, "EXECUTION_LOG_OPEN_TIMEOUT")
		}
		return call.Err()
	}
	opening.Stop()
	if expired.Load() {
		return status.Error(codes.Unavailable, "EXECUTION_LOG_OPEN_TIMEOUT")
	}
	if err = call.Err(); err != nil {
		return err
	}
	controller := http.NewResponseController(w)
	if err = controller.SetWriteDeadline(time.Now().Add(h.limits.WriteTimeout)); err != nil {
		return status.Error(codes.Unavailable, "EXECUTION_LOG_TRANSPORT_UNAVAILABLE")
	}
	defer controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	if _, err = io.WriteString(w, ": ready\n\n"); err != nil {
		return nil
	}
	if err = controller.Flush(); err != nil {
		return nil
	}
	writer := instanceSSEWriter{w: w, controller: controller, timeout: h.limits.WriteTimeout, textChunk: (h.limits.MaxFrameBytes - 32) / 6}
	ticker := time.NewTicker(h.limits.Heartbeat)
	defer ticker.Stop()
	decoder := instanceLogUTF8{}
	var lastOffset int64
	send := func(text string, offset int64) error {
		return writer.event("log", struct {
			Text   string `json:"text"`
			Offset int64  `json:"offset"`
		}{text, offset})
	}
	finish := func(event, reason string) {
		if text := decoder.decode(nil, true); text != "" {
			if send(text, 0) != nil {
				return
			}
		}
		_ = writer.event(event, struct {
			Reason string `json:"reason"`
		}{reason})
	}
	for {
		select {
		case result := <-reads:
			if result.err != nil {
				if errors.Is(call.Err(), context.DeadlineExceeded) && parent.Err() == nil {
					finish("end", "TIME_LIMIT")
				} else if errors.Is(result.err, io.EOF) {
					finish("end", "LOG_ENDED")
				} else if call.Err() == nil {
					finish("end", executionLogPublicReason(result.err))
				}
				return nil
			}
			switch result.frame.Kind {
			case modeldevcontractv1.FrameKind_FRAME_KIND_DATA:
				if result.frame.Reason != "" {
					finish("error", "EXECUTION_LOG_UPSTREAM_PROTOCOL")
					return nil
				}
				// A frame may end inside a multi-byte rune. The decoded text is
				// then empty, but its offset still belongs to the client's
				// resume point, so an offset-only event is emitted.
				text := decoder.decode(result.frame.Data, false)
				if text != "" || result.frame.Offset != lastOffset {
					if send(text, result.frame.Offset) != nil {
						return nil
					}
					lastOffset = result.frame.Offset
				}
			case modeldevcontractv1.FrameKind_FRAME_KIND_CLOSED:
				if len(result.frame.Data) != 0 {
					finish("error", "EXECUTION_LOG_UPSTREAM_PROTOCOL")
				} else {
					finish("end", executionLogClosedReason(result.frame.Reason))
				}
				return nil
			case modeldevcontractv1.FrameKind_FRAME_KIND_REJECTED:
				if len(result.frame.Data) != 0 {
					finish("error", "EXECUTION_LOG_UPSTREAM_PROTOCOL")
				} else {
					finish("error", executionLogReason(result.frame.Reason))
				}
				return nil
			default:
				finish("error", "EXECUTION_LOG_UPSTREAM_PROTOCOL")
				return nil
			}
		case <-ticker.C:
			if writer.heartbeat() != nil {
				return nil
			}
		case <-call.Done():
			if errors.Is(call.Err(), context.DeadlineExceeded) && parent.Err() == nil {
				finish("end", "TIME_LIMIT")
			}
			return nil
		}
	}
}

// executionLogOpenError turns a pre-READY upstream failure into an HTTP error,
// because no SSE frame has been announced yet.
func executionLogOpenError(err error) error {
	switch status.Code(err) {
	case codes.NotFound, codes.PermissionDenied, codes.Unauthenticated:
		return status.Error(codes.NotFound, "resource unavailable")
	case codes.FailedPrecondition:
		return status.Error(codes.FailedPrecondition, executionLogPublicReason(err))
	case codes.InvalidArgument:
		return status.Error(codes.InvalidArgument, "invalid log selection")
	default:
		return status.Error(codes.Unavailable, "EXECUTION_LOG_UPSTREAM_UNAVAILABLE")
	}
}

// executionLogReason is the allow-list of public control-frame reasons.
func executionLogReason(reason string) string {
	switch reason {
	case "OFFSET_INVALID", "PREVIOUS_UNAVAILABLE", "POD_GONE", "STEP_LOGS_UNAVAILABLE", "LOG_UNAVAILABLE":
		return reason
	default:
		return "LOG_UNAVAILABLE"
	}
}

func executionLogClosedReason(reason string) string {
	switch reason {
	case "LOG_ENDED", "LIFETIME_EXCEEDED", "POD_GONE":
		return reason
	default:
		return "LOG_ENDED"
	}
}

func executionLogPublicReason(err error) string {
	message := status.Convert(err).Message()
	if message == "" {
		return "EXECUTION_LOG_UPSTREAM_UNAVAILABLE"
	}
	return executionLogReason(message)
}
