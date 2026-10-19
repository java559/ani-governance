package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/emptypb"
)

func quotaLifecycleServer(t *testing.T) (*QuotaInternalServer, string, *x509.CertPool) {
	t.Helper()
	dir, pool := quotaTLSFixture(t)
	srv, err := NewQuotaInternalServer(QuotaInternalServerConfig{
		Enabled: true, Address: "127.0.0.1:0",
		CAFile: filepath.Join(dir, "ca.pem"), CertFile: filepath.Join(dir, "server.pem"),
		KeyFile:      filepath.Join(dir, "server.key"),
		CertOwnerMap: map[string]string{"ani-resource-service": "ani-resource-service"},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	return srv, dir, pool
}

func quotaLifecycleConn(t *testing.T, srv *QuotaInternalServer, dir string, pool *x509.CertPool) *grpc.ClientConn {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "owner.pem"), filepath.Join(dir, "owner.key"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, srv.listener.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: "ani-governance",
			Certificates: []tls.Certificate{pair},
		})), grpc.WithBlock())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func quotaStartAsync(srv *QuotaInternalServer) <-chan error {
	result := make(chan error, 1)
	go func() { result <- srv.Start(context.Background()) }()
	return result
}

func quotaStartResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("quota internal Start did not return")
		return nil
	}
}

func TestQuotaInternalStopBeforeStartReleasesListener(t *testing.T) {
	srv, _, _ := quotaLifecycleServer(t)
	addr := srv.listener.Addr().String()
	require.NoError(t, srv.Stop(context.Background()))
	require.NoError(t, srv.Stop(context.Background()))
	lis, err := net.Listen("tcp", addr)
	require.NoError(t, err, "constructor listener must be closed after assembly rollback")
	require.NoError(t, lis.Close())
	require.Error(t, srv.Start(context.Background()), "a stopped server must not restart")
}

func TestQuotaInternalStartBlocksAndConcurrentStop(t *testing.T) {
	srv, dir, pool := quotaLifecycleServer(t)
	result := quotaStartAsync(srv)
	_ = quotaLifecycleConn(t, srv, dir, pool)
	select {
	case err := <-result:
		t.Fatalf("Start returned before Stop: %v", err)
	default:
	}
	require.Error(t, srv.Start(context.Background()), "duplicate Start must be rejected")

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- srv.Stop(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NoError(t, quotaStartResult(t, result))
	require.NoError(t, srv.Stop(context.Background()))
	lis, err := net.Listen("tcp", srv.listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, lis.Close())
}

type quotaBlockingService interface {
	Block(context.Context, *emptypb.Empty) (*emptypb.Empty, error)
}

type quotaBlockingHandler struct{ entered chan struct{} }

func (h *quotaBlockingHandler) Block(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	close(h.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func quotaRegisterBlocker(srv *QuotaInternalServer, entered chan struct{}) {
	srv.grpcServer.RegisterService(&grpc.ServiceDesc{
		ServiceName: "quota.test.Blocker",
		HandlerType: (*quotaBlockingService)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Block",
			Handler: func(impl any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
				req := new(emptypb.Empty)
				if err := dec(req); err != nil {
					return nil, err
				}
				return impl.(quotaBlockingService).Block(ctx, req)
			},
		}},
	}, &quotaBlockingHandler{entered: entered})
}

func TestQuotaInternalStopHonorsContextDeadline(t *testing.T) {
	srv, dir, pool := quotaLifecycleServer(t)
	entered := make(chan struct{})
	quotaRegisterBlocker(srv, entered)
	result := quotaStartAsync(srv)
	conn := quotaLifecycleConn(t, srv, dir, pool)
	callDone := make(chan error, 1)
	go func() {
		callDone <- conn.Invoke(context.Background(), "/quota.test.Blocker/Block", &emptypb.Empty{}, &emptypb.Empty{})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking RPC did not begin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, srv.Stop(ctx), context.DeadlineExceeded)
	require.NoError(t, quotaStartResult(t, result))
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("forced Stop did not terminate the active RPC")
	}
	require.NoError(t, srv.Stop(context.Background()))
}

func TestQuotaInternalStopHonorsCanceledContext(t *testing.T) {
	srv, dir, pool := quotaLifecycleServer(t)
	entered := make(chan struct{})
	quotaRegisterBlocker(srv, entered)
	result := quotaStartAsync(srv)
	conn := quotaLifecycleConn(t, srv, dir, pool)
	callDone := make(chan error, 1)
	go func() {
		callDone <- conn.Invoke(context.Background(), "/quota.test.Blocker/Block", &emptypb.Empty{}, &emptypb.Empty{})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking RPC did not begin")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, srv.Stop(ctx), context.Canceled)
	require.NoError(t, quotaStartResult(t, result))
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("forced Stop did not terminate the active RPC")
	}
	require.NoError(t, srv.Stop(context.Background()))
}

type quotaFailingListener struct {
	net.Listener
	err error
}

func (l *quotaFailingListener) Accept() (net.Conn, error) { return nil, l.err }

type quotaPeerServer struct {
	stopped chan struct{}
	once    sync.Once
}

func (s *quotaPeerServer) Start(ctx context.Context) error {
	<-s.stopped
	return nil
}

func (s *quotaPeerServer) Stop(context.Context) error {
	s.once.Do(func() { close(s.stopped) })
	return nil
}

func TestQuotaInternalServeErrorReachesKratosApp(t *testing.T) {
	srv, _, _ := quotaLifecycleServer(t)
	acceptErr := errors.New("quota accept fault")
	srv.listener = &quotaFailingListener{Listener: srv.listener, err: acceptErr}
	peer := &quotaPeerServer{stopped: make(chan struct{})}
	app := kratos.New(kratos.Server(srv, peer))
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, acceptErr)
	case <-time.After(5 * time.Second):
		t.Fatal("App.Run did not observe quota Serve failure")
	}
	select {
	case <-peer.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Kratos did not stop the other registered server")
	}
	require.NoError(t, srv.Stop(context.Background()))
}
