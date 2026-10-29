package data

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type inferenceDiagnosticsCall struct {
	remaining time.Duration
	metadata  metadata.MD
}

type inferenceDiagnosticsEndpoint struct {
	runtimev1.UnimplementedInferenceServiceManagerServer
	runtimev1.UnimplementedInferenceDiagnosticsServer
	seen chan inferenceDiagnosticsCall
}

func (s *inferenceDiagnosticsEndpoint) observe(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	deadline, _ := ctx.Deadline()
	s.seen <- inferenceDiagnosticsCall{remaining: time.Until(deadline), metadata: md}
}

func (s *inferenceDiagnosticsEndpoint) GetRuntimeDiagnostics(ctx context.Context, in *runtimev1.GetRuntimeDiagnosticsRequest) (*runtimev1.RuntimeDiagnostics, error) {
	s.observe(ctx)
	if in.ResourceId == "slow-probe" {
		timer := time.NewTimer(3100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	} else if in.ResourceId == "wait-for-caller" {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return &runtimev1.RuntimeDiagnostics{InvocationHealth: "healthy"}, nil
}

func (s *inferenceDiagnosticsEndpoint) GetInferenceService(ctx context.Context, in *runtimev1.GetInferenceServiceRequest) (*runtimev1.InferenceService, error) {
	s.observe(ctx)
	return &runtimev1.InferenceService{Id: in.ResourceId}, nil
}

func (s *inferenceDiagnosticsEndpoint) GetRuntimeLogs(ctx context.Context, in *runtimev1.RuntimeLogsRequest) (*runtimev1.RuntimeLogsResponse, error) {
	s.observe(ctx)
	if in.ResourceId == "slow-logs" {
		timer := time.NewTimer(3100 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	} else if in.ResourceId == "wait-for-caller" {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return &runtimev1.RuntimeLogsResponse{}, nil
}

func (s *inferenceDiagnosticsEndpoint) ListRuntimeEvents(ctx context.Context, _ *runtimev1.ListRuntimeEventsRequest) (*runtimev1.ListRuntimeEventsResponse, error) {
	s.observe(ctx)
	return &runtimev1.ListRuntimeEventsResponse{}, nil
}

func inferenceDiagnosticsTestClient(t *testing.T) (*InferenceClient, <-chan inferenceDiagnosticsCall) {
	t.Helper()
	f := resourceClientTestCertificates(t, nil, []string{"ani-inference"})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint := &inferenceDiagnosticsEndpoint{seen: make(chan inferenceDiagnosticsCall, 16)}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(f.server)))
	runtimev1.RegisterInferenceServiceManagerServer(server, endpoint)
	runtimev1.RegisterInferenceDiagnosticsServer(server, endpoint)
	done := make(chan error, 1)
	go func() { done <- server.Serve(lis) }()
	t.Cleanup(func() {
		server.Stop()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, net.ErrClosed) {
				t.Errorf("inference test endpoint shutdown: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("inference test endpoint did not stop")
		}
	})
	client, closeClient, err := NewInferenceClient(InferenceClientConfig{Address: lis.Addr().String(), ServerName: "ani-inference", CAFile: f.caFile, CertFile: f.certFile, KeyFile: f.keyFile, Timeout: 3 * time.Second})
	require.NoError(t, err)
	t.Cleanup(closeClient)
	return client, endpoint.seen
}

func TestInferenceRuntimeDiagnosticsHasSeparateBudgetAndTrustedMetadata(t *testing.T) {
	c, seen := inferenceDiagnosticsTestClient(t)
	tenant, actor := "11111111-1111-4111-8111-111111111111", "governance:user:7"
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "public bearer", "x-ani-tenant-id", "forged"))
	result, err := c.RuntimeDiagnosticsTrusted(ctx, tenant, actor, &runtimev1.GetRuntimeDiagnosticsRequest{ResourceId: "slow-probe"})
	require.NoError(t, err, "a diagnostic probe taking more than three seconds must finish")
	require.Equal(t, "healthy", result.InvocationHealth)
	call := <-seen
	require.Greater(t, call.remaining, 14*time.Second)
	require.LessOrEqual(t, call.remaining, 15*time.Second)
	require.Equal(t, []string{tenant}, call.metadata.Get("x-ani-tenant-id"))
	require.Equal(t, []string{actor}, call.metadata.Get("x-ani-actor"))
	require.Empty(t, call.metadata.Get("authorization"))
	ids := call.metadata.Get("x-ani-request-id")
	require.Len(t, ids, 1)
	_, err = uuid.Parse(ids[0])
	require.NoError(t, err)
	for _, invoke := range []func() error{
		func() error {
			_, err := c.GetTrusted(ctx, tenant, actor, &runtimev1.GetInferenceServiceRequest{})
			return err
		},
		func() error {
			_, err := c.RuntimeEventsTrusted(ctx, tenant, actor, &runtimev1.ListRuntimeEventsRequest{})
			return err
		},
	} {
		require.NoError(t, invoke())
		call := <-seen
		require.Greater(t, call.remaining, 2*time.Second)
		require.LessOrEqual(t, call.remaining, 3*time.Second, "ordinary unary calls must retain their three-second cap")
	}
}

func TestInferenceRuntimeLogsBudgetAndCallerCancellation(t *testing.T) {
	c, seen := inferenceDiagnosticsTestClient(t)
	tenant, actor := "11111111-1111-4111-8111-111111111111", "governance:user:7"
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "public bearer", "x-ani-tenant-id", "forged"))
	_, err := c.RuntimeLogsTrusted(ctx, tenant, actor, &runtimev1.RuntimeLogsRequest{ResourceId: "slow-logs"})
	require.NoError(t, err, "log read taking more than three seconds must finish")
	call := <-seen
	require.Greater(t, call.remaining, 14*time.Second)
	require.LessOrEqual(t, call.remaining, 15*time.Second)
	require.Equal(t, []string{tenant}, call.metadata.Get("x-ani-tenant-id"))
	require.Equal(t, []string{actor}, call.metadata.Get("x-ani-actor"))
	require.Empty(t, call.metadata.Get("authorization"))

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = c.RuntimeLogsTrusted(ctx, tenant, actor, &runtimev1.RuntimeLogsRequest{ResourceId: "wait-for-caller"})
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	require.LessOrEqual(t, (<-seen).remaining, 150*time.Millisecond)

	ctx, cancelCall := context.WithCancel(context.Background())
	defer cancelCall()
	done := make(chan error, 1)
	go func() {
		_, err := c.RuntimeLogsTrusted(ctx, tenant, actor, &runtimev1.RuntimeLogsRequest{ResourceId: "wait-for-caller"})
		done <- err
	}()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("log request did not start")
	}
	cancelCall()
	select {
	case err := <-done:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(time.Second):
		t.Fatal("log request ignored caller cancellation")
	}
}

func TestInferenceRuntimeDiagnosticsPreservesCallerDeadlineAndCancellation(t *testing.T) {
	c, seen := inferenceDiagnosticsTestClient(t)
	tenant, actor := "11111111-1111-4111-8111-111111111111", "governance:user:7"
	_, err := c.GetTrusted(context.Background(), tenant, actor, &runtimev1.GetInferenceServiceRequest{})
	require.NoError(t, err)
	<-seen
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = c.RuntimeDiagnosticsTrusted(ctx, tenant, actor, &runtimev1.GetRuntimeDiagnosticsRequest{ResourceId: "wait-for-caller"})
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	require.LessOrEqual(t, (<-seen).remaining, 150*time.Millisecond)
	ctx, cancelCall := context.WithCancel(context.Background())
	defer cancelCall()
	done := make(chan error, 1)
	go func() {
		_, err := c.RuntimeDiagnosticsTrusted(ctx, tenant, actor, &runtimev1.GetRuntimeDiagnosticsRequest{ResourceId: "wait-for-caller"})
		done <- err
	}()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("diagnostics request did not start")
	}
	cancelCall()
	select {
	case err := <-done:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(time.Second):
		t.Fatal("diagnostics ignored caller cancellation")
	}
}
