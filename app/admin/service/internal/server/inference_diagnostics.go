package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const inferenceDiagnosticBase = "/api/v1/inference/services/{resource_id}"

type inferenceLogHTTP struct {
	service *service.InferenceService
	mu      sync.Mutex
	active  int
	tenants map[string]int
}

func RegisterInferenceDiagnosticsHTTP(server *khttp.Server, s *service.InferenceService) {
	h := &inferenceLogHTTP{service: s, tenants: make(map[string]int)}
	for _, endpoint := range []struct {
		suffix, operation string
		request           func() proto.Message
		invoke            func(context.Context, proto.Message) (proto.Message, error)
	}{
		{"/runtime", "GetRuntimeDiagnostics", func() proto.Message { return new(view.GetRuntimeDiagnosticsRequest) }, func(ctx context.Context, r proto.Message) (proto.Message, error) {
			return s.GetRuntimeDiagnostics(ctx, r.(*view.GetRuntimeDiagnosticsRequest))
		}},
		{"/logs", "GetRuntimeLogs", func() proto.Message { return new(view.RuntimeLogsRequest) }, func(ctx context.Context, r proto.Message) (proto.Message, error) {
			return s.GetRuntimeLogs(ctx, r.(*view.RuntimeLogsRequest))
		}},
		{"/events", "ListRuntimeEvents", func() proto.Message { return new(view.ListRuntimeEventsRequest) }, func(ctx context.Context, r proto.Message) (proto.Message, error) {
			return s.ListRuntimeEvents(ctx, r.(*view.ListRuntimeEventsRequest))
		}},
	} {
		server.Route("/").GET(inferenceDiagnosticBase+endpoint.suffix, func(ctx khttp.Context) error {
			khttp.SetOperation(ctx, "/admin.service.v1.InferenceDiagnosticsService/"+endpoint.operation)
			r := endpoint.request()
			if err := decodeInferenceDiagnosticsQuery(ctx.Request(), ctx.Vars().Get("resource_id"), r); err != nil {
				return err
			}
			out, err := ctx.Middleware(func(call context.Context, in any) (any, error) { return endpoint.invoke(call, in.(proto.Message)) })(ctx, r)
			if err != nil {
				return err
			}
			return ctx.Result(nethttp.StatusOK, out)
		})
	}
	server.Route("/").GET(inferenceDiagnosticBase+"/logs:stream", func(ctx khttp.Context) error {
		khttp.SetOperation(ctx, "/admin.service.v1.InferenceDiagnosticsService/StreamRuntimeLogs")
		r := new(view.RuntimeLogsRequest)
		if err := decodeInferenceDiagnosticsQuery(ctx.Request(), ctx.Vars().Get("resource_id"), r); err != nil {
			return err
		}
		_, err := ctx.Middleware(func(call context.Context, in any) (any, error) {
			return nil, h.stream(call, ctx.Response(), in.(*view.RuntimeLogsRequest))
		})(ctx, r)
		return err
	})
}

// Only documented filters are accepted; resource identity always comes from path.
func decodeInferenceDiagnosticsQuery(r *nethttp.Request, id string, out proto.Message) error {
	invalid := kratoserrors.BadRequest("INVALID_INFERENCE_DIAGNOSTICS", "invalid diagnostic filters")
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || id == "" {
		return invalid
	}
	switch v := out.(type) {
	case *view.GetRuntimeDiagnosticsRequest:
		v.ResourceId = id
	case *view.RuntimeLogsRequest:
		v.ResourceId = id
	case *view.ListRuntimeEventsRequest:
		v.ResourceId = id
	default:
		return invalid
	}
	for key, vs := range values {
		if len(vs) != 1 {
			return invalid
		}
		value := vs[0]
		switch key {
		case "since", "until":
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return invalid
			}
			stamp := timestamppb.New(parsed)
			if stamp.CheckValid() != nil {
				return invalid
			}
			switch v := out.(type) {
			case *view.RuntimeLogsRequest:
				if key == "since" {
					v.Since = stamp
				} else {
					v.Until = stamp
				}
			case *view.ListRuntimeEventsRequest:
				if key == "since" {
					v.Since = stamp
				} else {
					v.Until = stamp
				}
			default:
				return invalid
			}
		case "instance", "container":
			v, ok := out.(*view.RuntimeLogsRequest)
			if !ok {
				return invalid
			}
			if key == "instance" {
				v.Instance = value
			} else {
				v.Container = value
			}
		case "tail_lines":
			v, ok := out.(*view.RuntimeLogsRequest)
			if !ok {
				return invalid
			}
			n, e := strconv.ParseInt(value, 10, 64)
			if e != nil || n < 1 || n > 1000 {
				return invalid
			}
			v.TailLines = n
		case "page", "pageSize":
			v, ok := out.(*view.ListRuntimeEventsRequest)
			if !ok {
				return invalid
			}
			n, e := strconv.ParseInt(value, 10, 32)
			if e != nil || n < 1 {
				return invalid
			}
			if key == "page" {
				v.Page = int32(n)
			} else {
				if n > 100 {
					return invalid
				}
				v.PageSize = int32(n)
			}
		case "type":
			v, ok := out.(*view.ListRuntimeEventsRequest)
			if !ok {
				return invalid
			}
			v.Type = value
		default:
			return invalid
		}
	}
	return nil
}

func (h *inferenceLogHTTP) stream(parent context.Context, w nethttp.ResponseWriter, r *view.RuntimeLogsRequest) error {
	// Reserve process capacity before opening an upstream stream.
	h.mu.Lock()
	if h.active >= 32 {
		h.mu.Unlock()
		return kratoserrors.New(429, "INFERENCE_LOG_LIMIT", "too many log streams")
	}
	h.active++
	h.mu.Unlock()
	defer func() { h.mu.Lock(); h.active--; h.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	opening := time.AfterFunc(10*time.Second, cancel)
	defer opening.Stop()
	session, err := h.service.OpenRuntimeLogs(ctx, r)
	if err != nil {
		return err
	}
	h.mu.Lock()
	if h.tenants[session.TenantID] >= 4 {
		h.mu.Unlock()
		return kratoserrors.New(429, "INFERENCE_LOG_LIMIT", "too many tenant log streams")
	}
	h.tenants[session.TenantID]++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.tenants[session.TenantID]--
		if h.tenants[session.TenantID] == 0 {
			delete(h.tenants, session.TenantID)
		}
		h.mu.Unlock()
	}()
	type result struct {
		line proto.Message
		err  error
	}
	lines := make(chan result, 1)
	go func() {
		for {
			line, e := session.Stream.Recv()
			select {
			case lines <- result{line, e}:
			case <-ctx.Done():
				return
			}
			if e != nil {
				return
			}
		}
	}()
	// Receive first frame before committing HTTP 200, so owner errors retain status.
	var first result
	select {
	case first = <-lines:
	case <-ctx.Done():
		return kratoserrors.ServiceUnavailable("INFERENCE_LOG_UNAVAILABLE", "log stream unavailable")
	}
	opening.Stop()
	if first.err != nil && !errors.Is(first.err, io.EOF) {
		return serviceDiagnosticStreamError(first.err)
	}
	controller := nethttp.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func(event string, line proto.Message) error {
		raw := []byte("{}")
		if line != nil && line.ProtoReflect().IsValid() {
			var e error
			raw, e = (protojson.MarshalOptions{UseProtoNames: true}).Marshal(line)
			if e != nil {
				return e
			}
		}
		if len(raw) > 64*1024 {
			return fmt.Errorf("inference log frame too large")
		}
		if e := controller.SetWriteDeadline(time.Now().Add(10 * time.Second)); e != nil && !errors.Is(e, nethttp.ErrNotSupported) {
			return e
		}
		if _, e := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); e != nil {
			return e
		}
		return controller.Flush()
	}
	if errors.Is(first.err, io.EOF) {
		return write("done", nil)
	}
	if err = write("log", first.line); err != nil {
		return nil
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeat.C:
			if write("heartbeat", nil) != nil {
				return nil
			}
		case next := <-lines:
			if errors.Is(next.err, io.EOF) {
				_ = write("done", nil)
				return nil
			}
			if next.err != nil {
				_ = write("error", nil)
				return nil
			}
			if write("log", next.line) != nil {
				return nil
			}
		}
	}
}
func serviceDiagnosticStreamError(err error) error { return instanceHTTPError(err) }
