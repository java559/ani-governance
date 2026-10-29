package server

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	quotapb "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

func TestInferenceQuotaRegistrationKeepsTheOriginalBusinessOwner(t *testing.T) {
	t.Setenv("ANI_QUOTA_ENABLED", "true")
	t.Setenv("ANI_QUOTA_INTERNAL_ADDR", "127.0.0.1:9443")
	t.Setenv("ANI_QUOTA_CA_FILE", "ca.pem")
	t.Setenv("ANI_QUOTA_CERT_FILE", "server.pem")
	t.Setenv("ANI_QUOTA_KEY_FILE", "server.key")
	t.Setenv("ANI_QUOTA_RESOURCE_DNS_SAN", "ani-resource-service")
	t.Setenv("ANI_QUOTA_MODELDEV_ENABLED", "true")
	t.Setenv("ANI_QUOTA_INFERENCE_ENABLED", "")
	c := QuotaInternalConfigFromEnv()
	require.NotContains(t, c.CertOwnerMap, "ani-inference-service")
	t.Setenv("ANI_QUOTA_INFERENCE_ENABLED", "true")
	c = QuotaInternalConfigFromEnv()
	require.NoError(t, c.Validate())
	require.Equal(t, commandcontract.InferenceOwner, c.CertOwnerMap["ani-inference-service"])
	require.Equal(t, "ani-resource-service", c.CertOwnerMap["ani-resource-service"])
	require.Equal(t, commandcontract.ModelDevOwner, c.CertOwnerMap[commandcontract.ModelDevOwner])
	for _, mapping := range []map[string]string{
		{"inference.alias": commandcontract.InferenceOwner},
		{"ani-inference-service": "ani-resource-service"},
	} {
		c.CertOwnerMap = mapping
		require.Error(t, c.Validate())
	}
	t.Setenv("ANI_QUOTA_RESOURCE_DNS_SAN", "ani-inference-service")
	c = QuotaInternalConfigFromEnv()
	require.Error(t, c.Validate(), "a Resource identity collision must fail closed")
}

func TestInferenceQuotaReleaseAuthenticatesTheRegisteredCertificate(t *testing.T) {
	dir, roots := quotaTLSFixture(t)
	t.Setenv("ANI_QUOTA_ENABLED", "true")
	t.Setenv("ANI_QUOTA_INTERNAL_ADDR", "127.0.0.1:0")
	t.Setenv("ANI_QUOTA_CA_FILE", filepath.Join(dir, "ca.pem"))
	t.Setenv("ANI_QUOTA_CERT_FILE", filepath.Join(dir, "server.pem"))
	t.Setenv("ANI_QUOTA_KEY_FILE", filepath.Join(dir, "server.key"))
	t.Setenv("ANI_QUOTA_RESOURCE_DNS_SAN", "")
	t.Setenv("ANI_QUOTA_MODELDEV_ENABLED", "")
	t.Setenv("ANI_QUOTA_INFERENCE_ENABLED", "true")
	server, err := NewQuotaInternalServer(QuotaInternalConfigFromEnv(), nil)
	require.NoError(t, err)
	started := quotaStartAsync(server)
	t.Cleanup(func() {
		require.NoError(t, server.Stop(context.Background()))
		require.NoError(t, quotaStartResult(t, started))
	})
	for _, tc := range []struct {
		name string
		code codes.Code
	}{{"inference", codes.InvalidArgument}, {"inference-mixed", codes.Unauthenticated}, {"resource", codes.Unauthenticated}, {"unknown", codes.Unauthenticated}} {
		t.Run(tc.name, func(t *testing.T) {
			pair, err := tls.LoadX509KeyPair(filepath.Join(dir, tc.name+".pem"), filepath.Join(dir, tc.name+".key"))
			require.NoError(t, err)
			connection, err := grpc.NewClient(server.listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "ani-governance", Certificates: []tls.Certificate{pair}})))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connection.Close()) })
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			// Invalid scope stops before the ledger, but only after real mTLS
			// authentication has injected the registered business owner.
			_, err = quotapb.NewQuotaReleaseServiceClient(connection).ReportResourceReleased(ctx, &quotapb.ReportResourceReleasedRequest{})
			require.Equal(t, tc.code, status.Code(err))
		})
	}
}
