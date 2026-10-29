package data

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type inferenceKeyRPCPeer struct {
	runtimev1.UnimplementedInferenceAccessKeyServiceServer
}

func (*inferenceKeyRPCPeer) CreateInferenceAccessPolicy(context.Context, *runtimev1.CreateInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	return &runtimev1.InferenceAccessPolicy{}, nil
}
func (*inferenceKeyRPCPeer) ListInferenceAccessPolicies(context.Context, *runtimev1.ListInferenceAccessPoliciesRequest) (*runtimev1.ListInferenceAccessPoliciesResponse, error) {
	return &runtimev1.ListInferenceAccessPoliciesResponse{}, nil
}
func (*inferenceKeyRPCPeer) GetInferenceAccessPolicy(context.Context, *runtimev1.GetInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	return &runtimev1.InferenceAccessPolicy{}, nil
}
func (*inferenceKeyRPCPeer) UpdateInferenceAccessPolicy(context.Context, *runtimev1.UpdateInferenceAccessPolicyRequest) (*runtimev1.InferenceAccessPolicy, error) {
	return &runtimev1.InferenceAccessPolicy{}, nil
}
func (*inferenceKeyRPCPeer) DeleteInferenceAccessPolicy(context.Context, *runtimev1.DeleteInferenceAccessPolicyRequest) (*runtimev1.DeleteInferenceAccessPolicyResponse, error) {
	return &runtimev1.DeleteInferenceAccessPolicyResponse{}, nil
}
func (*inferenceKeyRPCPeer) BindInferenceAccessKeyPolicy(context.Context, *runtimev1.BindInferenceAccessKeyPolicyRequest) (*runtimev1.InferenceAccessKey, error) {
	return &runtimev1.InferenceAccessKey{}, nil
}

func (*inferenceKeyRPCPeer) CreateInferenceAccessKey(context.Context, *runtimev1.CreateInferenceAccessKeyRequest) (*runtimev1.CreateInferenceAccessKeyResponse, error) {
	return &runtimev1.CreateInferenceAccessKeyResponse{}, nil
}
func (*inferenceKeyRPCPeer) ListInferenceAccessKeys(context.Context, *runtimev1.ListInferenceAccessKeysRequest) (*runtimev1.ListInferenceAccessKeysResponse, error) {
	return &runtimev1.ListInferenceAccessKeysResponse{}, nil
}
func (*inferenceKeyRPCPeer) GetInferenceAccessKey(ctx context.Context, in *runtimev1.GetInferenceAccessKeyRequest) (*runtimev1.InferenceAccessKey, error) {
	if in.Id == "wait" {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return &runtimev1.InferenceAccessKey{}, nil
}
func (*inferenceKeyRPCPeer) RevokeInferenceAccessKey(context.Context, *runtimev1.RevokeInferenceAccessKeyRequest) (*runtimev1.RevokeInferenceAccessKeyResponse, error) {
	return &runtimev1.RevokeInferenceAccessKeyResponse{}, nil
}
func (*inferenceKeyRPCPeer) RotateInferenceAccessKey(context.Context, *runtimev1.RotateInferenceAccessKeyRequest) (*runtimev1.RotateInferenceAccessKeyResponse, error) {
	return &runtimev1.RotateInferenceAccessKeyResponse{}, nil
}

type inferenceKeyRPCCall struct {
	method     string
	metadata   metadata.MD
	remaining  time.Duration
	tlsVersion uint16
	clientDNS  []string
}

func TestInferenceKeyClientUsesManagedMTLSAndRebuiltMetadata(t *testing.T) {
	f := resourceClientTestCertificates(t, nil, []string{"ani-inference"})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	seen := make(chan inferenceKeyRPCCall, 16)
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(f.server)), grpc.UnaryInterceptor(func(ctx context.Context, in interface{}, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (interface{}, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		deadline, _ := ctx.Deadline()
		p, _ := peer.FromContext(ctx)
		state := p.AuthInfo.(credentials.TLSInfo).State
		seen <- inferenceKeyRPCCall{method: info.FullMethod, metadata: md, remaining: time.Until(deadline), tlsVersion: state.Version, clientDNS: state.PeerCertificates[0].DNSNames}
		return next(ctx, in)
	}))
	runtimev1.RegisterInferenceAccessKeyServiceServer(server, &inferenceKeyRPCPeer{})
	done := make(chan error, 1)
	go func() { done <- server.Serve(lis) }()
	t.Cleanup(func() {
		server.Stop()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, net.ErrClosed) {
				t.Errorf("close key peer: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("key peer did not stop")
		}
	})
	c, closeClient, err := NewInferenceClient(InferenceClientConfig{Address: lis.Addr().String(), ServerName: "ani-inference", CAFile: f.caFile, CertFile: f.certFile, KeyFile: f.keyFile, Timeout: 3 * time.Second})
	require.NoError(t, err)
	t.Cleanup(closeClient)
	tenant, actor := "11111111-1111-4111-8111-111111111111", "governance:user:33"
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "public-token", "x-api-key", "public-secret", "x-ani-tenant-id", "forged", "x-ani-actor", "forged"))
	for _, call := range []struct {
		method string
		invoke func() error
	}{
		{"CreateInferenceAccessPolicy", func() error {
			_, err := c.CreateInferencePolicyTrusted(ctx, tenant, actor, &runtimev1.CreateInferenceAccessPolicyRequest{})
			return err
		}},
		{"ListInferenceAccessPolicies", func() error {
			_, err := c.ListInferencePoliciesTrusted(ctx, tenant, actor, &runtimev1.ListInferenceAccessPoliciesRequest{})
			return err
		}},
		{"GetInferenceAccessPolicy", func() error {
			_, err := c.GetInferencePolicyTrusted(ctx, tenant, actor, &runtimev1.GetInferenceAccessPolicyRequest{})
			return err
		}},
		{"UpdateInferenceAccessPolicy", func() error {
			_, err := c.UpdateInferencePolicyTrusted(ctx, tenant, actor, &runtimev1.UpdateInferenceAccessPolicyRequest{})
			return err
		}},
		{"DeleteInferenceAccessPolicy", func() error {
			_, err := c.DeleteInferencePolicyTrusted(ctx, tenant, actor, &runtimev1.DeleteInferenceAccessPolicyRequest{})
			return err
		}},
		{"BindInferenceAccessKeyPolicy", func() error {
			_, err := c.BindInferenceKeyPolicyTrusted(ctx, tenant, actor, &runtimev1.BindInferenceAccessKeyPolicyRequest{})
			return err
		}},
		{"CreateInferenceAccessKey", func() error {
			_, err := c.CreateInferenceKeyTrusted(ctx, tenant, actor, &runtimev1.CreateInferenceAccessKeyRequest{})
			return err
		}},
		{"ListInferenceAccessKeys", func() error {
			_, err := c.ListInferenceKeysTrusted(ctx, tenant, actor, &runtimev1.ListInferenceAccessKeysRequest{})
			return err
		}},
		{"GetInferenceAccessKey", func() error {
			_, err := c.GetInferenceKeyTrusted(ctx, tenant, actor, &runtimev1.GetInferenceAccessKeyRequest{})
			return err
		}},
		{"RevokeInferenceAccessKey", func() error {
			_, err := c.RevokeInferenceKeyTrusted(ctx, tenant, actor, &runtimev1.RevokeInferenceAccessKeyRequest{})
			return err
		}},
		{"RotateInferenceAccessKey", func() error {
			_, err := c.RotateInferenceKeyTrusted(ctx, tenant, actor, &runtimev1.RotateInferenceAccessKeyRequest{})
			return err
		}},
	} {
		require.NoError(t, call.invoke())
		observed := <-seen
		require.Equal(t, "/inference.v1.InferenceAccessKeyService/"+call.method, observed.method)
		require.Equal(t, []string{tenant}, observed.metadata.Get("x-ani-tenant-id"))
		require.Equal(t, []string{actor}, observed.metadata.Get("x-ani-actor"))
		require.Len(t, observed.metadata.Get("x-ani-request-id"), 1)
		require.Empty(t, observed.metadata.Get("authorization"))
		require.Empty(t, observed.metadata.Get("x-api-key"))
		require.Equal(t, uint16(tls.VersionTLS13), observed.tlsVersion)
		require.Equal(t, []string{"ani-governance"}, observed.clientDNS)
		require.Greater(t, observed.remaining, time.Duration(0))
		require.LessOrEqual(t, observed.remaining, 3*time.Second)
	}
	_, err = c.GetInferenceKeyTrusted(ctx, "forged", actor, &runtimev1.GetInferenceAccessKeyRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Empty(t, seen)
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err = c.GetInferenceKeyTrusted(deadline, tenant, actor, &runtimev1.GetInferenceAccessKeyRequest{Id: "wait"})
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
	require.LessOrEqual(t, (<-seen).remaining, 100*time.Millisecond)
	callCtx, cancelCall := context.WithCancel(ctx)
	defer cancelCall()
	finished := make(chan error, 1)
	go func() {
		_, err := c.GetInferenceKeyTrusted(callCtx, tenant, actor, &runtimev1.GetInferenceAccessKeyRequest{Id: "wait"})
		finished <- err
	}()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("key RPC did not start")
	}
	cancelCall()
	select {
	case err := <-finished:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-time.After(time.Second):
		t.Fatal("key RPC ignored cancellation")
	}
}
