package server

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	quotapb "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
)

func TestModelDevQuotaReleaseRegistrationIsExplicitAndExact(t *testing.T) {
	t.Setenv("ANI_QUOTA_ENABLED", "true")
	t.Setenv("ANI_QUOTA_INTERNAL_ADDR", "127.0.0.1:9443")
	t.Setenv("ANI_QUOTA_CA_FILE", "ca.pem")
	t.Setenv("ANI_QUOTA_CERT_FILE", "server.pem")
	t.Setenv("ANI_QUOTA_KEY_FILE", "server.key")
	t.Setenv("ANI_QUOTA_RESOURCE_DNS_SAN", "ani-resource-service")
	t.Setenv("ANI_QUOTA_MODELDEV_ENABLED", "")
	c := QuotaInternalConfigFromEnv()
	require.NotContains(t, c.CertOwnerMap, commandcontract.ModelDevOwner)
	t.Setenv("ANI_QUOTA_MODELDEV_ENABLED", "true")
	c = QuotaInternalConfigFromEnv()
	require.NoError(t, c.Validate())
	require.Equal(t, "ani-resource-service", c.CertOwnerMap["ani-resource-service"])
	require.Equal(t, commandcontract.ModelDevOwner, c.CertOwnerMap[commandcontract.ModelDevOwner])
	c.CertOwnerMap = map[string]string{"modeldev.alias": commandcontract.ModelDevOwner}
	require.Error(t, c.Validate())
	c.CertOwnerMap = map[string]string{commandcontract.ModelDevOwner: "ani-inference"}
	require.Error(t, c.Validate())
	t.Setenv("ANI_QUOTA_RESOURCE_DNS_SAN", commandcontract.ModelDevOwner)
	c = QuotaInternalConfigFromEnv()
	require.Error(t, c.Validate(), "owner registration must not overwrite a configured Resource identity")
}

func TestModelDevResourceReleaseRejectsCreateUUIDAsCloseUUID(t *testing.T) {
	create := uuid.NewString()
	request := &quotapb.ReportResourceReleasedRequest{ReleaseEventId: create, CreateOperationId: create, ResourceId: uuid.NewString(), Reason: quotapb.ReleaseReason_RESOURCE_RELEASED}
	// This unit checks the finite release shape before any ledger access; the
	// real mTLS owner authentication is covered by the listener integration.
	server := &QuotaInternalServer{}
	response, err := server.ReportResourceReleased(withQuotaOwner(context.Background(), commandcontract.ModelDevOwner), request)
	require.Error(t, err)
	require.Nil(t, response)
}
