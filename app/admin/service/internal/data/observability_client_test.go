package data

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	pb "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/monitoring/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type observabilityTestEndpoint struct {
	pb.UnimplementedMonitoringServiceServer
	seen     chan metadata.MD
	started  chan struct{}
	canceled chan error
}

func (s *observabilityTestEndpoint) QueryLatest(ctx context.Context, _ *pb.QueryLatestRequest) (*pb.QueryLatestResponse, error) {
	p, _ := peer.FromContext(ctx)
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || tlsInfo.State.Version < tls.VersionTLS13 {
		return nil, status.Error(codes.Unauthenticated, "old transport")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	s.seen <- md
	if s.started != nil {
		s.started <- struct{}{}
		<-ctx.Done()
		s.canceled <- ctx.Err()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	return &pb.QueryLatestResponse{}, nil
}
func observabilityTestClient(t *testing.T, f resourceClientCertificates, s *observabilityTestEndpoint) *ObservabilityClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(f.server)))
	pb.RegisterMonitoringServiceServer(server, s)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("test server did not stop")
		}
	})
	for key, value := range map[string]string{"ANI_OBSERVABILITY_GRPC_ADDR": listener.Addr().String(), "ANI_OBSERVABILITY_CA_FILE": f.caFile, "ANI_OBSERVABILITY_CLIENT_CERT_FILE": f.certFile, "ANI_OBSERVABILITY_CLIENT_KEY_FILE": f.keyFile} {
		t.Setenv(key, value)
	}
	client, close, err := NewObservabilityClientFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(close)
	return client
}
func TestObservabilityClientTrustMetadataAndCancellation(t *testing.T) {
	f := resourceClientTestCertificates(t, nil, []string{"ani-observability-service"})
	endpoint := &observabilityTestEndpoint{seen: make(chan metadata.MD, 1)}
	client := observabilityTestClient(t, f, endpoint)
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-ani-tenant-id", "forged", "x-ani-tenant-id", "duplicate", "x-ani-actor", "forged", "x-ani-request-id", "forged"))
	_, err := client.QueryLatest(ctx, "11111111-1111-4111-8111-111111111111", "governance:user:9", &pb.QueryLatestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	md := <-endpoint.seen
	for key, want := range map[string]string{"x-ani-tenant-id": "11111111-1111-4111-8111-111111111111", "x-ani-actor": "governance:user:9"} {
		if values := md.Get(key); len(values) != 1 || values[0] != want {
			t.Fatalf("%s %+v", key, values)
		}
	}
	ids := md.Get("x-ani-request-id")
	if len(ids) != 1 {
		t.Fatal(ids)
	}
	if id, e := uuid.Parse(ids[0]); e != nil || id == uuid.Nil || id.String() != ids[0] {
		t.Fatal(ids)
	}
	endpoint.started, endpoint.canceled = make(chan struct{}, 1), make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.QueryLatest(ctx, "11111111-1111-4111-8111-111111111111", "governance:user:9", &pb.QueryLatestRequest{})
		done <- err
	}()
	select {
	case <-endpoint.started:
		cancel()
	case <-ctx.Done():
		t.Fatal("request did not reach endpoint")
	}
	select {
	case err := <-done:
		if status.Code(err) != codes.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("client cancellation lost")
	}
	select {
	case err := <-endpoint.canceled:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server cancellation lost")
	}
}
func TestObservabilityClientRejectsWrongServerAndOldTLS(t *testing.T) {
	for _, mode := range []string{"wrong SAN", "wildcard SAN", "wrong CA", "TLS1.2"} {
		t.Run(mode, func(t *testing.T) {
			names := []string{"ani-observability-service"}
			if mode == "wrong SAN" {
				names = []string{"ani-network-service"}
			}
			if mode == "wildcard SAN" {
				names = append(names, "*.ani.internal")
			}
			f := resourceClientTestCertificates(t, nil, names)
			if mode == "wrong CA" {
				f.caFile = resourceClientTestCertificates(t, nil, names).caFile
			}
			if mode == "TLS1.2" {
				f.server.MinVersion = tls.VersionTLS12
				f.server.MaxVersion = tls.VersionTLS12
			}
			s := &observabilityTestEndpoint{seen: make(chan metadata.MD, 1)}
			client := observabilityTestClient(t, f, s)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := client.QueryLatest(ctx, "11111111-1111-4111-8111-111111111111", "governance:user:9", &pb.QueryLatestRequest{})
			if err == nil || len(s.seen) != 0 {
				t.Fatalf("untrusted transport accepted: %v", err)
			}
		})
	}
}
func TestObservabilityClientRejectsAmbiguousClientAndPartialConfig(t *testing.T) {
	f := resourceClientTestCertificates(t, func(c *x509.Certificate) { c.DNSNames = append(c.DNSNames, "other") }, []string{"ani-observability-service"})
	if _, err := observabilityClientTLS(f.caFile, f.certFile, f.keyFile); err == nil {
		t.Fatal("ambiguous client accepted")
	}
	for _, name := range []string{"ANI_OBSERVABILITY_GRPC_ADDR", "ANI_OBSERVABILITY_CA_FILE", "ANI_OBSERVABILITY_CLIENT_CERT_FILE", "ANI_OBSERVABILITY_CLIENT_KEY_FILE"} {
		t.Setenv(name, "")
	}
	t.Setenv("ANI_OBSERVABILITY_CA_FILE", f.caFile)
	_, _, err := NewObservabilityClientFromEnv()
	if err == nil || !strings.Contains(err.Error(), "address") {
		t.Fatal("partial config accepted")
	}
}
