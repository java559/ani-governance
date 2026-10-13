package data

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	pb "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

type InstanceSessionClient struct{ client pb.SessionServiceClient }

func NewInstanceSessionClientFromEnv() (*InstanceSessionClient, func(), error) {
	addr := os.Getenv("ANI_INSTANCE_SG_ADDR")
	if addr == "" {
		return nil, func() {}, nil
	}
	name := os.Getenv("ANI_INSTANCE_SG_SERVER_NAME")
	if name == "" || strings.ContainsAny(name, "*:/") {
		return nil, nil, fmt.Errorf("instance SG exact server name required")
	}
	ca, e := os.ReadFile(os.Getenv("ANI_INSTANCE_SG_CA_FILE"))
	if e != nil {
		return nil, nil, fmt.Errorf("instance SG CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, nil, fmt.Errorf("instance SG CA invalid")
	}
	cert, e := tls.LoadX509KeyPair(os.Getenv("ANI_INSTANCE_SG_CERT_FILE"), os.Getenv("ANI_INSTANCE_SG_KEY_FILE"))
	if e != nil {
		return nil, nil, fmt.Errorf("instance SG Governance identity unavailable")
	}
	leaf, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil || len(leaf.URIs) != 1 || leaf.URIs[0].String() != "spiffe://ani.internal/service/ani-governance" {
		return nil, nil, fmt.Errorf("instance SG requires the exact Governance URI identity")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: name, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 {
			return fmt.Errorf("SG peer unverified")
		}
		for _, v := range cs.PeerCertificates[0].DNSNames {
			if v == name && !strings.Contains(v, "*") {
				return nil
			}
		}
		return fmt.Errorf("SG peer exact DNS mismatch")
	}}
	conn, e := grpc.NewClient(addr, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if e != nil {
		return nil, nil, e
	}
	return &InstanceSessionClient{pb.NewSessionServiceClient(conn)}, func() { _ = conn.Close() }, nil
}
func (c *InstanceSessionClient) CreateSession(ctx context.Context, r *pb.CreateSessionRequest) (*pb.CreateSessionResponse, error) {
	call, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.MD{}), 5*time.Second)
	defer cancel()
	return c.client.CreateSession(call, r)
}
