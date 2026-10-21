package data

import (
	"context"
	"crypto/x509"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type modelClientProbe struct {
	modelv1.ModelServiceClient
	calls         int
	bundleCalls   int
	bundleRequest *modelv1.ImportModelBundleRequest
	logsCalls     int
	logs          func(context.Context, *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error)
}

func (p *modelClientProbe) GetImportTaskLogs(ctx context.Context, request *modelv1.GetImportTaskLogsRequest, _ ...grpc.CallOption) (*modelv1.GetImportTaskLogsResponse, error) {
	p.logsCalls++
	return p.logs(ctx, request)
}

func (p *modelClientProbe) ImportModelBundle(ctx context.Context, request *modelv1.ImportModelBundleRequest, _ ...grpc.CallOption) (*modelv1.ImportModelBundleResponse, error) {
	p.bundleCalls++
	p.bundleRequest = request
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok || len(md.Get("authorization")) != 0 || md.Get("x-ani-tenant-id")[0] != request.TenantId || md.Get("x-ani-actor")[0] != "governance:user:7" {
		return nil, status.Error(codes.Internal, "identity metadata was not rebuilt")
	}
	return &modelv1.ImportModelBundleResponse{
		Model:   &modelv1.Model{TenantId: request.TenantId, Id: "model-1", Name: request.Model.Name},
		Version: &modelv1.ModelVersion{Id: "version-1", ModelId: "model-1", Version: request.Version.Version},
		Task:    &modelv1.ImportTask{TaskId: "task-1", ModelId: "model-1", ModelVersionId: "version-1"},
	}, nil
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

func TestModelClientImportTaskLogsRebuildsIdentityWithoutMutatingRequest(t *testing.T) {
	const tenant = "11111111-1111-4111-8111-111111111111"
	request := &modelv1.GetImportTaskLogsRequest{TenantId: "body-tenant", TaskId: "task-1"}
	want := &modelv1.GetImportTaskLogsResponse{TaskId: "task-1", Status: "running", Logs: "line 1\n", Available: true}
	probe := &modelClientProbe{logs: func(ctx context.Context, forwarded *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error) {
		if forwarded == request || forwarded.GetTenantId() != tenant || forwarded.GetTaskId() != request.GetTaskId() {
			t.Fatalf("unexpected forwarded logs request: %#v", forwarded)
		}
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok || len(md) != 3 || len(md.Get("x-ani-tenant-id")) != 1 || md.Get("x-ani-tenant-id")[0] != tenant || len(md.Get("x-ani-actor")) != 1 || md.Get("x-ani-actor")[0] != "governance:user:7" || len(md.Get("x-ani-request-id")) != 1 || md.Get("x-ani-request-id")[0] == "" || md.Get("x-ani-request-id")[0] == "forged-request" {
			t.Fatalf("logs identity metadata was not rebuilt: %#v", md)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("logs request has no bounded deadline")
		}
		return want, nil
	}}
	c := &ModelClient{client: probe, timeout: time.Second}
	forged := metadata.Pairs("x-ani-tenant-id", "forged-tenant", "x-ani-actor", "forged-actor", "x-ani-request-id", "forged-request", "authorization", "secret", "x-ani-operator", "forged-operator")
	ctx := metadata.NewIncomingContext(context.Background(), forged)
	ctx = metadata.NewOutgoingContext(ctx, forged)
	reply, err := c.GetImportTaskLogs(ctx, tenant, "governance:user:7", request)
	if err != nil {
		t.Fatal(err)
	}
	if probe.logsCalls != 1 || reply != want || request.TenantId != "body-tenant" || request.TaskId != "task-1" {
		t.Fatalf("unexpected logs result or mutated request: calls=%d request=%#v reply=%#v", probe.logsCalls, request, reply)
	}
}

func TestModelClientImportTaskLogsRejectsInvalidRequestAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tenant  string
		actor   string
		request *modelv1.GetImportTaskLogsRequest
		code    codes.Code
	}{
		{"nil request", "11111111-1111-4111-8111-111111111111", "governance:user:7", nil, codes.InvalidArgument},
		{"missing tenant", "", "governance:user:7", &modelv1.GetImportTaskLogsRequest{TaskId: "task-1"}, codes.Unauthenticated},
		{"invalid actor", "11111111-1111-4111-8111-111111111111", "governance:user:0", &modelv1.GetImportTaskLogsRequest{TaskId: "task-1"}, codes.Unauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &modelClientProbe{logs: func(context.Context, *modelv1.GetImportTaskLogsRequest) (*modelv1.GetImportTaskLogsResponse, error) {
				t.Fatal("invalid logs request reached Model")
				return nil, nil
			}}
			c := &ModelClient{client: probe, timeout: time.Second}
			reply, err := c.GetImportTaskLogs(context.Background(), tc.tenant, tc.actor, tc.request)
			if reply != nil || status.Code(err) != tc.code || probe.logsCalls != 0 {
				t.Fatalf("unexpected rejection: reply=%#v error=%v calls=%d", reply, err, probe.logsCalls)
			}
		})
	}
}

func TestModelClientImportsBundleOnceAndPreservesRuntimeInput(t *testing.T) {
	probe := &modelClientProbe{}
	c := &ModelClient{client: probe, timeout: time.Second}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "secret", "x-ani-tenant-id", "forged"))
	reply, err := c.ImportModelBundle(ctx, "11111111-1111-4111-8111-111111111111", "governance:user:7", &modelv1.ImportModelBundleRequest{
		TenantId: "body-tenant", IdempotencyKey: "bundle-1",
		Model:   &modelv1.ImportModelBundleModelData{Name: "qwen", Capabilities: []string{"text-generation"}},
		Version: &modelv1.ImportModelBundleVersionData{Version: "main", Format: "safetensors", EngineType: "vllm", EngineImage: "registry/vllm:tag", StartupCommand: "vllm serve /models --served-model-name qwen"},
		Source:  &modelv1.ImportModelBundleSourceData{Provider: "modelscope", RepoId: "Qwen/qwen", Revision: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply.GetTask().GetTaskId() != "task-1" || probe.bundleCalls != 1 || probe.bundleRequest.GetTenantId() != "11111111-1111-4111-8111-111111111111" || probe.bundleRequest.GetVersion().GetStartupCommand() != "vllm serve /models --served-model-name qwen" || probe.bundleRequest.GetVersion().GetEngineImage() != "registry/vllm:tag" {
		t.Fatalf("unexpected bundle call: calls=%d request=%#v reply=%#v", probe.bundleCalls, probe.bundleRequest, reply)
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
func TestModelClientCertificateIdentity(t *testing.T) {
	cases := []struct {
		name   string
		change func(*x509.Certificate)
	}{
		{"registered DNS", nil},
		{"old DNS", func(c *x509.Certificate) { c.DNSNames = []string{"ani-governance.ani.internal"} }},
		{"wrong DNS", func(c *x509.Certificate) { c.DNSNames = []string{"other-service"} }},
		{"multiple DNS", func(c *x509.Certificate) { c.DNSNames = append(c.DNSNames, "other-service") }},
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
			if closeModel != nil {
				t.Cleanup(closeModel)
			}
			if tc.change == nil {
				if modelErr != nil || model == nil {
					t.Fatalf("registered Governance certificate rejected: %v", modelErr)
				}
			} else if modelErr == nil || model != nil || closeModel != nil {
				t.Fatalf("invalid Governance certificate retained a client or cleanup: %v", modelErr)
			}
		})
	}
}

type modelTLSProbe struct {
	modelv1.UnimplementedModelServiceServer
	calls atomic.Int32
}

func (p *modelTLSProbe) GetModel(context.Context, *modelv1.GetModelRequest) (*modelv1.Model, error) {
	p.calls.Add(1)
	return &modelv1.Model{}, nil
}

func TestModelClientLiteralServerIdentity(t *testing.T) {
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
			endpoint := &modelTLSProbe{}
			server := grpc.NewServer(grpc.Creds(credentials.NewTLS(f.server)))
			modelv1.RegisterModelServiceServer(server, endpoint)
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
			_, modelErr := model.GetModel(context.Background(), "11111111-1111-4111-8111-111111111111", "governance:user:7", &modelv1.GetModelRequest{})
			if tc.valid {
				if modelErr != nil || endpoint.calls.Load() != 1 {
					t.Fatalf("registered TLS endpoint was not reached: Model=%v calls=%d", modelErr, endpoint.calls.Load())
				}
			} else if modelErr == nil || endpoint.calls.Load() != 0 {
				t.Fatalf("unregistered TLS endpoint reached handler: Model=%v calls=%d", modelErr, endpoint.calls.Load())
			}
		})
	}
}
