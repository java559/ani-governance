package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	vmres "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type vmLogHTTP struct {
	service      *service.VMService
	limits       InstanceLogLimits
	mu           sync.Mutex
	active       int
	opening      int
	tenants, vms map[string]int
}

func RegisterVMLogsHTTP(server *khttp.Server, s *service.VMService, configured ...InstanceLogLimits) {
	limits := DefaultInstanceLogLimits()
	if len(configured) > 1 {
		panic("multiple VM log configurations")
	}
	if len(configured) == 1 {
		limits = configured[0]
	}
	if err := limits.validate(); err != nil {
		panic(err)
	}
	h := &vmLogHTTP{service: s, limits: limits, tenants: map[string]int{}, vms: map[string]int{}}
	server.Route("/").GET(data.VMLogsPath, h.handle)
}

func decodeVMLogQuery(r *http.Request) (*view.VMLogsRequest, error) {
	invalid := kratoserrors.BadRequest("VM_INVALID_REQUEST", "invalid log options")
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalid
	}
	out := new(view.VMLogsRequest)
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

func (h *vmLogHTTP) handle(ctx khttp.Context) error {
	khttp.SetOperation(ctx, "/admin.service.v1.VMService/StreamVMLogs")
	r, err := decodeVMLogQuery(ctx.Request())
	if err != nil {
		return err
	}
	path := new(view.VMLogsRequest)
	if err = ctx.BindVars(path); err != nil {
		return err
	}
	r.VmId = path.VmId
	// Keep authentication/tracing middleware alive for the whole stream, and
	// retain the request cancellation chain installed by the HTTP listener.
	_, err = ctx.Middleware(func(call context.Context, in any) (any, error) {
		return nil, h.stream(call, ctx.Response(), in.(*view.VMLogsRequest))
	})(ctx, r)
	if err != nil {
		return vmLogHTTPError(err)
	}
	return nil
}

func (h *vmLogHTTP) acquire(tenant, id string) (func(), error) {
	key := tenant + "/" + id
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active >= h.limits.ProcessConnections || h.tenants[tenant] >= h.limits.TenantConnections || h.vms[key] >= h.limits.InstanceConnections {
		return nil, status.Error(codes.ResourceExhausted, "VM_LOG_CONNECTION_LIMIT")
	}
	h.active++
	h.tenants[tenant]++
	h.vms[key]++
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.active--
		h.tenants[tenant]--
		h.vms[key]--
		if h.tenants[tenant] == 0 {
			delete(h.tenants, tenant)
		}
		if h.vms[key] == 0 {
			delete(h.vms, key)
		}
	}, nil
}

func (h *vmLogHTTP) reserveOpen() (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active+h.opening >= h.limits.ProcessConnections {
		return nil, status.Error(codes.ResourceExhausted, "VM_LOG_CONNECTION_LIMIT")
	}
	h.opening++
	var once sync.Once
	return func() { once.Do(func() { h.mu.Lock(); defer h.mu.Unlock(); h.opening-- }) }, nil
}

type vmLogRead struct {
	frame *vmres.VMLogFrame
	err   error
}

func (h *vmLogHTTP) stream(parent context.Context, w http.ResponseWriter, r *view.VMLogsRequest) error {
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
	session, err := h.service.OpenVMLogs(call, r)
	if err != nil {
		if expired.Load() {
			return status.Error(codes.Unavailable, "VM_LOG_OPEN_TIMEOUT")
		}
		return err
	}
	release, err := h.acquire(session.TenantID, session.VMID)
	releaseOpening()
	if err != nil {
		return err
	}
	defer release()
	capacity := h.limits.MaxBufferedBytes/h.limits.MaxFrameBytes - 6
	if capacity > 8 {
		capacity = 8
	}
	reads := make(chan vmLogRead, capacity)
	go func() {
		for {
			frame, err := session.Stream.Recv()
			if err == nil && (frame == nil || len(frame.Data) > h.limits.MaxFrameBytes) {
				err = status.Error(codes.Internal, "VM_LOG_UPSTREAM_PROTOCOL")
			}
			select {
			case reads <- vmLogRead{frame, err}:
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
			return status.Error(codes.Unavailable, "VM_LOG_OPEN_TIMEOUT")
		}
		if first.err != nil {
			return vmLogBeforeReady(first.err)
		}
		if first.frame.Kind != vmres.VMLogFrame_READY || len(first.frame.Data) != 0 || first.frame.Reason != "" {
			return status.Error(codes.Unavailable, "VM_LOG_UPSTREAM_PROTOCOL")
		}
	case <-call.Done():
		if expired.Load() {
			return status.Error(codes.Unavailable, "VM_LOG_OPEN_TIMEOUT")
		}
		return call.Err()
	}
	opening.Stop()
	if expired.Load() {
		return status.Error(codes.Unavailable, "VM_LOG_OPEN_TIMEOUT")
	}
	if err = call.Err(); err != nil {
		return err
	}
	controller := http.NewResponseController(w)
	if err = controller.SetWriteDeadline(time.Now().Add(h.limits.WriteTimeout)); err != nil {
		return status.Error(codes.Unavailable, "VM_LOG_TRANSPORT_UNAVAILABLE")
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
					finish("error", vmLogPublicReason(result.err))
				}
				return nil
			}
			switch result.frame.Kind {
			case vmres.VMLogFrame_DATA:
				if result.frame.Reason != "" {
					finish("error", "VM_LOG_UPSTREAM_PROTOCOL")
					return nil
				}
				if text := decoder.decode(result.frame.Data, false); text != "" {
					if writer.text(text) != nil {
						return nil
					}
				}
			case vmres.VMLogFrame_END:
				reason := result.frame.Reason
				if len(result.frame.Data) != 0 || (reason != "EOF" && reason != "TIME_LIMIT" && reason != "POD_CHANGED") {
					finish("error", "VM_LOG_UPSTREAM_PROTOCOL")
				} else {
					finish("end", reason)
				}
				return nil
			case vmres.VMLogFrame_ERROR:
				if len(result.frame.Data) != 0 {
					finish("error", "VM_LOG_UPSTREAM_PROTOCOL")
				} else {
					finish("error", vmLogReason(result.frame.Reason))
				}
				return nil
			default:
				finish("error", "VM_LOG_UPSTREAM_PROTOCOL")
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

func vmLogBeforeReady(err error) error {
	if errors.Is(err, io.EOF) {
		return status.Error(codes.Unavailable, "VM_LOG_UPSTREAM_PROTOCOL")
	}
	return err
}

func vmLogReason(reason string) string {
	switch reason {
	case "VM_LOG_TARGET_UNAVAILABLE", "VM_LOG_UPSTREAM_UNAVAILABLE", "VM_LOG_UPSTREAM_PROTOCOL", "VM_LOG_CLIENT_TOO_SLOW", "VM_LOG_WRITE_TIMEOUT":
		return reason
	default:
		return "VM_LOG_UPSTREAM_UNAVAILABLE"
	}
}

func vmLogPublicReason(err error) string {
	return vmLogReason(status.Convert(err).Message())
}

// VM log errors keep their own reasons: a stopped VM is a precondition failure,
// and connection pressure maps to 429 rather than a generic dependency outage.
func vmLogHTTPError(err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.ResourceExhausted {
		return kratoserrors.New(429, "VM_LOG_CONNECTION_LIMIT", "VM log connection limit reached")
	}
	if message := status.Convert(err).Message(); message == "VM_LOG_TARGET_UNAVAILABLE" {
		return kratoserrors.New(409, message, "VM log target unavailable")
	}
	if message := status.Convert(err).Message(); strings.HasPrefix(message, "VM_LOG_") {
		return kratoserrors.New(503, vmLogReason(message), "VM log dependency unavailable")
	}
	return vmHTTPError(err)
}
