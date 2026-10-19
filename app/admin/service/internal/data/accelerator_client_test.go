package data

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type acceleratorIdentityServer struct {
	acc.UnimplementedAcceleratorCatalogServiceServer
	acc.UnimplementedAcceleratorUsageServiceServer
	t        *testing.T
	received chan metadata.MD
}

func (s *acceleratorIdentityServer) ListProfiles(ctx context.Context, r *acc.ListProfilesRequest) (*acc.ListProfilesResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.received <- md
	return &acc.ListProfilesResponse{NextToken: r.Page.Token}, nil
}
func (s *acceleratorIdentityServer) SyncGpuUsage(ctx context.Context, r *acc.SyncGpuUsageRequest) (*acc.SyncGpuUsageResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.received <- md
	return &acc.SyncGpuUsageResponse{Ref: r.Projection.Ref, AppliedRevision: r.Projection.Revision}, nil
}

func TestAcceleratorMTLSIdentityAndDelegation(t *testing.T) {
	dir := t.TempDir()
	caPub, caKey, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, e := x509.CreateCertificate(rand.Reader, ca, ca, caPub, caKey)
	if e != nil {
		t.Fatal(e)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	caPath := filepath.Join(dir, "ca.pem")
	if e = os.WriteFile(caPath, caPEM, 0600); e != nil {
		t.Fatal(e)
	}
	issue := func(name, uri string, server bool) (string, string, tls.Certificate) {
		t.Helper()
		pub, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
		if e != nil {
			t.Fatal(e)
		}
		leaf := &x509.Certificate{SerialNumber: serial, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		if uri != "" {
			u, e := url.Parse(uri)
			if e != nil {
				t.Fatal(e)
			}
			leaf.URIs = []*url.URL{u}
		}
		if server {
			leaf.DNSNames = []string{"accelerator.test"}
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		der, e := x509.CreateCertificate(rand.Reader, leaf, ca, pub, caKey)
		if e != nil {
			t.Fatal(e)
		}
		pk, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			t.Fatal(e)
		}
		cp, kp := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+"-key.pem")
		if e = os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0600); e != nil {
			t.Fatal(e)
		}
		pair, e := tls.LoadX509KeyPair(cp, kp)
		if e != nil {
			t.Fatal(e)
		}
		return cp, kp, pair
	}
	_, _, serverCert := issue("server", "", true)
	cp, kp, _ := issue("governance", GovernanceAcceleratorURI, false)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(caPEM)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert})))
	impl := &acceleratorIdentityServer{t: t, received: make(chan metadata.MD, 4)}
	acc.RegisterAcceleratorCatalogServiceServer(srv, impl)
	acc.RegisterAcceleratorUsageServiceServer(srv, impl)
	go srv.Serve(listener)
	t.Cleanup(srv.Stop)
	cfg := AcceleratorClientConfig{Address: listener.Addr().String(), ServerName: "accelerator.test", CAFile: caPath, CertFile: cp, KeyFile: kp, Timeout: 2 * time.Second}
	client, closeClient, e := NewAcceleratorClient(cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer closeClient()
	tenant := uuid.NewString()
	tc := &acc.TenantContext{RequestId: uuid.NewString(), TenantId: tenant, Actor: &acc.Actor{Type: "user", Id: "42"}}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer must-not-forward", "x-ani-action", "ObserveRelease", "x-ani-tenant-id", uuid.NewString(), "x-ani-actor-id", "evil"))
	reply, e := client.Catalog.ListProfiles(ctx, &acc.ListProfilesRequest{Context: tc, Page: &acc.Page{Size: 7, Token: "opaque-token"}})
	if e != nil {
		t.Fatal(e)
	}
	if reply.NextToken != "opaque-token" {
		t.Fatal("cursor was rewritten")
	}
	md := <-impl.received
	for key, want := range map[string]string{"x-ani-action": "ListProfiles", "x-ani-tenant-id": tenant, "x-ani-actor-type": "user", "x-ani-actor-id": "42"} {
		v := md.Get(key)
		if len(v) != 1 || v[0] != want {
			t.Fatalf("metadata %s=%v", key, v)
		}
	}
	if len(md.Get("authorization")) != 0 {
		t.Fatal("bearer credential forwarded")
	}
	ref := &acc.GpuUsageRef{TenantId: tenant, OwnerService: "ani-resource-service", ResourceId: uuid.NewString(), CreateOperationId: uuid.NewString()}
	ack, e := client.SyncGpuUsage(ctx, &acc.SyncGpuUsageRequest{RequestId: uuid.NewString(), Projection: &acc.GpuUsageProjection{Ref: ref, Revision: 1}})
	if e != nil {
		t.Fatal(e)
	}
	if !proto.Equal(ack.Ref, ref) {
		t.Fatal("sync ref changed")
	}
	md = <-impl.received
	for _, key := range []string{"authorization", "x-ani-action", "x-ani-tenant-id", "x-ani-actor-type", "x-ani-actor-id"} {
		if len(md.Get(key)) != 0 {
			t.Fatalf("sync received delegated %s", key)
		}
	}
	for _, uri := range []string{"", GovernanceAcceleratorURI + "-other", "spiffe://ani.internal/service/ani-inference"} {
		badCert, badKey, _ := issue(uuid.NewString(), uri, false)
		bad := cfg
		bad.CertFile = badCert
		bad.KeyFile = badKey
		if c, done, e := NewAcceleratorClient(bad); e == nil {
			done()
			t.Fatalf("accepted wrong client URI %q: %v", uri, c)
		}
	}
	wrongServer := cfg
	wrongServer.ServerName = "other.test"
	bad, done, e := NewAcceleratorClient(wrongServer)
	if e != nil {
		t.Fatal(e)
	}
	defer done()
	if _, e = bad.Catalog.ListProfiles(context.Background(), &acc.ListProfilesRequest{Context: tc}); e == nil {
		t.Fatal("accepted wrong server DNS identity")
	}
	missingCert := cfg
	missingCert.CertFile = filepath.Join(dir, "missing-client.pem")
	if _, _, e = NewAcceleratorClient(missingCert); e == nil {
		t.Fatal("accepted missing client certificate")
	}
	untrustedCA, _, _ := issue("untrusted-root", "", false)
	wrongCA := cfg
	wrongCA.CAFile = untrustedCA
	untrusted, doneUntrusted, e := NewAcceleratorClient(wrongCA)
	requireNoAcceleratorError(t, e)
	defer doneUntrusted()
	if _, e = untrusted.Catalog.ListProfiles(context.Background(), &acc.ListProfilesRequest{Context: tc}); e == nil {
		t.Fatal("accepted server outside configured CA")
	}
}

func requireNoAcceleratorError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestAcceleratorDelegationRejectsInvalidTenantAndOwnerRPC(t *testing.T) {
	for _, tenant := range []string{"", "0", "*", uuid.Nil.String()} {
		_, e := acceleratorMetadata(acc.AcceleratorCatalogService_ListProfiles_FullMethodName, &acc.ListProfilesRequest{Context: &acc.TenantContext{RequestId: "id", TenantId: tenant, Actor: &acc.Actor{Type: "user", Id: "1"}}})
		if e == nil {
			t.Fatalf("accepted tenant %q", tenant)
		}
	}
	if _, e := acceleratorMetadata(acc.AcceleratorUsageService_ObserveRelease_FullMethodName, &acc.ObserveReleaseRequest{RequestId: "id"}); e == nil {
		t.Fatal("Governance proxy admitted owner ObserveRelease")
	}
	if _, e := acceleratorMetadata(acc.AcceleratorAdminService_ListClusters_FullMethodName, &acc.ListClustersRequest{Context: &acc.AdminRead{RequestId: "id", Actor: &acc.Actor{Type: "api_key", Id: "1"}}}); e == nil {
		t.Fatal("admitted access-key actor")
	}
}
