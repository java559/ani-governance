package data

import (
	"context"
	"crypto/x509"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type modelClientProbe struct {
	modelv1.ModelServiceClient
	calls int
}

func (p *modelClientProbe) CreateModel(ctx context.Context, request *modelv1.CreateModelRequest, _ ...grpc.CallOption) (*modelv1.Model, error) {
	p.calls++
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok || md.Get("x-ani-tenant-id")[0] != request.TenantId || md.Get("x-ani-actor")[0] != "governance:user:7" || len(md.Get("authorization")) != 0 {
		return nil, status.Error(codes.Internal, "identity metadata was not rebuilt")
	}
	if request.TenantId != "11111111-1111-4111-8111-111111111111" {
		return nil, status.Error(codes.Internal, "request tenant was not replaced")
	}
	return &modelv1.Model{TenantId: request.TenantId, Id: "model-1", Name: request.Name}, nil
}

func TestModelClientRebuildsTrustedIdentity(t *testing.T) {
	c := &ModelClient{client: &modelClientProbe{}, timeout: time.Second}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"x-ani-tenant-id", "forged", "x-ani-actor", "forged", "authorization", "secret",
	))
	reply, err := c.CreateModel(ctx, "11111111-1111-4111-8111-111111111111", "governance:user:7", &modelv1.CreateModelRequest{TenantId: "body-tenant", Name: "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	if reply == nil || reply.TenantId != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("unexpected reply: %#v", reply)
	}
	if c.client.(*modelClientProbe).calls != 1 {
		t.Fatal("model call was not made exactly once")
	}
}

func TestModelClientRejectsInvalidTrustedIdentity(t *testing.T) {
	c := &ModelClient{client: &modelClientProbe{}, timeout: time.Second}
	for _, tc := range []struct{ tenant, actor string }{
		{"", "governance:user:7"},
		{"11111111-1111-4111-8111-111111111111", "governance:user:0"},
		{"11111111-1111-4111-8111-111111111111", "governance:machine:7"},
	} {
		if _, err := c.CreateModel(context.Background(), tc.tenant, tc.actor, &modelv1.CreateModelRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("identity (%q,%q) accepted: %v", tc.tenant, tc.actor, err)
		}
	}
	if _, _, err := NewModelClient(ModelClientConfig{}); err == nil {
		t.Fatal("insecure model client configuration accepted")
	}
}

// Reuse the package's temporary certificates to exercise the real constructors;
// these cases never connect to a database or a deployed downstream service.
func TestModelInferenceClientCertificateIdentity(t *testing.T) {
	cases := []struct {
		name   string
		change func(*x509.Certificate)
	}{
		{"registered DNS", nil},
		{"old DNS", func(c *x509.Certificate) { c.DNSNames = []string{"ani-governance.ani.internal"} }},
		{"wrong DNS", func(c *x509.Certificate) { c.DNSNames = []string{"ani-inference"} }},
		{"multiple DNS", func(c *x509.Certificate) { c.DNSNames = append(c.DNSNames, "ani-inference") }},
		{"duplicate DNS", func(c *x509.Certificate) { c.DNSNames = append(c.DNSNames, "ani-governance") }},
		{"wildcard DNS", func(c *x509.Certificate) { c.DNSNames = []string{"*.ani.internal"} }},
		{"CN only", func(c *x509.Certificate) { c.DNSNames = nil; c.Subject.CommonName = "ani-governance" }},
		{"mixed URI", func(c *x509.Certificate) {
			c.URIs = []*url.URL{{Scheme: "spiffe", Host: "ani.internal", Path: "/service/ani-governance"}}
		}},
		{"mixed IP", func(c *x509.Certificate) { c.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")} }},
		{"mixed email", func(c *x509.Certificate) { c.EmailAddresses = []string{"gov@ani.internal"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := resourceClientTestCertificates(t, tc.change, []string{"downstream.ani.internal"})
			model, closeModel, modelErr := NewModelClient(ModelClientConfig{Address: "127.0.0.1:1", ServerName: "downstream.ani.internal", CAFile: f.caFile, CertFile: f.certFile, KeyFile: f.keyFile, Timeout: time.Second})
			inference, closeInference, inferenceErr := NewInferenceClient(InferenceClientConfig{Address: "127.0.0.1:1", ServerName: "downstream.ani.internal", CAFile: f.caFile, CertFile: f.certFile, KeyFile: f.keyFile, Timeout: time.Second})
			if closeModel != nil {
				t.Cleanup(closeModel)
			}
			if closeInference != nil {
				t.Cleanup(closeInference)
			}
			if tc.change == nil {
				if modelErr != nil || inferenceErr != nil || model == nil || inference == nil {
					t.Fatalf("registered Governance certificate rejected: Model=%v Inference=%v", modelErr, inferenceErr)
				}
			} else if modelErr == nil || inferenceErr == nil || model != nil || inference != nil || closeModel != nil || closeInference != nil {
				t.Fatalf("invalid Governance certificate retained a client or cleanup: Model=%v Inference=%v", modelErr, inferenceErr)
			}
		})
	}
}

type modelInferenceTLSProbe struct {
	modelv1.UnimplementedModelServiceServer
	inferencev1.UnimplementedInferenceServiceManagerServer
	calls atomic.Int32
}

func (p *modelInferenceTLSProbe) GetModel(context.Context, *modelv1.GetModelRequest) (*modelv1.Model, error) {
	p.calls.Add(1)
	return &modelv1.Model{}, nil
}

func (p *modelInferenceTLSProbe) GetInferenceService(context.Context, *inferencev1.GetInferenceServiceRequest) (*inferencev1.InferenceService, error) {
	p.calls.Add(1)
	return &inferencev1.InferenceService{}, nil
}

func TestModelInferenceClientLiteralServerIdentity(t *testing.T) {
	cases := []struct {
		name  string
		dns   []string
		valid bool
	}{
		{"registered DNS", []string{"downstream.ani.internal"}, true},
		{"registered DNS with other literal DNS", []string{"downstream.ani.internal", "other.ani.internal"}, true},
		{"wrong DNS", []string{"other.ani.internal"}, false},
		{"wildcard DNS", []string{"*.ani.internal"}, false},
		{"registered DNS with wildcard", []string{"downstream.ani.internal", "*.ani.internal"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := resourceClientTestCertificates(t, nil, tc.dns)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			endpoint := &modelInferenceTLSProbe{}
			server := grpc.NewServer(grpc.Creds(credentials.NewTLS(f.server)))
			modelv1.RegisterModelServiceServer(server, endpoint)
			inferencev1.RegisterInferenceServiceManagerServer(server, endpoint)
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() {
				server.Stop()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("test downstream server shutdown: %v", err)
					}
				case <-time.After(time.Second):
					t.Error("test downstream server did not stop")
				}
			})
			model, closeModel, err := NewModelClient(ModelClientConfig{Address: listener.Addr().String(), ServerName: "downstream.ani.internal", CAFile: f.caFile, CertFile: f.certFile, KeyFile: f.keyFile, Timeout: 250 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(closeModel)
			inference, closeInference, err := NewInferenceClient(InferenceClientConfig{Address: listener.Addr().String(), ServerName: "downstream.ani.internal", CAFile: f.caFile, CertFile: f.certFile, KeyFile: f.keyFile, Timeout: 250 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(closeInference)
			_, modelErr := model.GetModel(context.Background(), "11111111-1111-4111-8111-111111111111", "governance:user:7", &modelv1.GetModelRequest{})
			_, inferenceErr := inference.GetTrusted(context.Background(), "11111111-1111-4111-8111-111111111111", "governance:user:7", &inferencev1.GetInferenceServiceRequest{})
			if tc.valid {
				if modelErr != nil || inferenceErr != nil || endpoint.calls.Load() != 2 {
					t.Fatalf("registered TLS endpoint was not reached: Model=%v Inference=%v calls=%d", modelErr, inferenceErr, endpoint.calls.Load())
				}
			} else if modelErr == nil || inferenceErr == nil || endpoint.calls.Load() != 0 {
				t.Fatalf("unregistered TLS endpoint reached handler: Model=%v Inference=%v calls=%d", modelErr, inferenceErr, endpoint.calls.Load())
			}
		})
	}
}
