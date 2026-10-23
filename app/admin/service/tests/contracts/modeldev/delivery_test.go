//go:build modeldev_pg && modeldev_contract

package modeldev_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	entgo "entgo.io/ent"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	modeldevv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	trainingv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/training/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	contractpb "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestModelDevWorkerDeliversFrozenAcceptanceAfterRestart(t *testing.T) {
	fixture := readModelDevDeliveryFixture(t)
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 50*time.Second)
	defer cancel()
	original := fixture.Frozen.envelope(fixture.Target)
	control := fixture.Frozen.envelope(fixture.Control)
	wantIntent, wantSnapshot, err := original.CanonicalPayloads()
	require.NoError(t, err)
	replayModelDevDeliveryControl(t, ctx, fixture, control)

	runtimeDSN, fixtureDSN := modelDevDeliveryDatabaseSettings(t)
	writer, _ := openModelDevDeliveryPG(t, fixtureDSN)
	initial, closeInitial := openModelDevDeliveryPG(t, runtimeDSN)
	assertModelDevDeliveryDatabaseRoles(t, ctx, initial, writer)
	owner, err := writer.Client().Tenant.Create().SetName("modeldev delivery fixture").SetCode("delivery-" + uuid.NewString()).SetResourceTenantID(original.TenantID).Save(ctx)
	require.NoError(t, err, "MODELDEV_DELIVERY_PREFLIGHT: fresh fixture tenant required")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
		defer stop()
		_, err := writer.Client().ModelDevAcceptance.Delete().Where(modeldevacceptance.TenantIDEQ(owner.ID)).Exec(cleanup)
		require.NoError(t, err)

		require.NoError(t, writer.Client().Tenant.DeleteOneID(owner.ID).Exec(cleanup))
	})
	scope := data.ModelDevAdmissionScope{TenantID: owner.ID, ResourceTenantID: original.TenantID, Actor: original.Actor, Action: data.ModelDevCreateAction, IdempotencyKey: uuid.NewString()}
	accepted, replayed, err := data.NewModelDevAcceptanceRepo(initial).AcceptFrozen(ctx, scope, data.ModelDevFrozenCandidate{
		OperationID: original.OperationID, ExecutionID: original.ExecutionID, Intent: original.Intent, Snapshot: original.Snapshot, AcceptedAt: original.AcceptedAt,
	})
	require.NoError(t, err, "MODELDEV_DELIVERY_PREFLIGHT: original acceptance must commit before worker construction")
	require.False(t, replayed)
	require.Equal(t, "QUEUED", accepted.DispatchState)
	require.Equal(t, original.OperationID, accepted.OperationID)
	require.Equal(t, original.ExecutionID, accepted.ExecutionID)
	closeInitial()
	require.Error(t, initial.DB().PingContext(ctx), "the accepting connection must actually be closed before reconstruction")

	workerConnection, closeWorkerConnection := openModelDevDeliveryPG(t, runtimeDSN)
	observer, _ := openModelDevDeliveryPG(t, runtimeDSN)
	assertModelDevDeliveryOriginal(t, ctx, observer, scope, original, wantIntent, wantSnapshot, "QUEUED")
	client, closeClient, err := data.NewModelDevClient(data.ModelDevClientConfig{
		Address: fixture.Address, CAFile: fixture.TLS.CAFile, CertFile: fixture.TLS.CertFile, KeyFile: fixture.TLS.KeyFile, Timeout: 3 * time.Second,
	})
	require.NoError(t, err, "MODELDEV_DELIVERY_PREFLIGHT: real fixed-identity client must construct")
	t.Cleanup(closeClient)
	// Only the ACKED mutation is faulted. Claim must first commit normally and
	// the actual mTLS owner must return a validated ACK before this hook runs.
	// This is an Ent commit-boundary substitute, not a PostgreSQL server COMMIT
	// failure. The ACK SQL update and its rollback still use the real database.
	failedACK, commitFaults := rejectModelDevDeliveryACKCommit(workerConnection)
	worker := service.NewModelDevDispatchWorker(testutil.NewBootstrapContext(nil), data.NewModelDevAcceptanceRepo(workerConnection), client)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		require.NoError(t, worker.Stop(cleanup))
	})
	t.Log("MODELDEV_DELIVERY_PREFLIGHT PASS: real generated mTLS control ACK, runtime PostgreSQL role, committed frozen QUEUED record, closed accepting connection and reconstructed worker")
	require.NoError(t, worker.Start(ctx))
	firstACKDeadline := time.NewTimer(8 * time.Second)
	defer firstACKDeadline.Stop()
	var rejectedReceipt []byte
	select {
	case rejectedReceipt = <-failedACK:
	case <-ctx.Done():
		t.Fatal("MODELDEV_DELIVERY_RECOVERY: context ended before the actual ACK commit boundary")
	case <-firstACKDeadline.C:
		t.Fatal("MODELDEV_DELIVERY_RECOVERY: no ACKED commit-boundary fault was observed")
	}
	assertModelDevDeliveryReceipt(t, rejectedReceipt, original, false)
	stopContext, stop := context.WithTimeout(context.Background(), 5*time.Second)
	require.NoError(t, worker.Stop(stopContext))
	stop()
	require.Equal(t, int64(1), commitFaults.Load(), "the controlled failure must hit exactly the first ACK commit")
	assertModelDevDeliveryOriginal(t, ctx, observer, scope, original, wantIntent, wantSnapshot, "DISPATCHING")
	var firstGeneration, firstAttempt int64
	var firstLeaseUntil time.Time
	var pendingReceipt []byte
	require.NoError(t, observer.DB().QueryRowContext(ctx, `SELECT lease_generation,attempt_count,lease_until,owner_receipt_canonical
		FROM sys_modeldev_acceptances WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3`,
		scope.TenantID, scope.ResourceTenantID, original.OperationID).Scan(&firstGeneration, &firstAttempt, &firstLeaseUntil, &pendingReceipt))
	require.Equal(t, int64(1), firstGeneration)
	require.Equal(t, int64(1), firstAttempt)
	require.Nil(t, pendingReceipt, "the real ACK update must have rolled back")
	requestModelDevDeliveryOwnerObservation(t, ctx, fixture, original)
	t.Log("MODELDEV_DELIVERY_RECOVERY GAP: provider independently confirmed durable original; Governance ACK commit hook hit once, rollback left DISPATCHING with no receipt")
	closeWorkerConnection()
	closeClient()
	require.Error(t, workerConnection.DB().PingContext(ctx), "the failed worker's database connection must close before reconstruction")

	// Recreate both outbound resources and the worker. No fixture UPDATE moves
	// the lease; the restarted worker must wait for the real 15-second expiry.
	recoveryConnection, _ := openModelDevDeliveryPG(t, runtimeDSN)
	recoveryClient, closeRecoveryClient, err := data.NewModelDevClient(data.ModelDevClientConfig{
		Address: fixture.Address, CAFile: fixture.TLS.CAFile, CertFile: fixture.TLS.CertFile, KeyFile: fixture.TLS.KeyFile, Timeout: 3 * time.Second,
	})
	require.NoError(t, err)
	t.Cleanup(closeRecoveryClient)
	recoveryWorker := service.NewModelDevDispatchWorker(testutil.NewBootstrapContext(nil), data.NewModelDevAcceptanceRepo(recoveryConnection), recoveryClient)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		require.NoError(t, recoveryWorker.Stop(cleanup))
	})
	var leaseStillLive bool
	require.NoError(t, observer.DB().QueryRowContext(ctx, `SELECT statement_timestamp() < $1::timestamptz`, firstLeaseUntil).Scan(&leaseStillLive))
	require.True(t, leaseStillLive, "recovery must start while the committed first lease is still live")
	require.NoError(t, recoveryWorker.Start(ctx))

	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	var receiptBytes []byte
	var recoveredGeneration, recoveredAttempt int64
	var leaseExpired bool
	for {
		var state string
		err := observer.DB().QueryRowContext(ctx, `SELECT dispatch_state,owner_receipt_canonical,lease_generation,attempt_count,
			statement_timestamp() >= $4::timestamptz FROM sys_modeldev_acceptances
			WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3`,
			scope.TenantID, scope.ResourceTenantID, original.OperationID, firstLeaseUntil).
			Scan(&state, &receiptBytes, &recoveredGeneration, &recoveredAttempt, &leaseExpired)
		if err != nil {
			t.Fatal("MODELDEV_DELIVERY_RECOVERY: independent durable receipt read failed")
		}
		if state == "ACKED" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("MODELDEV_DELIVERY_BEHAVIOR: context ended before durable ACK")
		case <-deadline.C:
			t.Fatal("MODELDEV_DELIVERY_RECOVERY: worker did not persist replay ACK within twenty-five seconds")
		case <-poll.C:
		}
	}
	require.True(t, leaseExpired, "the second claim must not bypass the live first lease")
	require.Equal(t, int64(2), recoveredGeneration)
	require.Equal(t, int64(2), recoveredAttempt)
	require.Equal(t, int64(1), commitFaults.Load(), "the stopped first worker must not resume sending")
	assertModelDevDeliveryReceipt(t, receiptBytes, original, true)
	assertModelDevDeliveryOriginal(t, ctx, observer, scope, original, wantIntent, wantSnapshot, "ACKED")
	stopContext, stop = context.WithTimeout(context.Background(), 5*time.Second)
	require.NoError(t, recoveryWorker.Stop(stopContext))
	stop()
	reconnected, _ := openModelDevDeliveryPG(t, runtimeDSN)
	var afterRestart []byte
	require.NoError(t, reconnected.DB().QueryRowContext(ctx, `SELECT owner_receipt_canonical FROM sys_modeldev_acceptances WHERE tenant_id=$1 AND resource_tenant_id=$2 AND operation_id=$3 AND dispatch_state='ACKED'`, scope.TenantID, scope.ResourceTenantID, original.OperationID).Scan(&afterRestart))
	require.Equal(t, receiptBytes, afterRestart, "a new connection must recover the complete same durable owner receipt")
	assertModelDevDeliveryReceipt(t, afterRestart, original, true)
	t.Log("MODELDEV_DELIVERY_BEHAVIOR PASS: ACK commit hook rolled back; reconstructed worker replayed the original after real lease expiry; owner revision=1, Governance attempts=2, durable replay ACK retained after reconnect")
}

func rejectModelDevDeliveryACKCommit(client *entCrud.EntClient[*ent.Client]) (<-chan []byte, *atomic.Int64) {
	failed := make(chan []byte, 1)
	calls := &atomic.Int64{}
	client.Client().ModelDevAcceptance.Use(func(next entgo.Mutator) entgo.Mutator {
		return entgo.MutateFunc(func(ctx context.Context, mutation entgo.Mutation) (entgo.Value, error) {
			m, ok := mutation.(*ent.ModelDevAcceptanceMutation)
			if !ok {
				return nil, errors.New("unexpected delivery mutation")
			}
			state, changed := m.DispatchState()
			if !changed || state != modeldevacceptance.DispatchStateACKED {
				return next.Mutate(ctx, mutation)
			}
			raw, present := m.OwnerReceiptCanonical()
			if !present || len(raw) == 0 {
				return nil, errors.New("ACK mutation has no receipt")
			}
			tx, err := m.Tx()
			if err != nil {
				return nil, err
			}
			raw = bytes.Clone(raw)
			tx.OnCommit(func(ent.Committer) ent.Committer {
				return ent.CommitFunc(func(context.Context, *ent.Tx) error {
					calls.Add(1)
					select {
					case failed <- raw:
					default:
					}
					return errors.New("controlled ACK commit boundary failure")
				})
			})
			return next.Mutate(ctx, mutation)
		})
	})
	return failed, calls
}

// This one-purpose file barrier asks the already running real provider to
// inspect its own PG state. No target command is sent by this helper.
func requestModelDevDeliveryOwnerObservation(t *testing.T, ctx context.Context, fixture modelDevDeliveryFixture, original cpup01.AdmissionEnvelope) {
	t.Helper()
	directory := filepath.Dir(fixture.TLS.CAFile)
	request, err := os.OpenFile(filepath.Join(directory, "observe-target"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, err)
	require.NoError(t, request.Close())
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	path := filepath.Join(directory, "target-observed.json")
	for {
		info, err := os.Lstat(path)
		if err == nil {
			require.True(t, info.Mode().IsRegular())
			require.Equal(t, os.FileMode(0600), info.Mode().Perm())
			require.LessOrEqual(t, info.Size(), int64(1024))
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			require.LessOrEqual(t, len(raw), 1024)
			var observed struct {
				Schema            string `json:"schema"`
				OperationID       string `json:"operation_id"`
				ExecutionID       string `json:"execution_id"`
				ExecutionSpecHash string `json:"execution_spec_hash"`
				Revision          uint64 `json:"revision"`
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			require.NoError(t, decoder.Decode(&observed))
			require.Equal(t, io.EOF, decoder.Decode(new(any)))
			canonical, err := json.Marshal(observed)
			require.NoError(t, err)
			require.Equal(t, canonical, raw)
			require.Equal(t, "ani.cpu-p01.delivery-observation.v1", observed.Schema)
			require.Equal(t, original.OperationID, observed.OperationID)
			require.Equal(t, original.ExecutionID, observed.ExecutionID)
			require.Equal(t, original.SpecHash, observed.ExecutionSpecHash)
			require.Equal(t, uint64(1), observed.Revision)
			return
		}
		require.True(t, os.IsNotExist(err), "provider observation path failed before publication")
		select {
		case <-ctx.Done():
			t.Fatal("MODELDEV_DELIVERY_RECOVERY: context ended before independent owner observation")
		case <-deadline.C:
			t.Fatal("MODELDEV_DELIVERY_RECOVERY: owner did not publish its independent observation")
		case <-tick.C:
		}
	}
}

type modelDevDeliveryIdentity struct {
	OperationID string `json:"operation_id"`
	ExecutionID string `json:"execution_id"`
}

type modelDevDeliveryFrozen struct {
	ResourceTenantID  string          `json:"resource_tenant_id"`
	Actor             string          `json:"actor"`
	Intent            cpup01.Intent   `json:"intent"`
	Snapshot          cpup01.Snapshot `json:"snapshot"`
	IntentHash        string          `json:"intent_hash"`
	ExecutionSpecHash string          `json:"execution_spec_hash"`
	AcceptedAt        time.Time       `json:"accepted_at"`
}

func (f modelDevDeliveryFrozen) envelope(identity modelDevDeliveryIdentity) cpup01.AdmissionEnvelope {
	snapshot := f.Snapshot
	snapshot.IdentityRef = cpup01.IdentityRef{TenantID: f.ResourceTenantID, Actor: f.Actor, OperationID: identity.OperationID, ExecutionID: identity.ExecutionID, IntentHash: f.IntentHash}
	snapshot.RuntimePlan.ExpectedSpec = []byte(strings.ReplaceAll(string(snapshot.RuntimePlan.ExpectedSpec), "ani-kfp-workspace-99999999-9999-4999-8999-999999999999", "ani-kfp-workspace-"+identity.ExecutionID))
	digest := sha256.Sum256(snapshot.RuntimePlan.ExpectedSpec)
	snapshot.RuntimePlan.ExpectedSpecSHA256 = hex.EncodeToString(digest[:])
	snapshot.RuntimePlan.Name = cpup01.RuntimeName(snapshot.Environment.NamespaceUID, identity.ExecutionID)
	hash, _ := snapshot.Digest()
	return cpup01.AdmissionEnvelope{TenantID: f.ResourceTenantID, Actor: f.Actor, OperationID: identity.OperationID, ExecutionID: identity.ExecutionID, Intent: f.Intent, IntentHash: f.IntentHash, Snapshot: snapshot, SpecHash: hash, AcceptedAt: f.AcceptedAt}
}

type modelDevDeliveryFixture struct {
	Schema  string `json:"schema"`
	Address string `json:"address"`
	TLS     struct {
		CAFile   string `json:"ca_file"`
		CertFile string `json:"cert_file"`
		KeyFile  string `json:"key_file"`
	} `json:"tls"`
	Frozen  modelDevDeliveryFrozen   `json:"frozen"`
	Target  modelDevDeliveryIdentity `json:"target"`
	Control modelDevDeliveryIdentity `json:"control"`
}

func readModelDevDeliveryFixture(t *testing.T) modelDevDeliveryFixture {
	t.Helper()
	file := os.Getenv("ANI_MODELDEV_CONTRACT_HANDSHAKE")
	if !filepath.IsAbs(file) || filepath.Base(file) != "handshake.json" {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: explicit private handshake required")
	}
	directory := filepath.Dir(file)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: private provider directory required")
	}
	t.Cleanup(func() {
		stop, err := os.OpenFile(filepath.Join(directory, "stop"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Error("MODELDEV_DELIVERY_CLEANUP: exact provider stop signal failed")
			return
		}
		if err := stop.Close(); err != nil {
			t.Error("MODELDEV_DELIVERY_CLEANUP: stop signal close failed")
		}
	})
	info, err = os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: bounded private handshake required")
	}
	raw, err := os.ReadFile(file)
	if err != nil || len(raw) > 16384 {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: handshake read failed")
	}
	var fixture modelDevDeliveryFixture
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fixture) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: strict handshake decode failed")
	}
	host, port, addressErr := net.SplitHostPort(fixture.Address)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if fixture.Schema != "ani.modeldev.governance-delivery-fixture.v2.1" || addressErr != nil || host != "127.0.0.1" || portErr != nil || portNumber == 0 ||
		fixture.TLS.CAFile != filepath.Join(directory, "ca.pem") || fixture.TLS.CertFile != filepath.Join(directory, "governance.pem") || fixture.TLS.KeyFile != filepath.Join(directory, "governance.key") {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: handshake connection scope invalid")
	}
	canonicalHandshake, err := json.Marshal(fixture)
	if err != nil || !bytes.Equal(raw, canonicalHandshake) {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: handshake is not a complete canonical fixture")
	}
	for _, identity := range []modelDevDeliveryIdentity{fixture.Target, fixture.Control} {
		operation, operationErr := uuid.Parse(identity.OperationID)
		execution, executionErr := uuid.Parse(identity.ExecutionID)
		if operationErr != nil || executionErr != nil || operation == uuid.Nil || execution == uuid.Nil || operation.String() != identity.OperationID || execution.String() != identity.ExecutionID || operation == execution {
			t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: invalid fixture identities")
		}
	}
	if fixture.Target.OperationID == fixture.Control.OperationID || fixture.Target.ExecutionID == fixture.Control.ExecutionID {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: target and control must differ")
	}
	wantIntent, wantIntentHash, err := cpup01.CanonicalIntent(testutil.ModelDevContractIntent())
	require.NoError(t, err)
	if fixture.Frozen.ResourceTenantID != "11111111-2222-4333-8444-555555555555" || fixture.Frozen.Actor != "governance:user:42" || fixture.Frozen.AcceptedAt.IsZero() || fixture.Frozen.AcceptedAt.Nanosecond()%1000 != 0 {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: invalid fixed original scope/time")
	}
	baseIdentity := cpup01.IdentityRef{TenantID: fixture.Frozen.ResourceTenantID, Actor: fixture.Frozen.Actor, OperationID: "88888888-8888-4888-8888-888888888888", ExecutionID: "99999999-9999-4999-8999-999999999999", IntentHash: wantIntentHash}
	baseBytes, baseHash := testutil.ExpectedModelDevContractSnapshotAt(t, baseIdentity, fixture.Frozen.AcceptedAt)
	actualBase, err := fixture.Frozen.Snapshot.Canonical()
	require.NoError(t, err)
	require.Equal(t, baseBytes, actualBase)
	require.Equal(t, baseHash, fixture.Frozen.ExecutionSpecHash)
	for _, identity := range []modelDevDeliveryIdentity{fixture.Target, fixture.Control} {
		envelope := fixture.Frozen.envelope(identity)
		expectedIdentity := baseIdentity
		expectedIdentity.OperationID = identity.OperationID
		expectedIdentity.ExecutionID = identity.ExecutionID
		wantBytes, wantHash := testutil.ExpectedModelDevContractSnapshotAt(t, expectedIdentity, fixture.Frozen.AcceptedAt)
		actualIntent, actualSnapshot, e := envelope.CanonicalPayloads()
		if e != nil || !bytes.Equal(actualIntent, wantIntent) || !bytes.Equal(actualSnapshot, wantBytes) || fixture.Frozen.IntentHash != wantIntentHash || envelope.SpecHash != wantHash {
			t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: frozen command differs from independent complete fixture")
		}
	}
	return fixture
}

func replayModelDevDeliveryControl(t *testing.T, ctx context.Context, fixture modelDevDeliveryFixture, original cpup01.AdmissionEnvelope) {
	t.Helper()
	ca, err := os.ReadFile(fixture.TLS.CAFile)
	if err != nil {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: synthetic CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: synthetic CA invalid")
	}
	certificate, err := tls.LoadX509KeyPair(fixture.TLS.CertFile, fixture.TLS.KeyFile)
	if err != nil {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: synthetic client identity invalid")
	}
	connection, err := grpc.NewClient(fixture.Address, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}, ServerName: "ani-modeldev-service"})))
	if err != nil {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: generated channel configuration failed")
	}
	defer connection.Close()
	intent, err := contractpb.EncodeIntent(original.Intent)
	require.NoError(t, err)
	snapshot, err := contractpb.EncodeSnapshot(original.Snapshot)
	require.NoError(t, err)
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", original.TenantID, "x-ani-actor", original.Actor, "x-ani-request-id", uuid.NewString()))
	identity := &trainingv1.ExecutionIdentity{OperationId: original.OperationID, ExecutionId: original.ExecutionID, ExecutionSpecHash: original.SpecHash}
	reply, err := modeldevv1.NewModelDevCommandServiceClient(connection).AcceptExecution(call, &modeldevv1.AcceptExecutionRequest{Identity: identity, ResourceTenantId: original.TenantID, AdmittedActorId: original.Actor, IntentHash: original.IntentHash, Snapshot: snapshot, AcceptedAt: timestamppb.New(original.AcceptedAt), Intent: intent}, grpc.WaitForReady(true))
	if err != nil {
		t.Fatalf("MODELDEV_DELIVERY_PREFLIGHT: actual control replay failed (code=%s); worker behavior NOT_RUN", status.Code(err))
	}
	want := &modeldevv1.AcceptExecutionResponse{Identity: identity, Replayed: true, Revision: 1, States: &modeldevv1.ExecutionStates{ComputeState: modeldevv1.ComputeState_COMPUTE_STATE_ACCEPTED, DeliveryState: modeldevv1.DeliveryState_DELIVERY_STATE_PENDING, ResourceState: modeldevv1.ResourceState_RESOURCE_STATE_NOT_APPLICABLE, CloseState: modeldevv1.CloseState_CLOSE_STATE_OPEN}}
	require.True(t, proto.Equal(want, reply), "MODELDEV_DELIVERY_PREFLIGHT: complete control replay receipt must match independently known facts")
}

func modelDevDeliveryDatabaseSettings(t *testing.T) (string, string) {
	t.Helper()
	dsn, file := os.Getenv("ANI_TEST_DATABASE_DSN"), os.Getenv("ANI_MODELDEV_FIXTURE_DSN_FILE")
	if dsn == "" || os.Getenv("ANI_TEST_DATABASE_EXCLUSIVE") != "1" || !filepath.IsAbs(file) {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: explicit exclusive PG and private fixture identity required")
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: bounded private fixture identity required")
	}
	raw, err := os.ReadFile(file)
	if err != nil || len(raw) > 16384 {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: fixture identity read failed")
	}
	fixtureDSN := strings.TrimSpace(string(raw))
	runtimeConfig, runtimeErr := pgx.ParseConfig(dsn)
	fixtureConfig, fixtureErr := pgx.ParseConfig(fixtureDSN)
	if runtimeErr != nil || fixtureErr != nil || runtimeConfig.Host != fixtureConfig.Host || runtimeConfig.Port != fixtureConfig.Port || runtimeConfig.Database != fixtureConfig.Database || runtimeConfig.User == fixtureConfig.User {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: distinct fixture/runtime roles must use same task PG")
	}
	return dsn, fixtureDSN
}

func openModelDevDeliveryPG(t *testing.T, dsn string) (*entCrud.EntClient[*ent.Client], func()) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: PostgreSQL configuration failed")
	}
	driver := entsql.OpenDB("postgres", db)
	client := ent.NewClient(ent.Driver(driver))
	closed := false
	closeConnection := func() {
		if !closed {
			require.NoError(t, client.Close())
			closed = true
		}
	}
	t.Cleanup(closeConnection)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("MODELDEV_DELIVERY_PREFLIGHT: actual PostgreSQL connection failed")
	}
	return entCrud.NewEntClient(client, driver), closeConnection
}

func assertModelDevDeliveryDatabaseRoles(t *testing.T, ctx context.Context, runtime, writer *entCrud.EntClient[*ent.Client]) {
	t.Helper()
	var runtimeUser, fixtureUser, runtimeDatabase, fixtureDatabase, runtimeAddress, fixtureAddress string
	var runtimePort, fixturePort int
	identitySQL := `SELECT current_user,current_database(),coalesce(inet_server_addr()::text,''),coalesce(inet_server_port(),0)`
	require.NoError(t, runtime.DB().QueryRowContext(ctx, identitySQL).Scan(&runtimeUser, &runtimeDatabase, &runtimeAddress, &runtimePort))
	require.NoError(t, writer.DB().QueryRowContext(ctx, identitySQL).Scan(&fixtureUser, &fixtureDatabase, &fixtureAddress, &fixturePort))
	require.NotEqual(t, runtimeUser, fixtureUser)
	require.Equal(t, runtimeDatabase, fixtureDatabase)
	require.Equal(t, runtimeAddress, fixtureAddress)
	require.Equal(t, runtimePort, fixturePort)
	var superuser, bypassRLS, canCreate, canTemp bool
	require.NoError(t, runtime.DB().QueryRowContext(ctx, `SELECT rolsuper,rolbypassrls,has_database_privilege(current_database(),'CREATE'),has_database_privilege(current_database(),'TEMP') FROM pg_roles WHERE rolname=current_user`).Scan(&superuser, &bypassRLS, &canCreate, &canTemp))
	require.False(t, superuser)
	require.False(t, bypassRLS)
	require.False(t, canCreate)
	require.False(t, canTemp)
}

func assertModelDevDeliveryOriginal(t *testing.T, ctx context.Context, observer *entCrud.EntClient[*ent.Client], scope data.ModelDevAdmissionScope, original cpup01.AdmissionEnvelope, wantIntent, wantSnapshot []byte, state string) {
	t.Helper()
	row, err := observer.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID), modeldevacceptance.ResourceTenantIDEQ(scope.ResourceTenantID), modeldevacceptance.OperationIDEQ(original.OperationID)).Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, row.TenantID)
	require.Equal(t, scope.TenantID, *row.TenantID)
	require.Equal(t, original.TenantID, row.ResourceTenantID)
	require.Equal(t, original.Actor, row.Actor)
	require.Equal(t, scope.Action, row.Action)
	require.Equal(t, scope.IdempotencyKey, row.IdempotencyKey)
	require.Equal(t, original.OperationID, row.OperationID)
	require.Equal(t, original.ExecutionID, row.ExecutionID)
	require.Equal(t, wantIntent, row.IntentCanonical)
	require.Equal(t, wantSnapshot, row.SnapshotCanonical)
	require.Equal(t, original.IntentHash, row.IntentHash)
	require.Equal(t, original.SpecHash, row.ExecutionSpecHash)
	require.True(t, original.AcceptedAt.Equal(row.AcceptedAt))
	require.Equal(t, state, string(row.DispatchState))
	count, err := observer.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count, "the same frozen acceptance is the only queued or acknowledged command")
}

func assertModelDevDeliveryReceipt(t *testing.T, raw []byte, original cpup01.AdmissionEnvelope, replayed bool) {
	t.Helper()
	var receipt struct {
		Schema   string `json:"schema"`
		Identity struct {
			OperationID       string `json:"operation_id"`
			ExecutionID       string `json:"execution_id"`
			ExecutionSpecHash string `json:"execution_spec_hash"`
		} `json:"identity"`
		States struct {
			ComputeState  string `json:"compute_state"`
			DeliveryState string `json:"delivery_state"`
			ResourceState string `json:"resource_state"`
			CloseState    string `json:"close_state"`
		} `json:"states"`
		Revision string `json:"revision"`
		Replayed bool   `json:"replayed"`
	}
	require.LessOrEqual(t, len(raw), 4096)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&receipt))
	require.Equal(t, io.EOF, decoder.Decode(new(any)))
	canonical, err := json.Marshal(receipt)
	require.NoError(t, err)
	require.Equal(t, canonical, raw, "complete ordered receipt must reject omitted fields, duplicates, nulls and trailing whitespace")
	require.Equal(t, "ani.governance.modeldev-owner-receipt.v1", receipt.Schema)
	require.Equal(t, original.OperationID, receipt.Identity.OperationID)
	require.Equal(t, original.ExecutionID, receipt.Identity.ExecutionID)
	require.Equal(t, original.SpecHash, receipt.Identity.ExecutionSpecHash)
	require.Equal(t, "ACCEPTED", receipt.States.ComputeState)
	require.Equal(t, "PENDING", receipt.States.DeliveryState)
	require.Equal(t, "NOT_APPLICABLE", receipt.States.ResourceState)
	require.Equal(t, "OPEN", receipt.States.CloseState)
	revision, err := strconv.ParseUint(receipt.Revision, 10, 64)
	require.NoError(t, err)
	require.Equal(t, uint64(1), revision, "this provider permits no owner facts beyond the original admission")
	require.Equal(t, strconv.FormatUint(revision, 10), receipt.Revision)
	require.Equal(t, replayed, receipt.Replayed)
}
