package logging

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	auditV1 "go-wind-admin/api/gen/go/audit/service/v1"
	"go-wind-admin/pkg/middleware/auth"
)

func TestNetworkAuditRedactsRawRequestForEveryTenantOperation(t *testing.T) {
	contract := adminV1.File_admin_service_v1_i_network_proto.Services().ByName("NetworkService")
	require.NotNil(t, contract)
	methods := contract.Methods()
	require.Len(t, auth.NetworkOperations, methods.Len())
	for index := 0; index < methods.Len(); index++ {
		operation := "/" + string(contract.FullName()) + "/" + string(methods.Get(index).Name())
		require.Contains(t, auth.NetworkOperations, operation, "every released Network RPC must retain audit redaction")
	}
	for operation, policy := range auth.NetworkOperations {
		t.Run(operation, func(t *testing.T) {
			var record *auditV1.ApiAuditLog
			server := khttp.NewServer(khttp.Middleware(Server(WithWriteApiLogFunc(func(_ context.Context, log *auditV1.ApiAuditLog) error { record = log; return nil }))))
			const body = `{"name":"private-network-config","idempotency_key":"private-idempotency-value"}`
			server.Route("/").Handle(policy.Method, "/network-audit", func(c khttp.Context) error {
				khttp.SetOperation(c, operation)
				_, err := c.Middleware(func(ctx context.Context, _ interface{}) (interface{}, error) {
					// Logger unit boundary: verified identity is recorded by the
					// authentication layer; no signing/JWT logic is replaced here.
					ctx = auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectAPIKey, ID: 42, TenantID: 7})
					require.Nil(t, bodySnapshotFromContext(ctx), "sensitive raw bytes must not be retained for secondary audits")
					got, err := io.ReadAll(c.Request().Body)
					require.NoError(t, err)
					require.Equal(t, body, string(got), "redaction must leave the business request intact")
					return nil, nil
				})(c, nil)
				return err
			})
			r := httptest.NewRequest(policy.Method, "/network-audit?cursor=private-cursor-value", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Access-Key", "ak-log-fixture")
			r.Header.Set("X-Signature", strings.Repeat("f", 64))
			r.Header.Set("Referer", "https://example.test/?private=referer")
			server.ServeHTTP(httptest.NewRecorder(), r)
			require.NotNil(t, record)
			require.Equal(t, "[redacted]", record.GetRequestBody())
			require.Equal(t, "/network-audit", record.GetRequestUri())
			require.Empty(t, record.GetReferer())
			require.Empty(t, record.GetRequestHeader())
			require.Empty(t, record.GetResponse())
			require.Equal(t, "api_key", record.GetSubjectType())
			require.Equal(t, uint32(42), record.GetSubjectId())
			require.Equal(t, uint32(7), record.GetTenantId())
			require.Zero(t, record.GetUserId())
		})
	}
}
