package data

import (
	"context"
	"encoding/base64"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pb "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const tokenTestTenant = "11111111-1111-4111-8111-111111111111"
const tokenTestSandbox = "22222222-2222-4222-8222-222222222222"
const tokenTestID = "33333333-3333-4333-8333-333333333333"

type tokenVerifierProbe struct {
	pb.UnimplementedSandboxServiceServer
	calls int
	actor string
}

func (p *tokenVerifierProbe) ValidateToken(ctx context.Context, r *pb.ValidateTokenRequest) (*pb.ValidateTokenResponse, error) {
	p.calls++
	md, _ := metadata.FromIncomingContext(ctx)
	p.actor = md.Get("x-ani-actor")[0]
	return &pb.ValidateTokenResponse{TenantId: r.TenantId, SandboxId: r.SandboxId, TokenId: tokenTestID, GovTenantId: 7, IssuerId: 9, Scopes: []string{r.RequiredScope}, ExpiresAt: time.Now().Add(time.Minute).Unix()}, nil
}
func TestSandboxTokenClientUnavailableIsNotInvalidCredential(t *testing.T) {
	var c *InstanceClient
	_, err := c.VerifySandboxToken(context.Background(), "ani.sbx.x.y", tokenTestSandbox, "files")
	require.Equal(t, codes.Unavailable, status.Code(err))
}
func TestSandboxTokenClientUsesVerifiedResourceResponse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	probe := new(tokenVerifierProbe)
	pb.RegisterSandboxServiceServer(srv, probe)
	go srv.Serve(listener)
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///"+listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	c := &InstanceClient{sandbox: pb.NewSandboxServiceClient(conn)}
	payload := `{"tid":"` + tokenTestTenant + `","sid":"` + tokenTestSandbox + `","jti":"` + tokenTestID + `"}`
	token := auth.SandboxTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
	subject, err := c.VerifySandboxToken(context.Background(), token, tokenTestSandbox, "files")
	require.NoError(t, err)
	require.Equal(t, auth.SubjectSandbox, subject.Type)
	require.Equal(t, "governance:sandbox-token:"+tokenTestID, probe.actor)
	require.Equal(t, tokenTestTenant, subject.ResourceTenantID)
	_, err = c.VerifySandboxToken(context.Background(), token, "44444444-4444-4444-8444-444444444444", "files")
	require.Error(t, err)
	require.Equal(t, 1, probe.calls)
}
