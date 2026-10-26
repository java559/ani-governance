package server

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	quotapb "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func TestModelDevQuotaReleaseUsesOnlyOriginalUniqueMTLSSAN(t *testing.T) {
	dir, roots := quotaTLSFixture(t)
	server, err := NewQuotaInternalServer(QuotaInternalServerConfig{Enabled: true, Address: "127.0.0.1:0", CAFile: filepath.Join(dir, "ca.pem"), CertFile: filepath.Join(dir, "server.pem"), KeyFile: filepath.Join(dir, "server.key"), CertOwnerMap: map[string]string{commandcontract.ModelDevOwner: commandcontract.ModelDevOwner}}, nil)
	require.NoError(t, err)
	started := quotaStartAsync(server)
	t.Cleanup(func() {
		require.NoError(t, server.Stop(context.Background()))
		require.NoError(t, quotaStartResult(t, started))
	})
	call := func(certificate string) error {
		pair, err := tls.LoadX509KeyPair(filepath.Join(dir, certificate+".pem"), filepath.Join(dir, certificate+".key"))
		require.NoError(t, err)
		connection, err := grpc.NewClient(server.listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "ani-governance", Certificates: []tls.Certificate{pair}})))
		require.NoError(t, err)
		defer connection.Close()
		create := uuid.NewString()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// Deliberately invalid close scope reaches the production handler only
		// for the authenticated sole ModelDev identity, before any ledger call.
		_, err = quotapb.NewQuotaReleaseServiceClient(connection).ReportResourceReleased(ctx, &quotapb.ReportResourceReleasedRequest{ReleaseEventId: create, CreateOperationId: create, ResourceId: uuid.NewString(), Reason: quotapb.ReleaseReason_RESOURCE_RELEASED})
		return err
	}
	require.Equal(t, codes.InvalidArgument, status.Code(call("modeldev")))
	for _, name := range []string{"modeldev-mixed", "modeldev-alias", "resource", "unknown"} {
		require.Equal(t, codes.Unauthenticated, status.Code(call(name)), name)
	}
}
