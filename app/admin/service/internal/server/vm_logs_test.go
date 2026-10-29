package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	vmres "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const vmLogVMID = "30000000-0000-4000-8000-000000000003"

type vmLogAuthorization struct{ deny bool }

func (a *vmLogAuthorization) Authorize(_ context.Context, tenant, actor uint32, path, method string) error {
	if a.deny || tenant != 7 || actor != 9 || method != "GET" || path != data.VMLogsPath {
		return data.ErrActionAuthorizationDenied
	}
	return nil
}

type vmLogTenants struct{}

func (vmLogTenants) ResourceTenantID(_ context.Context, id uint32) (string, error) {
	if id != 7 {
		return "", status.Error(codes.PermissionDenied, "VM_SCOPE_DENIED")
	}
	return diagnosticTenantID, nil
}

type vmLogResource struct {
	service.VMOwnerClient
	open   func(context.Context) (grpc.ServerStreamingClient[vmres.VMLogFrame], error)
	calls  atomic.Int32
	tail   uint32
	follow bool
}

func (p *vmLogResource) Logs(ctx context.Context, tenant, actor, id string, tail uint32, follow bool) (grpc.ServerStreamingClient[vmres.VMLogFrame], error) {
	if tenant != diagnosticTenantID || actor != "governance:user:9" || id != vmLogVMID {
		return nil, status.Error(codes.PermissionDenied, "VM_SCOPE_DENIED")
	}
	p.calls.Add(1)
	p.tail, p.follow = tail, follow
	return p.open(ctx)
}

type vmLogStream struct {
	grpc.ClientStream
	ctx    context.Context
	frames chan *vmres.VMLogFrame
}

func (s *vmLogStream) Recv() (*vmres.VMLogFrame, error) {
	select {
	case frame, ok := <-s.frames:
		if !ok {
			return nil, io.EOF
		}
		return frame, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func vmLogHTTPFixture(t *testing.T, peer *vmLogResource, a *vmLogAuthorization, limits InstanceLogLimits) *httptest.Server {
	t.Helper()
	server := khttp.NewServer(khttp.Timeout(0), khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, r any) (any, error) {
			return next(auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}), r)
		}
	}))
	vmService := service.NewVMService(nil, service.NewVMBinding(peer, a, "cluster"), vmLogTenants{}, &data.QuotaLedgerRepo{}, nil)
	RegisterVMLogsHTTP(server, vmService, limits)
	actual := httptest.NewServer(server)
	t.Cleanup(actual.Close)
	return actual
}

func vmFrames(values ...*vmres.VMLogFrame) chan *vmres.VMLogFrame {
	ch := make(chan *vmres.VMLogFrame, len(values))
	for _, value := range values {
		ch <- value
	}
	close(ch)
	return ch
}

func TestVMLogQueryStrict(t *testing.T) {
	for _, query := range []string{"tailLines=0", "tailLines=01", "tailLines=1001", "tailLines=1&tailLines=2", "follow=TRUE", "follow=", "tail_lines=2", "token=secret", "container=other", "bad=%zz"} {
		if _, err := decodeVMLogQuery(httptest.NewRequest("GET", "/logs?"+query, nil)); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
	parsed, err := decodeVMLogQuery(httptest.NewRequest("GET", "/logs?tailLines=1000&follow=false", nil))
	if err != nil || parsed.GetTailLines() != 1000 || parsed.GetFollow() {
		t.Fatalf("options: %v %v", parsed, err)
	}
}

func TestVMLogsHTTPReadyUTF8AndTerminalEvents(t *testing.T) {
	peer := &vmLogResource{open: func(ctx context.Context) (grpc.ServerStreamingClient[vmres.VMLogFrame], error) {
		return &vmLogStream{ctx: ctx, frames: vmFrames(
			&vmres.VMLogFrame{Kind: vmres.VMLogFrame_READY},
			&vmres.VMLogFrame{Kind: vmres.VMLogFrame_DATA, Data: []byte{0xe4, 0xbd}},
			&vmres.VMLogFrame{Kind: vmres.VMLogFrame_DATA, Data: []byte{0xa0, '\n', '"', 0xff}},
			&vmres.VMLogFrame{Kind: vmres.VMLogFrame_END, Reason: "EOF"},
		)}, nil
	}}
	actual := vmLogHTTPFixture(t, peer, &vmLogAuthorization{}, DefaultInstanceLogLimits())
	response, err := http.Get(actual.URL + "/api/v1/virtual-machines/" + vmLogVMID + "/logs?follow=false")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("headers: %d %#v", response.StatusCode, response.Header)
	}
	text, end := "", false
	for _, block := range strings.Split(string(raw), "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[1], "data: ") {
			continue
		}
		var value map[string]string
		if err = json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &value); err != nil {
			t.Fatal(err)
		}
		if lines[0] == "event: log" {
			text += value["text"]
		}
		if lines[0] == "event: end" {
			end = value["reason"] == "EOF"
		}
	}
	if text != "你\n\"�" || !end {
		t.Fatalf("SSE: %s text=%q", raw, text)
	}
	if peer.tail != 100 || peer.follow {
		t.Fatalf("independent defaults: %d %v", peer.tail, peer.follow)
	}
}

func TestVMLogsHTTPTargetUnavailable(t *testing.T) {
	peer := &vmLogResource{open: func(context.Context) (grpc.ServerStreamingClient[vmres.VMLogFrame], error) {
		return nil, status.Error(codes.FailedPrecondition, "VM_LOG_TARGET_UNAVAILABLE")
	}}
	actual := vmLogHTTPFixture(t, peer, &vmLogAuthorization{}, DefaultInstanceLogLimits())
	response, err := http.Get(actual.URL + "/api/v1/virtual-machines/" + vmLogVMID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body map[string]any
	if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 409 || body["reason"] != "VM_LOG_TARGET_UNAVAILABLE" {
		t.Fatalf("target error: %d %#v", response.StatusCode, body)
	}
}

func TestVMLogsHTTPDeniedAndLimit(t *testing.T) {
	peer := &vmLogResource{open: func(ctx context.Context) (grpc.ServerStreamingClient[vmres.VMLogFrame], error) {
		return &vmLogStream{ctx: ctx, frames: vmFrames(&vmres.VMLogFrame{Kind: vmres.VMLogFrame_READY}, &vmres.VMLogFrame{Kind: vmres.VMLogFrame_END, Reason: "EOF"})}, nil
	}}
	actual := vmLogHTTPFixture(t, peer, &vmLogAuthorization{deny: true}, DefaultInstanceLogLimits())
	response, err := http.Get(actual.URL + "/api/v1/virtual-machines/" + vmLogVMID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 || peer.calls.Load() != 0 {
		t.Fatalf("denied scope reached Resource: %d %d", response.StatusCode, peer.calls.Load())
	}
	h := &vmLogHTTP{limits: InstanceLogLimits{ProcessConnections: 1, TenantConnections: 1, InstanceConnections: 1}, tenants: map[string]int{}, vms: map[string]int{}}
	if _, err := h.acquire("t", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.acquire("t", "v"); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("connection limit not enforced: %v", err)
	}
}
