//go:build modeldev_contract

package data

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"go-wind-admin/app/admin/service/tests/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The provider is the separately selected ModelDev governance_contract test.
// It runs real buildApp, pinned files and PostgreSQL; this package imports only
// the provider's public API/contract, never its internal implementation.
func TestModelDevResolveUsesRealManagedAdmissionProvider(t *testing.T) {
	fixture := testutil.ReadModelDevContractFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	wantCanonical, wantHash := testutil.ExpectedModelDevContractSnapshot(t)
	ca, err := os.ReadFile(fixture.TLS.CAFile)
	if err != nil {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: private CA unavailable; behavior NOT_RUN")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: private CA invalid; behavior NOT_RUN")
	}
	certificate, err := tls.LoadX509KeyPair(fixture.TLS.CertFile, fixture.TLS.KeyFile)
	if err != nil {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: private client material invalid; behavior NOT_RUN")
	}
	connection, err := grpc.NewClient(fixture.Address, grpc.WithDisableServiceConfig(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: "ani-modeldev-service",
	})))
	if err != nil {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: direct channel configuration failed; behavior NOT_RUN")
	}
	t.Cleanup(func() { _ = connection.Close() })
	intent, err := contractpb.EncodeIntent(fixture.Intent)
	if err != nil {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: fixture intent invalid; behavior NOT_RUN")
	}
	directContext := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"x-ani-tenant-id", fixture.Scope.ResourceTenantID, "x-ani-actor", fixture.Scope.Actor, "x-ani-request-id", uuid.NewString(),
	))
	admissionClient := modeldevv1.NewModelDevAdmissionServiceClient(connection)
	prepared, err := admissionClient.PrepareAdmission(directContext, &modeldevv1.PrepareAdmissionRequest{Intent: intent, IdentityRef: &modeldevv1.IdentityRef{TenantId: fixture.IdentityRef.TenantID, Actor: fixture.IdentityRef.Actor, OperationId: fixture.IdentityRef.OperationID, ExecutionId: fixture.IdentityRef.ExecutionID, IntentHash: fixture.IdentityRef.IntentHash}, ComputeSelectionCanonical: []byte(fixture.ComputeSelectionCanonical), SelectionDigest: fixture.SelectionDigest, AcceptedAt: timestamppb.New(fixture.AcceptedAt)}, grpc.WaitForReady(true))
	if err != nil || prepared == nil {
		t.Fatalf("MODELDEV_CLIENT_PREFLIGHT: direct Prepare unavailable code=%s", status.Code(err))
	}
	direct, err := admissionClient.FinalizeAdmission(directContext, &modeldevv1.FinalizeAdmissionRequest{CandidateCanonical: prepared.CandidateCanonical, CandidateHash: prepared.CandidateHash}, grpc.WaitForReady(true))
	if err != nil || direct == nil {
		t.Fatalf("MODELDEV_CLIENT_PREFLIGHT: generated direct RPC unavailable (code=%s); behavior NOT_RUN", status.Code(err))
	}
	directSnapshot, err := contractpb.DecodeSnapshot(direct.Snapshot)
	if err != nil {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: direct RPC returned invalid snapshot; behavior NOT_RUN")
	}
	directCanonical, err := directSnapshot.Canonical()
	if err != nil || !bytes.Equal(directCanonical, wantCanonical) || direct.ExecutionSpecHash != wantHash {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: real provider differs from independently authored snapshot/hash; behavior NOT_RUN")
	}
	t.Log("MODELDEV_CLIENT_PREFLIGHT PASS: generated direct RPC over real mTLS to ModelDev buildApp with pinned files and durable READY input")

	client, closeClient, err := NewModelDevClient(ModelDevClientConfig{
		Address: fixture.Address, CAFile: fixture.TLS.CAFile, CertFile: fixture.TLS.CertFile, KeyFile: fixture.TLS.KeyFile, Timeout: 3 * time.Second,
	})
	if err != nil || client == nil || closeClient == nil {
		t.Fatal("MODELDEV_CLIENT_BEHAVIOR: valid client configuration rejected")
	}
	t.Cleanup(closeClient)
	resolved, err := client.ResolvePrepared(ctx,
		ModelDevResolveScope{ResourceTenantID: fixture.Scope.ResourceTenantID, Actor: fixture.Scope.Actor},
		fixture.Intent,
		fixture.IdentityRef, testutil.ModelDevContractSelection(t, fixture),
		fixture.AcceptedAt,
	)
	if err != nil {
		if status.Code(err) == codes.Unimplemented && status.Convert(err).Message() == "modeldev resolution client not implemented" {
			t.Fatal("MODELDEV_CLIENT_BEHAVIOR: modeldev resolution client not implemented after real provider preflight PASS")
		}
		t.Fatalf("MODELDEV_CLIENT_BEHAVIOR: resolution failed (code=%s)", status.Code(err))
	}
	canonical, err := resolved.Snapshot.Canonical()
	if err != nil || !bytes.Equal(canonical, wantCanonical) || resolved.ExecutionSpecHash != wantHash {
		t.Fatal("MODELDEV_CLIENT_BEHAVIOR: client did not return the independent complete snapshot/hash")
	}
}
