package data

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// External response peers use real TLS sockets. They do not replace Gov
// authorization/persistence or claim to run ModelDev's planning implementation.
type modelDevResponseCase struct {
	scope      ModelDevResolveScope
	intent     cpup01.Intent
	identity   cpup01.IdentityRef
	selection  *compute.ComputeSelection
	acceptedAt time.Time
	candidate  cpup01.PreparedCandidate
	response   *modeldevv1.FinalizeAdmissionResponse
}

func modelDevResponseFixture(t *testing.T) modelDevResponseCase {
	t.Helper()
	snapshot := conformance.SnapshotV1()
	intent := conformance.IntentV1()
	scope := ModelDevResolveScope{ResourceTenantID: uuid.NewString(), Actor: "governance:user:42"}
	_, hash, err := cpup01.CanonicalIntent(intent)
	require.NoError(t, err)
	identity := cpup01.IdentityRef{TenantID: scope.ResourceTenantID, Actor: scope.Actor, OperationID: uuid.NewString(), ExecutionID: uuid.NewString(), IntentHash: hash}
	snapshot.IdentityRef = identity
	snapshot.RuntimePlan.Name = cpup01.RuntimeName(snapshot.Environment.NamespaceUID, identity.ExecutionID)
	selection := &compute.ComputeSelection{SchemaVersion: compute.SelectionSchemaVersion, PresetId: intent.ComputePresetID, Code: "cpu-small", Name: "CPU small", ComputeType: "cpu", CpuMillis: 1000, MemoryMib: 1024}
	selection.SelectionDigest, err = compute.SelectionDigest(selection)
	require.NoError(t, err)
	selectionBytes, err := compute.CanonicalSelection(selection)
	require.NoError(t, err)
	snapshot.ComputeOrigin = cpup01.ComputeOrigin{ComputePresetID: selection.PresetId, SelectionDigest: selection.SelectionDigest, SelectionJSON: string(selectionBytes), CPUMillis: 1000, MemoryMiB: 1024, DeviceMode: "cpu"}
	wire, err := contractpb.EncodeSnapshot(snapshot)
	require.NoError(t, err)
	digest, err := snapshot.Digest()
	require.NoError(t, err)
	incomplete := snapshot
	incomplete.RuntimePlan = cpup01.RuntimePlan{}
	candidate := cpup01.PreparedCandidate{SchemaVersion: cpup01.CandidateSchemaVersion, Intent: intent, Snapshot: incomplete}
	require.NoError(t, candidate.Validate())
	return modelDevResponseCase{scope: scope, intent: intent, identity: identity, selection: selection, acceptedAt: snapshot.Timing.AcceptedAt, candidate: candidate, response: &modeldevv1.FinalizeAdmissionResponse{Snapshot: wire, ExecutionSpecHash: digest}}
}
func (f modelDevResponseCase) resolve(ctx context.Context, c *ModelDevClient) (ModelDevResolution, error) {
	return c.ResolvePrepared(ctx, f.scope, f.intent, f.identity, f.selection, f.acceptedAt)
}
func newModelDevBoundaryClient(t *testing.T, config ModelDevClientConfig) *ModelDevClient {
	t.Helper()
	c, close, err := NewModelDevClient(config)
	require.NoError(t, err)
	require.NotNil(t, c)
	require.NotNil(t, close)
	t.Cleanup(close)
	return c
}

type modelDevPlanPeer struct {
	modeldevv1.UnimplementedModelDevAdmissionServiceServer
	fixture       modelDevResponseCase
	prepare       func(context.Context, *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error)
	finalize      func(context.Context, *modeldevv1.FinalizeAdmissionRequest) (*modeldevv1.FinalizeAdmissionResponse, error)
	prepareCalls  atomic.Int32
	finalizeCalls atomic.Int32
}

func (p *modelDevPlanPeer) PrepareAdmission(ctx context.Context, r *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error) {
	p.prepareCalls.Add(1)
	if p.prepare != nil {
		return p.prepare(ctx, r)
	}
	b, e := p.fixture.candidate.Canonical()
	if e != nil {
		return nil, e
	}
	h, e := p.fixture.candidate.Digest()
	if e != nil {
		return nil, e
	}
	return &modeldevv1.PrepareAdmissionResponse{CandidateCanonical: b, CandidateHash: h}, nil
}
func (p *modelDevPlanPeer) FinalizeAdmission(ctx context.Context, r *modeldevv1.FinalizeAdmissionRequest) (*modeldevv1.FinalizeAdmissionResponse, error) {
	p.finalizeCalls.Add(1)
	if p.finalize != nil {
		return p.finalize(ctx, r)
	}
	return proto.Clone(p.fixture.response).(*modeldevv1.FinalizeAdmissionResponse), nil
}
func startModelDevPlanPeer(t *testing.T, names []string, f modelDevResponseCase, prepare func(context.Context, *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error), finalize func(context.Context, *modeldevv1.FinalizeAdmissionRequest) (*modeldevv1.FinalizeAdmissionResponse, error)) (ModelDevClientConfig, *modelDevPlanPeer) {
	t.Helper()
	config, security := modelDevBoundaryTLS(t, names, []string{"ani-governance"})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	config.Address = listener.Addr().String()
	peer := &modelDevPlanPeer{fixture: f, prepare: prepare, finalize: finalize}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(security)))
	modeldevv1.RegisterModelDevAdmissionServiceServer(server, peer)
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve(listener) }()
	return config, peer
}

func TestModelDevPlanPreservesFixedCandidateAndRebuildsMetadata(t *testing.T) {
	f := modelDevResponseFixture(t)
	type prepareObservation struct {
		request *modeldevv1.PrepareAdmissionRequest
		md      metadata.MD
	}
	type finalObservation struct {
		request *modeldevv1.FinalizeAdmissionRequest
		md      metadata.MD
	}
	prepares := make(chan prepareObservation, 1)
	finals := make(chan finalObservation, 1)
	raw, err := f.candidate.Canonical()
	require.NoError(t, err)
	hash, err := f.candidate.Digest()
	require.NoError(t, err)
	config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, func(ctx context.Context, r *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		prepares <- prepareObservation{proto.Clone(r).(*modeldevv1.PrepareAdmissionRequest), md.Copy()}
		return &modeldevv1.PrepareAdmissionResponse{CandidateCanonical: raw, CandidateHash: hash}, nil
	}, func(ctx context.Context, r *modeldevv1.FinalizeAdmissionRequest) (*modeldevv1.FinalizeAdmissionResponse, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		finals <- finalObservation{proto.Clone(r).(*modeldevv1.FinalizeAdmissionRequest), md.Copy()}
		return proto.Clone(f.response).(*modeldevv1.FinalizeAdmissionResponse), nil
	})
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "private-sentinel", "x-ani-tenant-id", uuid.NewString(), "x-ani-actor", "private-sentinel"))
	got, err := f.resolve(ctx, newModelDevBoundaryClient(t, config))
	require.NoError(t, err)
	require.Equal(t, f.response.ExecutionSpecHash, got.ExecutionSpecHash)
	require.Equal(t, int32(1), peer.prepareCalls.Load())
	require.Equal(t, int32(1), peer.finalizeCalls.Load())
	observedPrepare, observedFinal := <-prepares, <-finals
	require.Equal(t, f.identity.ExecutionID, observedPrepare.request.IdentityRef.ExecutionId)
	require.Equal(t, f.identity.OperationID, observedPrepare.request.IdentityRef.OperationId)
	require.Equal(t, f.identity.IntentHash, observedPrepare.request.IdentityRef.IntentHash)
	require.True(t, f.acceptedAt.Equal(observedPrepare.request.AcceptedAt.AsTime()))
	origin, err := compute.CanonicalSelection(f.selection)
	require.NoError(t, err)
	require.Equal(t, origin, observedPrepare.request.ComputeSelectionCanonical)
	require.Equal(t, raw, observedFinal.request.CandidateCanonical)
	require.Equal(t, hash, observedFinal.request.CandidateHash)
	require.Empty(t, observedFinal.request.GpuExecutionCanonical)
	require.Empty(t, observedFinal.request.GpuDisplayCanonical)
	for _, md := range []metadata.MD{observedPrepare.md, observedFinal.md} {
		require.Equal(t, []string{f.scope.ResourceTenantID}, md.Get("x-ani-tenant-id"))
		require.Equal(t, []string{f.scope.Actor}, md.Get("x-ani-actor"))
		require.Empty(t, md.Get("authorization"))
		require.Len(t, md.Get("x-ani-request-id"), 1)
	}
	require.NotEqual(t, observedPrepare.md.Get("x-ani-request-id"), observedFinal.md.Get("x-ani-request-id"))
}

func TestModelDevPrepareRejectsWrongScopeOrChangedObservation(t *testing.T) {
	for _, field := range []string{"identity", "accepted_at", "compute", "hash", "unknown"} {
		t.Run(field, func(t *testing.T) {
			f := modelDevResponseFixture(t)
			c := f.candidate
			switch field {
			case "identity":
				c.Snapshot.IdentityRef.OperationID = uuid.NewString()
			case "accepted_at":
				c.Snapshot.Timing.AcceptedAt = c.Snapshot.Timing.AcceptedAt.Add(time.Second)
				c.Snapshot.Timing.ExecutionDeadlineAt = c.Snapshot.Timing.ExecutionDeadlineAt.Add(time.Second)
				c.Snapshot.DeadlineAt = c.Snapshot.Timing.ExecutionDeadlineAt
			case "compute":
				c.Snapshot.Resources.RequestMillicpu++
				c.Snapshot.Resources.LimitMillicpu++
				changed := proto.Clone(f.selection).(*compute.ComputeSelection)
				changed.CpuMillis++
				changed.SelectionDigest, _ = compute.SelectionDigest(changed)
				changedBytes, _ := compute.CanonicalSelection(changed)
				c.Snapshot.ComputeOrigin.CPUMillis = changed.CpuMillis
				c.Snapshot.ComputeOrigin.SelectionDigest = changed.SelectionDigest
				c.Snapshot.ComputeOrigin.SelectionJSON = string(changedBytes)
			}
			raw, err := c.Canonical()
			require.NoError(t, err)
			hash, err := c.Digest()
			require.NoError(t, err)
			if field == "hash" {
				hash = strings.Repeat("0", 64)
			}
			out := &modeldevv1.PrepareAdmissionResponse{CandidateCanonical: raw, CandidateHash: hash}
			if field == "unknown" {
				out.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			}
			config, peer := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, func(context.Context, *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error) {
				return out, nil
			}, nil)
			got, err := f.resolve(context.Background(), newModelDevBoundaryClient(t, config))
			require.Error(t, err)
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.Empty(t, got.ExecutionSpecHash)
			require.Zero(t, peer.finalizeCalls.Load())
		})
	}
}
func TestModelDevFinalizeRejectsCorrectlyHashedChangedCandidate(t *testing.T) {
	for _, field := range []string{"parameters", "deadline", "identity", "nil", "unknown", "hash"} {
		t.Run(field, func(t *testing.T) {
			f := modelDevResponseFixture(t)
			out := proto.Clone(f.response).(*modeldevv1.FinalizeAdmissionResponse)
			switch field {
			case "nil":
				out = nil
			case "unknown":
				out.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			case "hash":
				out.ExecutionSpecHash = strings.Repeat("0", 64)
			default:
				snapshot, err := contractpb.DecodeSnapshot(out.Snapshot)
				require.NoError(t, err)
				switch field {
				case "parameters":
					for i, p := range snapshot.Program.ResolvedParameters {
						if p.Name == "epochs" {
							snapshot.Program.ResolvedParameters[i].Value = "17"
						}
					}
				case "deadline":
					snapshot.Timing.AcceptedAt = snapshot.Timing.AcceptedAt.Add(time.Second)
					snapshot.Timing.ExecutionDeadlineAt = snapshot.Timing.ExecutionDeadlineAt.Add(time.Second)
					snapshot.DeadlineAt = snapshot.Timing.ExecutionDeadlineAt
				case "identity":
					snapshot.IdentityRef.OperationID = uuid.NewString()
				}
				out.Snapshot, err = contractpb.EncodeSnapshot(snapshot)
				require.NoError(t, err)
				out.ExecutionSpecHash, err = snapshot.Digest()
				require.NoError(t, err)
			}
			config, _ := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, nil, func(context.Context, *modeldevv1.FinalizeAdmissionRequest) (*modeldevv1.FinalizeAdmissionResponse, error) {
				return out, nil
			})
			got, err := f.resolve(context.Background(), newModelDevBoundaryClient(t, config))
			require.Error(t, err)
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.Empty(t, got.ExecutionSpecHash)
		})
	}
}
func TestModelDevPlanCancellationAndRemoteErrorSanitization(t *testing.T) {
	f := modelDevResponseFixture(t)
	entered := make(chan struct{})
	config, _ := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, func(ctx context.Context, _ *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error) {
		close(entered)
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}, nil)
	client := newModelDevBoundaryClient(t, config)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := f.resolve(ctx, client); done <- err }()
	<-entered
	cancel()
	require.Equal(t, codes.Canceled, status.Code(<-done))
	for _, typed := range []bool{false, true} {
		config, _ := startModelDevPlanPeer(t, []string{"ani-modeldev-service"}, f, func(ctx context.Context, _ *modeldevv1.PrepareAdmissionRequest) (*modeldevv1.PrepareAdmissionResponse, error) {
			if !typed {
				return nil, status.Error(codes.NotFound, "private-sentinel")
			}
			md, _ := metadata.FromIncomingContext(ctx)
			err, e := status.New(codes.NotFound, "private-sentinel").WithDetails(&modeldevv1.ErrorDetail{Reason: modeldevv1.ErrorReason_ERROR_REASON_RESOURCE_NOT_FOUND, SafeMessage: "private-sentinel", CorrelationId: md.Get("x-ani-request-id")[0]})
			if e != nil {
				return nil, status.Error(codes.Internal, "test detail construction failed")
			}
			return nil, err.Err()
		}, nil)
		_, err := f.resolve(context.Background(), newModelDevBoundaryClient(t, config))
		require.NotContains(t, err.Error(), "private-sentinel")
		if typed {
			require.Equal(t, codes.NotFound, status.Code(err))
		} else {
			require.Equal(t, codes.Unavailable, status.Code(err))
		}
	}
}
func TestModelDevClientRequiresFixedTLSIdentityAndMaterials(t *testing.T) {
	f := modelDevResponseFixture(t)
	for _, names := range [][]string{{"ani-modeldev-service", "extra.test"}, {"ani-modeldev-service", "*.ani.internal"}, {"other-modeldev.test"}} {
		config, peer := startModelDevPlanPeer(t, names, f, nil, nil)
		_, err := f.resolve(context.Background(), newModelDevBoundaryClient(t, config))
		if len(names) == 2 && names[1] == "extra.test" {
			require.NoError(t, err)
		} else {
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.Zero(t, peer.prepareCalls.Load())
		}
	}
	config, _ := modelDevBoundaryTLS(t, []string{"ani-modeldev-service"}, []string{"ani-governance", "other"})
	config.Address = "127.0.0.1:1"
	c, cleanup, err := NewModelDevClient(config)
	require.Error(t, err)
	require.Nil(t, c)
	require.Nil(t, cleanup)
}

func modelDevDirectTLSConnection(t *testing.T, config ModelDevClientConfig, serverName string) *grpc.ClientConn {
	t.Helper()
	ca, err := os.ReadFile(config.CAFile)
	if err != nil {
		t.Fatal("MODELDEV_RESPONSE_PREFLIGHT: CA material unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("MODELDEV_RESPONSE_PREFLIGHT: CA material invalid")
	}
	certificate, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		t.Fatal("MODELDEV_RESPONSE_PREFLIGHT: client material invalid")
	}
	connection, err := grpc.NewClient(config.Address, grpc.WithDisableServiceConfig(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: serverName})))
	if err != nil {
		t.Fatal("MODELDEV_RESPONSE_PREFLIGHT: channel configuration failed")
	}
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

// Only this test's private CA/keys are written. No real provider material or
// database credential is copied, printed, or exported by the response tests.
func modelDevBoundaryTLS(t *testing.T, serverNames, clientNames []string) (ModelDevClientConfig, *tls.Config) {
	t.Helper()
	directory := t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ModelDev response substitute CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, public, private)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caFile := filepath.Join(directory, "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	issue := func(label string, serial int64, names []string, usage x509.ExtKeyUsage) (string, string, tls.Certificate) {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: names, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		encoded, err := x509.CreateCertificate(rand.Reader, leaf, ca, pub, private)
		if err != nil {
			t.Fatal(err)
		}
		keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certFile, keyFile := filepath.Join(directory, label+".pem"), filepath.Join(directory, label+".key")
		if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}), 0600); err != nil {
			t.Fatal(err)
		}
		pair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			t.Fatal(err)
		}
		return certFile, keyFile, pair
	}
	_, _, server := issue("server", 2, serverNames, x509.ExtKeyUsageServerAuth)
	certFile, keyFile, _ := issue("governance", 3, clientNames, x509.ExtKeyUsageClientAuth)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("MODELDEV_RESPONSE_PREFLIGHT: generated CA invalid")
	}
	return ModelDevClientConfig{CAFile: caFile, CertFile: certFile, KeyFile: keyFile, Timeout: time.Second}, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{server}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}
}
