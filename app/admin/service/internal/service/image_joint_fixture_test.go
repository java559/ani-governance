package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	imagev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/image/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Protocol fixture only. Resource's domain, real PG and provider tests are
// separate gates; this proves actual HTTP/authorization/mTLS/DTO boundaries.
type imageJointPeer struct {
	imagev1.UnimplementedTenantImageServiceServer
	mu        sync.Mutex
	badTenant bool
	calls     int
	actor     string
}

func (p *imageJointPeer) space(tenant string) *imagev1.ImageSpace {
	p.mu.Lock()
	bad := p.badTenant
	p.mu.Unlock()
	if bad {
		tenant = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	}
	return &imagev1.ImageSpace{TenantId: tenant, SpaceId: "22222222-2222-4222-8222-222222222222", ProjectName: "t-example", RegistryAuthority: "registry.example.test", State: "available", Version: 1, PullCredentialGeneration: 1, CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}
}
func (p *imageJointPeer) credential(tenant, state string) *imagev1.PublisherCredential {
	return &imagev1.PublisherCredential{TenantId: tenant, SpaceId: p.space(tenant).SpaceId, Username: "provider-returned-user", Generation: 1, Version: 1, State: state, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)), UpdatedAt: timestamppb.Now()}
}
func (p *imageJointPeer) registration(tenant string, scope imagev1.ImageScope, id string) *imagev1.ImageRegistration {
	if id == "" {
		id = "img_" + strings.Repeat("a", 32)
	}
	if scope == 2 {
		tenant = ""
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	return &imagev1.ImageRegistration{TenantId: tenant, Scope: scope, ImageId: id, SpaceId: p.space(tenant).SpaceId, DisplayName: "Example", Repository: "app", SourceReference: "registry.example.test/t-example/app:v1", Digest: digest, ResolvedReference: "registry.example.test/t-example/app@" + digest, Platforms: []*imagev1.ImagePlatform{{Os: "linux", Architecture: "amd64"}}, Purposes: []imagev1.ImagePurpose{1}, Version: 1, CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now()}
}
func (p *imageJointPeer) EnsureImageSpace(_ context.Context, r *imagev1.EnsureImageSpaceRequest) (*imagev1.EnsureImageSpaceResponse, error) {
	return &imagev1.EnsureImageSpaceResponse{Space: p.space(r.TenantId)}, nil
}
func (p *imageJointPeer) GetImageSpace(_ context.Context, r *imagev1.GetImageSpaceRequest) (*imagev1.GetImageSpaceResponse, error) {
	return &imagev1.GetImageSpaceResponse{Space: p.space(r.TenantId)}, nil
}
func (p *imageJointPeer) GetPublisherCredential(_ context.Context, r *imagev1.GetPublisherCredentialRequest) (*imagev1.GetPublisherCredentialResponse, error) {
	return &imagev1.GetPublisherCredentialResponse{Credential: p.credential(r.TenantId, "active")}, nil
}
func (p *imageJointPeer) IssuePublisherCredential(_ context.Context, r *imagev1.IssuePublisherCredentialRequest) (*imagev1.IssuePublisherCredentialResponse, error) {
	return &imagev1.IssuePublisherCredentialResponse{Credential: p.credential(r.TenantId, "active"), Secret: "joint-delivery-secret-sentinel", ReplayUntil: timestamppb.New(time.Now().Add(10 * time.Minute))}, nil
}
func (p *imageJointPeer) ResetPublisherCredential(_ context.Context, r *imagev1.ResetPublisherCredentialRequest) (*imagev1.ResetPublisherCredentialResponse, error) {
	return &imagev1.ResetPublisherCredentialResponse{Credential: p.credential(r.TenantId, "active"), Secret: "joint-delivery-secret-sentinel", ReplayUntil: timestamppb.New(time.Now().Add(10 * time.Minute))}, nil
}
func (p *imageJointPeer) DisablePublisherCredential(_ context.Context, r *imagev1.DisablePublisherCredentialRequest) (*imagev1.DisablePublisherCredentialResponse, error) {
	return &imagev1.DisablePublisherCredentialResponse{Credential: p.credential(r.TenantId, "disabled")}, nil
}
func (p *imageJointPeer) RegisterImage(_ context.Context, r *imagev1.RegisterImageRequest) (*imagev1.RegisterImageResponse, error) {
	return &imagev1.RegisterImageResponse{Image: p.registration(r.TenantId, 1, "")}, nil
}
func (p *imageJointPeer) GetImage(_ context.Context, r *imagev1.GetImageRequest) (*imagev1.GetImageResponse, error) {
	return &imagev1.GetImageResponse{Image: p.registration(r.TenantId, r.Scope, r.ImageId)}, nil
}
func (p *imageJointPeer) ListImages(_ context.Context, r *imagev1.ListImagesRequest) (*imagev1.ListImagesResponse, error) {
	if r.GetSearch() == "empty-total" {
		return &imagev1.ListImagesResponse{}, nil
	}
	return &imagev1.ListImagesResponse{Total: 37, Items: []*imagev1.ImageRegistration{p.registration(r.TenantId, r.Scope, "")}}, nil
}
func (p *imageJointPeer) UpdateImage(_ context.Context, r *imagev1.UpdateImageRequest) (*imagev1.UpdateImageResponse, error) {
	return &imagev1.UpdateImageResponse{Image: p.registration(r.TenantId, 1, r.ImageId)}, nil
}
func (p *imageJointPeer) UnregisterImage(_ context.Context, r *imagev1.UnregisterImageRequest) (*imagev1.UnregisterImageResponse, error) {
	v := p.registration(r.TenantId, 1, r.ImageId)
	v.UnregisteredAt = timestamppb.Now()
	return &imagev1.UnregisterImageResponse{Image: v}, nil
}

func newImageJointPeer(t *testing.T, serverName string) (*data.ImageClient, *imageJointPeer) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Image joint test CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	raw, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "ca.pem")
	if err = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	issue := func(name string, usage x509.ExtKeyUsage, serial int64) (tls.Certificate, string, string) {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		c := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		der, err := x509.CreateCertificate(rand.Reader, c, ca, &k.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		pk, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		cp, kp := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key")
		if err = os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: pk}), 0600); err != nil {
			t.Fatal(err)
		}
		cert, err := tls.LoadX509KeyPair(cp, kp)
		if err != nil {
			t.Fatal(err)
		}
		return cert, cp, kp
	}
	cert, _, _ := issue(serverName, x509.ExtKeyUsageServerAuth, 2)
	_, cp, kp := issue("ani-governance", x509.ExtKeyUsageClientAuth, 3)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	peer := &imageJointPeer{}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 || len(cs.PeerCertificates[0].DNSNames) != 1 || cs.PeerCertificates[0].DNSNames[0] != "ani-governance" {
			return status.Error(codes.Unauthenticated, "wrong workload")
		}
		return nil
	}})), grpc.UnaryInterceptor(func(ctx context.Context, r any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		for _, key := range []string{"x-ani-tenant-id", "x-ani-actor", "x-ani-request-id"} {
			if len(md.Get(key)) != 1 {
				t.Error("identity is not single valued")
				return nil, status.Error(codes.Unauthenticated, "bad identity")
			}
		}
		if len(md.Get("authorization")) != 0 || len(md.Get("x-ani-operator")) != 0 {
			t.Error("inbound metadata leaked to Resource")
		}
		m := r.(proto.Message).ProtoReflect()
		field := m.Descriptor().Fields().ByName("tenant_id")
		if field == nil || m.Get(field).String() != md.Get("x-ani-tenant-id")[0] {
			t.Error("tenant assertion mismatch")
		}
		peer.mu.Lock()
		peer.calls++
		peer.actor = md.Get("x-ani-actor")[0]
		peer.mu.Unlock()
		return next(ctx, r)
	}))
	imagev1.RegisterTenantImageServiceServer(srv, peer)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)
	client, closeClient, err := data.NewImageClient(data.ImageClientConfig{Address: listener.Addr().String(), CAFile: caPath, CertFile: cp, KeyFile: kp, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeClient)
	return client, peer
}
