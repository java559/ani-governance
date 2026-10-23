//go:build quota_pg

package service

import (
	"context"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	pagination "go-wind-admin/pkg/localdeps/go-crud/api/gen/go/pagination/v1"
	"go-wind-admin/pkg/middleware/auth"
)

func TestQuotaAdminAndSelfDomainViews(t *testing.T) {
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	sys := appViewer.NewSystemViewerContext(context.Background())
	plan, err := c.Client().Plan.Create().SetName("quota-domain-views").Save(sys)
	require.NoError(t, err)
	tenant, err := c.Client().Tenant.Create().SetName("quota-domain-views").SetCode("quota-domain-views").SetPlanID(plan.ID).Save(sys)
	require.NoError(t, err)
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(plan.ID).SetQuotaCode(GpuPhysicalQuotaCode).SetQuotaValue(4).Exec(sys))

	repo := data.NewQuotaAdminRepo(testutil.NewBootstrapContext(nil), c)
	platform := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 0})
	self := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 8, TenantID: tenant.ID})
	adminService := NewQuotaAdminService(testutil.NewBootstrapContext(nil), repo)
	definitions, err := adminService.ListQuotaDefinitions(platform, &pagination.PagingRequest{NoPaging: proto.Bool(true)})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(definitions.Items), 3)
	require.EqualValues(t, len(definitions.Items), definitions.Total)
	platformAccounts, err := adminService.ListTenantQuotaAccounts(platform, &admin.GetTenantQuotaAccountsRequest{Id: tenant.ID})
	require.NoError(t, err)
	require.Equal(t, tenant.ID, platformAccounts.TenantId)
	require.Len(t, platformAccounts.Items, len(definitions.Items))

	selfService := NewAcceleratorService(nil, nil, nil, nil, repo, NewQuotaAdapterRegistry(), nil)
	selfAccounts, err := selfService.GetMyQuotaAccounts(self, &emptypb.Empty{})
	require.NoError(t, err)
	require.Equal(t, tenant.ID, selfAccounts.TenantId)
	require.True(t, proto.Equal(platformAccounts, selfAccounts))

	_, err = adminService.ListTenantQuotaAccounts(self, &admin.GetTenantQuotaAccountsRequest{Id: tenant.ID})
	require.Equal(t, "QUOTA_NOT_FOUND", kratoserrors.FromError(err).Reason)

	bridge := NewGpuBffLedgerBridge(nil, repo)
	checks, err := bridge.GPUPreviewQuota(self, tenant.ID, []*view.GpuPreviewQuotaItem{{QuotaCode: GpuPhysicalQuotaCode, Units: 2}})
	require.NoError(t, err)
	require.Len(t, checks, 1)
	require.True(t, checks[0].Sufficient)
	require.EqualValues(t, 4, checks[0].Available)
}
