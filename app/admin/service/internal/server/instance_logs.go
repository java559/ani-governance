package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// InstanceLogLimits is operator-owned transport protection, never public input.
// Buffers are per connection; counters deliberately remain process-local.
type InstanceLogLimits struct {
	MaxFrameBytes, MaxBufferedBytes                            int
	OpenTimeout, WriteTimeout, Heartbeat, Lifetime             time.Duration
	ProcessConnections, TenantConnections, InstanceConnections int
}

func DefaultInstanceLogLimits() InstanceLogLimits {
	return InstanceLogLimits{MaxFrameBytes: 16 * 1024, MaxBufferedBytes: 256 * 1024, OpenTimeout: 15 * time.Second, WriteTimeout: 10 * time.Second, Heartbeat: 15 * time.Second, Lifetime: 10 * time.Minute, ProcessConnections: 100, TenantConnections: 10, InstanceConnections: 3}
}
func (l InstanceLogLimits) validate() error {
	if l.MaxFrameBytes < 128 || l.MaxFrameBytes > 16*1024 || l.MaxBufferedBytes < 8*l.MaxFrameBytes || l.MaxBufferedBytes > 1024*1024 || l.OpenTimeout <= 0 || l.WriteTimeout <= 0 || l.Heartbeat <= 0 || l.Lifetime <= 0 || l.ProcessConnections < 1 || l.TenantConnections < 1 || l.InstanceConnections < 1 {
		return fmt.Errorf("invalid instance log limits")
	}
	return nil
}

type instanceLogHTTP struct {
	service            *service.InstanceService
	limits             InstanceLogLimits
	mu                 sync.Mutex
	active             int
	opening            int
	tenants, instances map[string]int
}

// A deployment may supply validated operator limits. Invalid configuration is
// rejected at registration, before the listener starts accepting requests.
func RegisterInstanceDiagnosticsHTTP(server *khttp.Server, s *service.InstanceService, configured ...InstanceLogLimits) {
	limits := DefaultInstanceLogLimits()
	if len(configured) > 1 {
		panic("multiple instance log configurations")
	}
	if len(configured) == 1 {
		limits = configured[0]
	}
	if err := limits.validate(); err != nil {
		panic(err)
	}
	h := &instanceLogHTTP{service: s, limits: limits, tenants: map[string]int{}, instances: map[string]int{}}
	router := server.Route("/")
	for _, endpoint := range []struct {
		path, operation string
		invoke          func(context.Context, *view.GetInstanceRequest) (proto.Message, error)
	}{
		{data.InstanceRuntimePath, "/admin.service.v1.InstanceService/GetInstanceRuntime", func(ctx context.Context, r *view.GetInstanceRequest) (proto.Message, error) {
			return s.GetInstanceRuntime(ctx, r)
		}},
		{data.InstanceEventsPath, "/admin.service.v1.InstanceService/ListInstanceEvents", func(ctx context.Context, r *view.GetInstanceRequest) (proto.Message, error) {
			return s.ListInstanceEvents(ctx, r)
		}},
	} {
		endpoint := endpoint
		router.GET(endpoint.path, func(ctx khttp.Context) error {
			khttp.SetOperation(ctx, endpoint.operation)
			r := new(view.GetInstanceRequest)
			if err := decodeInstanceQuery(ctx.Request(), r); err != nil {
				return err
			}
			if err := ctx.BindVars(r); err != nil {
				return err
			}
			out, err := ctx.Middleware(func(call context.Context, in any) (any, error) {
				return endpoint.invoke(call, in.(*view.GetInstanceRequest))
			})(ctx, r)
			if err != nil {
				return instanceHTTPError(err)
			}
			return ctx.Result(http.StatusOK, out)
		})
	}
	router.GET(data.InstanceLogsPath, h.handle)
}

func decodeInstanceLogQuery(r *http.Request) (*view.InstanceLogsRequest, error) {
	invalid := kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid log options")
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalid
	}
	out := new(view.InstanceLogsRequest)
	for key, values := range values {
		if len(values) != 1 {
			return nil, invalid
		}
		value := values[0]
		switch key {
		case "tailLines":
			n, err := strconv.ParseUint(value, 10, 32)
			if err != nil || n < 1 || n > 1000 || strconv.FormatUint(n, 10) != value {
				return nil, invalid
			}
			tail := uint32(n)
			out.TailLines = &tail
		case "follow":
			if value != "true" && value != "false" {
				return nil, invalid
			}
			follow := value == "true"
			out.Follow = &follow
		default:
			return nil, invalid
		}
	}
	return out, nil
}

func (h *instanceLogHTTP) handle(ctx khttp.Context) error {
	khttp.SetOperation(ctx, "/admin.service.v1.InstanceService/StreamInstanceLogs")
	r, err := decodeInstanceLogQuery(ctx.Request())
	if err != nil {
		return err
	}
	path := new(view.InstanceLogsRequest)
	if err = ctx.BindVars(path); err != nil {
		return err
	}
	r.InstanceId = path.InstanceId
	// Keep authentication/tracing middleware alive for the whole stream, and
	// retain the request cancellation chain installed by the HTTP listener.
	_, err = ctx.Middleware(func(call context.Context, in any) (any, error) {
		return nil, h.stream(call, ctx.Response(), in.(*view.InstanceLogsRequest))
	})(ctx, r)
	if err != nil {
		if status.Code(err) == codes.ResourceExhausted {
			return kratoserrors.New(429, "INSTANCE_LOG_CONNECTION_LIMIT", "instance log connection limit reached")
		}
		return instanceHTTPError(err)
	}
	return nil
}

func (h *instanceLogHTTP) acquire(tenant, id string) (func(), error) {
	key := tenant + "/" + id
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active >= h.limits.ProcessConnections || h.tenants[tenant] >= h.limits.TenantConnections || h.instances[key] >= h.limits.InstanceConnections {
		return nil, status.Error(codes.ResourceExhausted, "INSTANCE_LOG_CONNECTION_LIMIT")
	}
	h.active++
	h.tenants[tenant]++
	h.instances[key]++
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.active--
		h.tenants[tenant]--
		h.instances[key]--
		if h.tenants[tenant] == 0 {
			delete(h.tenants, tenant)
		}
		if h.instances[key] == 0 {
			delete(h.instances, key)
		}
	}, nil
}

type instanceLogRead struct {
	frame *res.InstanceLogFrame
	err   error
}

func (h *instanceLogHTTP) reserveOpen() (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active+h.opening >= h.limits.ProcessConnections {
		return nil, status.Error(codes.ResourceExhausted, "INSTANCE_LOG_CONNECTION_LIMIT")
	}
	h.opening++
	var once sync.Once
	return func() { once.Do(func() { h.mu.Lock(); defer h.mu.Unlock(); h.opening-- }) }, nil
}

func (h *instanceLogHTTP) stream(parent context.Context, w http.ResponseWriter, r *view.InstanceLogsRequest) error {
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
	session, err := h.service.OpenInstanceLogs(call, r)
	if err != nil {
		if expired.Load() {
			return status.Error(codes.Unavailable, "INSTANCE_LOG_OPEN_TIMEOUT")
		}
		return err
	}
	release, err := h.acquire(session.TenantID, session.InstanceID)
	releaseOpening()
	if err != nil {
		return err
	}
	defer release()
	// Reserve space for receive/decode/JSON/write scratch in addition to queued
	// frames. JSON text reserves framing bytes before worst-case escaping (worst-case escaping).
	capacity := h.limits.MaxBufferedBytes/h.limits.MaxFrameBytes - 6
	if capacity > 8 {
		capacity = 8
	}
	reads := make(chan instanceLogRead, capacity)
	go func() {
		for {
			frame, err := session.Stream.Recv()
			if err == nil && (frame == nil || len(frame.Data) > h.limits.MaxFrameBytes) {
				err = status.Error(codes.Internal, "INSTANCE_LOG_UPSTREAM_PROTOCOL")
			}
			select {
			case reads <- instanceLogRead{frame, err}:
			case <-call.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case first := <-reads:
		if expired.Load() {
			return status.Error(codes.Unavailable, "INSTANCE_LOG_OPEN_TIMEOUT")
		}
		if first.err != nil {
			return instanceLogBeforeReady(first.err)
		}
		if first.frame.Kind != res.InstanceLogFrame_READY || len(first.frame.Data) != 0 || first.frame.Reason != "" {
			return status.Error(codes.Unavailable, "INSTANCE_LOG_UPSTREAM_PROTOCOL")
		}
	case <-call.Done():
		if expired.Load() {
			return status.Error(codes.Unavailable, "INSTANCE_LOG_OPEN_TIMEOUT")
		}
		return call.Err()
	}
	opening.Stop()
	if expired.Load() {
		return status.Error(codes.Unavailable, "INSTANCE_LOG_OPEN_TIMEOUT")
	}
	if err = call.Err(); err != nil {
		return err
	}
	controller := http.NewResponseController(w)
	if err = controller.SetWriteDeadline(time.Now().Add(h.limits.WriteTimeout)); err != nil {
		return status.Error(codes.Unavailable, "INSTANCE_LOG_TRANSPORT_UNAVAILABLE")
	}
	defer controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	// The first write/flush happens only after Resource's READY. No HTTP errors
	// may escape after this point, including write/flush failures.
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
	finish := func(event, reason string) {
		if text := decoder.decode(nil, true); text != "" {
			if writer.text(text) != nil {
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
					finish("end", "EOF")
				} else if call.Err() == nil {
					finish("error", instanceLogPublicReason(result.err))
				}
				return nil
			}
			switch result.frame.Kind {
			case res.InstanceLogFrame_DATA:
				if result.frame.Reason != "" {
					finish("error", "INSTANCE_LOG_UPSTREAM_PROTOCOL")
					return nil
				}
				if text := decoder.decode(result.frame.Data, false); text != "" {
					if writer.text(text) != nil {
						return nil
					}
				}
			case res.InstanceLogFrame_END:
				reason := result.frame.Reason
				if len(result.frame.Data) != 0 || (reason != "EOF" && reason != "TIME_LIMIT" && reason != "POD_CHANGED") {
					finish("error", "INSTANCE_LOG_UPSTREAM_PROTOCOL")
				} else {
					finish("end", reason)
				}
				return nil
			case res.InstanceLogFrame_ERROR:
				if len(result.frame.Data) != 0 {
					finish("error", "INSTANCE_LOG_UPSTREAM_PROTOCOL")
				} else {
					finish("error", instanceLogReason(result.frame.Reason))
				}
				return nil
			default:
				finish("error", "INSTANCE_LOG_UPSTREAM_PROTOCOL")
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

func instanceLogBeforeReady(err error) error {
	if errors.Is(err, io.EOF) {
		return status.Error(codes.Unavailable, "INSTANCE_LOG_UPSTREAM_PROTOCOL")
	}
	return err
}
func instanceLogReason(reason string) string {
	switch reason {
	case "INSTANCE_LOG_TARGET_UNAVAILABLE", "INSTANCE_LOG_UPSTREAM_UNAVAILABLE", "INSTANCE_LOG_UPSTREAM_PROTOCOL", "INSTANCE_LOG_CLIENT_TOO_SLOW", "INSTANCE_LOG_WRITE_TIMEOUT":
		return reason
	default:
		return "INSTANCE_LOG_UPSTREAM_UNAVAILABLE"
	}
}
func instanceLogPublicReason(err error) string {
	return instanceLogReason(status.Convert(err).Message())
}

type instanceSSEWriter struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
	textChunk  int
}

func (w *instanceSSEWriter) write(raw []byte) error {
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil {
		return err
	}
	if _, err := w.w.Write(raw); err != nil {
		return err
	}
	return w.controller.Flush()
}
func (w *instanceSSEWriter) heartbeat() error { return w.write([]byte(": heartbeat\n\n")) }
func (w *instanceSSEWriter) event(event string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	frame := append([]byte("event: "+event+"\ndata: "), raw...)
	frame = append(frame, '\n', '\n')
	return w.write(frame)
}
func (w *instanceSSEWriter) text(text string) error {
	for len(text) > 0 {
		size := len(text)
		if size > w.textChunk {
			size = w.textChunk
			for size > 0 && !utf8.RuneStart(text[size]) {
				size--
			}
		}
		if err := w.event("log", struct {
			Text string `json:"text"`
		}{text[:size]}); err != nil {
			return err
		}
		text = text[size:]
	}
	return nil
}

// At most three trailing bytes are retained between independent gRPC frames.
// Invalid sequences are display replacements; byte boundaries are not runes.
type instanceLogUTF8 struct{ pending []byte }

func (d *instanceLogUTF8) decode(raw []byte, final bool) string {
	input := make([]byte, 0, len(d.pending)+len(raw))
	input = append(input, d.pending...)
	input = append(input, raw...)
	d.pending = nil
	var out strings.Builder
	for len(input) > 0 {
		if !final && !utf8.FullRune(input) {
			d.pending = append(d.pending, input...)
			break
		}
		r, size := utf8.DecodeRune(input)
		out.WriteRune(r)
		input = input[size:]
	}
	return out.String()
}
