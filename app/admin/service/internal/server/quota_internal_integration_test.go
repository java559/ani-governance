//go:build quota_pg

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	quotapb "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestQuotaInternalMTLSAndCumulativeRelease(t *testing.T) {
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	plan, err := c.Client().Plan.Create().SetName("quota-mtls").Save(ctx)
	require.NoError(t, err)
	tenant, err := c.Client().Tenant.Create().SetName("quota-mtls").SetCode("quota-mtls").SetPlanID(plan.ID).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(plan.ID).SetQuotaCode("storage.bytes").SetQuotaValue(8).Exec(ctx))
	ledger := data.NewQuotaLedgerRepo(testutil.NewBootstrapContext(nil), c)
	occupied, err := ledger.Occupy(context.Background(), &data.QuotaOccupyInput{TenantID: tenant.ID, ResourceTenantID: tenant.ResourceTenantID, ResourceID: uuid.NewString(), ActorType: "user", ActorID: "1", OwnerService: "other-owner", Action: "STORAGE_CREATE", IdempotencyKey: uuid.NewString(), RequestHash: "mtls-hash", CanonicalRequest: `{"resource":"storage"}`, Items: []data.QuotaOccupyItem{{QuotaCode: "storage.bytes", Units: 2}}})
	require.NoError(t, err)
	charge, err := c.Client().QuotaCharge.Query().Where(quotacharge.OperationIDEQ(occupied.OperationID)).Only(ctx)
	require.NoError(t, err)
	dir, pool := quotaTLSFixture(t)
	srv, err := NewQuotaInternalServer(QuotaInternalServerConfig{Enabled: true, Address: "127.0.0.1:0", CAFile: filepath.Join(dir, "ca.pem"), CertFile: filepath.Join(dir, "server.pem"), KeyFile: filepath.Join(dir, "server.key"), CertOwnerMap: map[string]string{"ani-resource-service": "ani-resource-service", "ani-resource-alias": "ani-resource-service", "other-owner": "other-owner"}}, ledger)
	require.NoError(t, err)
	startResult := make(chan error, 1)
	go func() { startResult <- srv.Start(context.Background()) }()
	t.Cleanup(func() {
		require.NoError(t, srv.Stop(context.Background()))
		select {
		case err := <-startResult:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("quota internal Start did not return after Stop")
		}
	})
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readyCancel()
	ownerPair, err := tls.LoadX509KeyPair(filepath.Join(dir, "owner.pem"), filepath.Join(dir, "owner.key"))
	require.NoError(t, err)
	readyConn, err := grpc.DialContext(readyCtx, srv.listener.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: "ani-governance",
			Certificates: []tls.Certificate{ownerPair},
		})), grpc.WithBlock())
	require.NoError(t, err)
	require.NoError(t, readyConn.Close())
	dial := func(name string, roots *x509.CertPool) quotapb.QuotaReleaseServiceClient {
		t.Helper()
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "ani-governance"}
		if name != "" {
			pair, e := tls.LoadX509KeyPair(filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key"))
			require.NoError(t, e)
			cfg.Certificates = []tls.Certificate{pair}
		}
		conn, e := grpc.NewClient(srv.listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
		require.NoError(t, e)
		t.Cleanup(func() { _ = conn.Close() })
		return quotapb.NewQuotaReleaseServiceClient(conn)
	}
	call := func(client quotapb.QuotaReleaseServiceClient, event string, total int64) error {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, e := client.ReportQuotaRelease(cctx, &quotapb.ReportQuotaReleaseRequest{ReleaseEventId: event, OperationId: occupied.OperationID, Items: []*quotapb.QuotaReleaseItem{{ChargeId: charge.ChargeID, QuotaCode: "storage.bytes", ReleasedTotal: total}}, Reason: quotapb.ReleaseReason_RESOURCE_RELEASED})
		return e
	}
	require.Error(t, call(dial("", pool), uuid.NewString(), 1), "client cert required")
	require.Error(t, call(dial("owner", x509.NewCertPool()), uuid.NewString(), 1), "trusted server CA required")
	require.Equal(t, codes.Unauthenticated, status.Code(call(dial("unknown", pool), uuid.NewString(), 1)))
	require.Equal(t, codes.Unauthenticated, status.Code(call(dial("mixed", pool), uuid.NewString(), 1)))
	balance := func() int64 {
		rows, e := ledger.RecomputeInvariants(context.Background(), tenant.ID)
		require.NoError(t, e)
		for _, r := range rows {
			require.True(t, r.Balanced)
			if r.QuotaCode == "storage.bytes" {
				return r.OccupiedUnits
			}
		}
		t.Fatal("missing gpu account")
		return -1
	}
	require.EqualValues(t, 2, balance())
	// This is the existing independent non-GPU cumulative-release surface.
	// Use its explicitly registered test owner rather than a retired instance command.
	owner := dial("other", pool)
	_, legacyGPUError := owner.ReportQuotaRelease(context.Background(), &quotapb.ReportQuotaReleaseRequest{ReleaseEventId: uuid.NewString(), OperationId: occupied.OperationID, Items: []*quotapb.QuotaReleaseItem{{ChargeId: charge.ChargeID, QuotaCode: "gpu.shared_memory_mib", ReleasedTotal: 2}}, Reason: quotapb.ReleaseReason_RESOURCE_RELEASED})
	require.Equal(t, codes.InvalidArgument, status.Code(legacyGPUError))
	event := uuid.NewString()
	require.NoError(t, call(owner, event, 1))
	require.EqualValues(t, 1, balance())
	require.NoError(t, call(owner, event, 1))
	require.EqualValues(t, 1, balance())
	require.Equal(t, codes.Unauthenticated, status.Code(call(dial("alias", pool), uuid.NewString(), 2)))
	require.NoError(t, call(owner, uuid.NewString(), 2))
	require.EqualValues(t, 0, balance())
	require.NoError(t, call(owner, uuid.NewString(), 1))
	require.EqualValues(t, 0, balance())
	// The new resource-release RPC carries no quota codes or quantities.
	original, err := ledger.Occupy(context.Background(), &data.QuotaOccupyInput{TenantID: tenant.ID, ResourceTenantID: tenant.ResourceTenantID, ResourceID: uuid.NewString(), ActorType: "user", ActorID: "1", OwnerService: "other-owner", Action: "STORAGE_CREATE", IdempotencyKey: uuid.NewString(), RequestHash: "complete-hash", CanonicalRequest: `{"resource":"storage"}`, Items: []data.QuotaOccupyItem{{QuotaCode: "storage.bytes", Units: 3}}})
	require.NoError(t, err)
	full := &quotapb.ReportResourceReleasedRequest{ReleaseEventId: uuid.NewString(), CreateOperationId: original.OperationID, ResourceId: original.ResourceID, Reason: quotapb.ReleaseReason_RESOURCE_RELEASED}
	callFull := func(client quotapb.QuotaReleaseServiceClient, r *quotapb.ReportResourceReleasedRequest) (*quotapb.ReportResourceReleasedResponse, error) {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return client.ReportResourceReleased(cctx, r)
	}
	require.EqualValues(t, 3, balance())
	wrong := proto.Clone(full).(*quotapb.ReportResourceReleasedRequest)
	wrong.ResourceId = uuid.NewString()
	_, err = callFull(owner, wrong)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.EqualValues(t, 3, balance())
	_, err = callFull(dial("owner", pool), full)
	require.Error(t, err)
	require.EqualValues(t, 3, balance())
	ack, err := callFull(owner, full)
	require.NoError(t, err)
	require.Equal(t, full.ReleaseEventId, ack.ReleaseEventId)
	require.Equal(t, full.CreateOperationId, ack.CreateOperationId)
	require.Equal(t, full.ResourceId, ack.ResourceId)
	require.Equal(t, quotapb.ResourceReleaseStatus_APPLIED, ack.Status)
	require.Zero(t, balance())
	ack, err = callFull(owner, full)
	require.NoError(t, err)
	require.Equal(t, quotapb.ResourceReleaseStatus_ALREADY_APPLIED, ack.Status)
	require.Zero(t, balance())
	changed := proto.Clone(full).(*quotapb.ReportResourceReleasedRequest)
	changed.Reason = quotapb.ReleaseReason_ABORTED_CLEANED
	_, err = callFull(owner, changed)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Zero(t, balance())
	repeated := proto.Clone(full).(*quotapb.ReportResourceReleasedRequest)
	repeated.ReleaseEventId = uuid.NewString()
	ack, err = callFull(owner, repeated)
	require.NoError(t, err)
	require.Equal(t, quotapb.ResourceReleaseStatus_ALREADY_APPLIED, ack.Status)
	require.Zero(t, balance())

}
