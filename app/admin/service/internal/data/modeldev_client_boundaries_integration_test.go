//go:build modeldev_contract

package data

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"go-wind-admin/app/admin/service/tests/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// A separate ModelDev process runs buildApp, pinned implementation/facts and
// real PostgreSQL READY input; only the external storage observation is synthetic.
func TestModelDevResolveRealProviderBoundaries(t *testing.T) {
	f := testutil.ReadModelDevContractFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config := ModelDevClientConfig{Address: f.Address, CAFile: f.TLS.CAFile, CertFile: f.TLS.CertFile, KeyFile: f.TLS.KeyFile, Timeout: 2 * time.Second}
	scope := ModelDevResolveScope{ResourceTenantID: f.Scope.ResourceTenantID, Actor: f.Scope.Actor}
	selection := testutil.ModelDevContractSelection(t, f)
	want, hash := testutil.ExpectedModelDevContractSnapshot(t)
	client := newModelDevBoundaryClient(t, config)
	control, err := client.ResolvePrepared(ctx, scope, f.Intent, f.IdentityRef, selection, f.AcceptedAt)
	assertRealModelDevCandidate(t, control, err, want, hash)
	t.Log("MODELDEV_BOUNDARY_PREFLIGHT PASS: separate real buildApp/PG/mTLS Prepare and Finalize")
	t.Run("rebuild metadata and preserve caller values", func(t *testing.T) {
		poison := metadata.Pairs("x-ani-tenant-id", uuid.NewString(), "x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "governance:user:900", "x-ani-actor", "governance:user:901", "authorization", "Bearer caller-private-sentinel", "cookie", "caller-private-sentinel", "x-forwarded-client-cert", "caller-private-sentinel", "ani-workload-token", "caller-private-sentinel")
		before := poison.Copy()
		beforeIntent, _, e := cpup01.CanonicalIntent(f.Intent)
		require.NoError(t, e)
		polluted := metadata.NewOutgoingContext(metadata.NewIncomingContext(ctx, poison), poison)
		got, e := client.ResolvePrepared(polluted, scope, f.Intent, f.IdentityRef, selection, f.AcceptedAt)
		assertRealModelDevCandidate(t, got, e, want, hash)
		afterIntent, _, e := cpup01.CanonicalIntent(f.Intent)
		require.NoError(t, e)
		outgoing, _ := metadata.FromOutgoingContext(polluted)
		incoming, _ := metadata.FromIncomingContext(polluted)
		require.Equal(t, beforeIntent, afterIntent)
		require.True(t, reflect.DeepEqual(before, poison))
		require.True(t, reflect.DeepEqual(before, outgoing))
		require.True(t, reflect.DeepEqual(before, incoming))
	})
	t.Run("other tenant cannot borrow facts and requests have fresh correlation", func(t *testing.T) {
		other := scope
		other.ResourceTenantID = uuid.NewString()
		identity := f.IdentityRef
		identity.TenantID = other.ResourceTenantID
		lastID := ""
		for n := 0; n < 2; n++ {
			got, e := client.ResolvePrepared(ctx, other, f.Intent, identity, selection, f.AcceptedAt)
			require.Equal(t, ModelDevResolution{}, got)
			require.Equal(t, codes.Unavailable, status.Code(e))
			require.NotContains(t, e.Error(), "private-sentinel")
			details := status.Convert(e).Details()
			require.Len(t, details, 1)
			detail, ok := details[0].(*modeldevv1.ErrorDetail)
			require.True(t, ok)
			require.Equal(t, modeldevv1.ErrorReason_ERROR_REASON_ENVIRONMENT_NOT_READY, detail.Reason)
			require.NotEmpty(t, detail.CorrelationId)
			require.NotEqual(t, lastID, detail.CorrelationId)
			lastID = detail.CorrelationId
		}
	})
	t.Run("actual provider rejects absent input", func(t *testing.T) {
		intent := f.Intent
		intent.DatasetVersionID = uuid.NewString()
		identity := f.IdentityRef
		_, identity.IntentHash, err = cpup01.CanonicalIntent(intent)
		require.NoError(t, err)
		got, e := client.ResolvePrepared(ctx, scope, intent, identity, selection, f.AcceptedAt)
		require.Equal(t, ModelDevResolution{}, got)
		require.Equal(t, codes.NotFound, status.Code(e))
		require.Equal(t, "input version unavailable", status.Convert(e).Message())
	})
	for _, field := range []string{"zero tenant", "wrong actor", "wrong intent hash", "wrong preset", "zero time", "submicrosecond time"} {
		t.Run(field, func(t *testing.T) {
			badScope, identity, at := scope, f.IdentityRef, f.AcceptedAt
			badIntent := f.Intent
			switch field {
			case "zero tenant":
				badScope.ResourceTenantID = uuid.Nil.String()
				identity.TenantID = badScope.ResourceTenantID
			case "wrong actor":
				badScope.Actor = "governance:user:4294967296"
				identity.Actor = badScope.Actor
			case "wrong intent hash":
				identity.IntentHash = "invalid"
			case "wrong preset":
				badIntent.ComputePresetID = uuid.NewString()
				_, identity.IntentHash, _ = cpup01.CanonicalIntent(badIntent)
			case "zero time":
				at = time.Time{}
			case "submicrosecond time":
				at = at.Add(time.Nanosecond)
			}
			got, e := client.ResolvePrepared(ctx, badScope, badIntent, identity, selection, at)
			require.Equal(t, ModelDevResolution{}, got)
			require.Equal(t, codes.InvalidArgument, status.Code(e))
		})
	}
	t.Run("cancellation wins before validation", func(t *testing.T) {
		canceled, stop := context.WithCancel(ctx)
		stop()
		got, e := client.ResolvePrepared(canceled, ModelDevResolveScope{}, f.Intent, f.IdentityRef, selection, f.AcceptedAt)
		require.Equal(t, ModelDevResolution{}, got)
		require.Equal(t, codes.Canceled, status.Code(e))
		expired, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
		defer stop()
		_, e = client.ResolvePrepared(expired, ModelDevResolveScope{}, f.Intent, f.IdentityRef, selection, f.AcceptedAt)
		require.Equal(t, codes.DeadlineExceeded, status.Code(e))
	})
	t.Run("foreign CA cannot reach real provider", func(t *testing.T) {
		foreign, _ := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance"})
		bad := config
		bad.CAFile = foreign.CAFile
		got, e := newModelDevBoundaryClient(t, bad).ResolvePrepared(ctx, scope, f.Intent, f.IdentityRef, selection, f.AcceptedAt)
		require.Equal(t, ModelDevResolution{}, got)
		require.Equal(t, codes.Unavailable, status.Code(e))
	})
	got, err := client.ResolvePrepared(ctx, scope, f.Intent, f.IdentityRef, selection, f.AcceptedAt)
	assertRealModelDevCandidate(t, got, err, want, hash)
}
func assertRealModelDevCandidate(t *testing.T, got ModelDevResolution, err error, want []byte, hash string) {
	t.Helper()
	require.NoError(t, err)
	raw, e := got.Snapshot.Canonical()
	require.NoError(t, e)
	require.True(t, bytes.Equal(want, raw))
	require.Equal(t, hash, got.ExecutionSpecHash)
}
