//go:build modeldev_contract

package testutil

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
)

// Each selected consumer suite reads once and owns one provider stop signal.
// The runner supplies a fresh provider for each suite, including each package.
type ModelDevContractFixture struct {
	Schema  string `json:"schema"`
	Address string `json:"address"`
	TLS     struct {
		CAFile   string `json:"ca_file"`
		CertFile string `json:"cert_file"`
		KeyFile  string `json:"key_file"`
	} `json:"tls"`
	Scope struct {
		ResourceTenantID string `json:"resource_tenant_id"`
		Actor            string `json:"actor"`
	} `json:"scope"`
	Intent                    cpup01.Intent      `json:"intent"`
	IdentityRef               cpup01.IdentityRef `json:"identity_ref"`
	ComputeSelectionCanonical string             `json:"compute_selection_canonical"`
	SelectionDigest           string             `json:"selection_digest"`
	AcceptedAt                time.Time          `json:"accepted_at"`
}

func ReadModelDevContractFixture(t *testing.T) ModelDevContractFixture {
	t.Helper()
	file := os.Getenv("ANI_MODELDEV_CONTRACT_HANDSHAKE")
	if !filepath.IsAbs(file) || filepath.Base(file) != "handshake.json" {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: explicit private handshake required; behavior NOT_RUN")
	}
	directory := filepath.Dir(file)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: private provider directory unavailable; behavior NOT_RUN")
	}
	// Cleanup requests provider shutdown even when the intentional product RED
	// calls Fatal. No credential or handshake contents enter logs or artifacts.
	t.Cleanup(func() {
		stop, err := os.OpenFile(filepath.Join(directory, "stop"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Error("MODELDEV_CLIENT_CLEANUP: cannot signal the exact provider fixture")
			return
		}
		if err := stop.Close(); err != nil {
			t.Error("MODELDEV_CLIENT_CLEANUP: cannot close provider stop signal")
		}
	})
	info, err = os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: bounded private handshake unavailable; behavior NOT_RUN")
	}
	raw, err := os.ReadFile(file)
	if err != nil || len(raw) > 16384 {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: handshake read failed; behavior NOT_RUN")
	}
	var fixture ModelDevContractFixture
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&fixture) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: handshake schema invalid; behavior NOT_RUN")
	}
	host, port, err := net.SplitHostPort(fixture.Address)
	portNumber, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host != "127.0.0.1" || portErr != nil || portNumber == 0 ||
		fixture.Schema != "ani.modeldev.governance-admission-fixture.v2.1" ||
		fixture.TLS.CAFile != filepath.Join(directory, "ca.pem") || fixture.TLS.CertFile != filepath.Join(directory, "governance.pem") || fixture.TLS.KeyFile != filepath.Join(directory, "governance.key") {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: handshake connection scope invalid; behavior NOT_RUN")
	}
	seed := conformance.SnapshotV1()
	wantIntent := ModelDevContractIntent()
	wantIntentCanonical, hash, err := cpup01.CanonicalIntent(wantIntent)
	actualIntentCanonical, _, actualErr := cpup01.CanonicalIntent(fixture.Intent)
	identity := cpup01.IdentityRef{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42", OperationID: "88888888-8888-4888-8888-888888888888", ExecutionID: "99999999-9999-4999-8999-999999999999", IntentHash: hash}
	if err != nil || actualErr != nil || !bytes.Equal(actualIntentCanonical, wantIntentCanonical) || fixture.IdentityRef != identity || fixture.Scope.ResourceTenantID != identity.TenantID || fixture.Scope.Actor != identity.Actor || fixture.ComputeSelectionCanonical != seed.ComputeOrigin.SelectionJSON || fixture.SelectionDigest != seed.ComputeOrigin.SelectionDigest || !fixture.AcceptedAt.Equal(time.Date(2026, 9, 30, 12, 3, 0, 123000, time.UTC)) {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: handshake differs from fixed authored fixture facts")
	}
	_ = ModelDevContractSelection(t, fixture)
	return fixture
}

func ModelDevContractIntent() cpup01.Intent {
	seed := conformance.SnapshotV1()
	rate := "0.0200"
	return cpup01.Intent{Name: "configured-admission", Kind: "GENERAL_TRAINING", ImplementationID: "builtin.mlp", ComputePresetID: seed.ComputeOrigin.ComputePresetID, DatasetVersionID: seed.Input.InputVersionID, MLP: &cpup01.MLPParameters{LearningRate: &rate}}
}
func ModelDevContractSelection(t *testing.T, f ModelDevContractFixture) *compute.ComputeSelection {
	t.Helper()
	var selection compute.ComputeSelection
	if protojson.Unmarshal([]byte(f.ComputeSelectionCanonical), &selection) != nil {
		t.Fatal("invalid official compute fixture")
	}
	selection.SelectionDigest = f.SelectionDigest
	canonical, err := compute.CanonicalSelection(&selection)
	if err != nil || compute.ValidateSelection(&selection) != nil || string(canonical) != f.ComputeSelectionCanonical {
		t.Fatal("invalid official compute fixture")
	}
	return &selection
}

func ExpectedModelDevContractSnapshot(t *testing.T) ([]byte, string) {
	_, hash, err := cpup01.CanonicalIntent(ModelDevContractIntent())
	if err != nil {
		t.Fatal(err)
	}
	return ExpectedModelDevContractSnapshotAt(t, cpup01.IdentityRef{TenantID: "11111111-2222-4333-8444-555555555555", Actor: "governance:user:42", OperationID: "88888888-8888-4888-8888-888888888888", ExecutionID: "99999999-9999-4999-8999-999999999999", IntentHash: hash}, time.Date(2026, 9, 30, 12, 3, 0, 123000, time.UTC))
}
func ExpectedModelDevContractSnapshotAt(t *testing.T, identity cpup01.IdentityRef, acceptedAt time.Time) ([]byte, string) {
	t.Helper()
	// Complete expected program/timing/Runtime are authored here, independently
	// of either provider response and of ModelDev's runtime compiler.
	want := conformance.SnapshotV1()
	want.IdentityRef = identity
	want.Program.ResolvedParameters = []cpup01.Parameter{{Name: "batch_size", Type: "INTEGER", Value: "64"}, {Name: "epochs", Type: "INTEGER", Value: "3"}, {Name: "learning_rate", Type: "DECIMAL", Value: "0.02"}, {Name: "seed", Type: "INTEGER", Value: "42"}}
	want.Program.ResolvedArgs = []string{"--data", "/inputs/data.csv", "--output", "/outputs", "--learning-rate", "0.02", "--epochs", "3", "--batch-size", "64", "--seed", "42"}
	want.Timing = cpup01.Timing{AcceptedAt: acceptedAt, ExecutionTimeoutSeconds: 3600, ExecutionTimeoutMaxSeconds: 7200, StepTimeoutSeconds: 900, ExecutionDeadlineAt: acceptedAt.Add(time.Hour)}
	want.DeadlineAt = want.Timing.ExecutionDeadlineAt
	want.RuntimePlan = independentModelDevContractRuntime(t)
	want.RuntimePlan.Name = cpup01.RuntimeName(want.Environment.NamespaceUID, identity.ExecutionID)
	want.RuntimePlan.ExpectedSpec = []byte(strings.ReplaceAll(string(want.RuntimePlan.ExpectedSpec), "ani-kfp-workspace-99999999-9999-4999-8999-999999999999", "ani-kfp-workspace-"+identity.ExecutionID))
	runtimeDigest := sha256.Sum256(want.RuntimePlan.ExpectedSpec)
	want.RuntimePlan.ExpectedSpecSHA256 = hex.EncodeToString(runtimeDigest[:])
	canonical, err := want.Canonical()
	if err != nil {
		t.Fatal("MODELDEV_CLIENT_PREFLIGHT: independent complete snapshot invalid", err)
	}
	digest := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(digest[:])
}

func independentModelDevContractRuntime(t *testing.T) cpup01.RuntimePlan {
	t.Helper()
	const literal = `{"mlPolicy":{"numNodes":1},"podGroupPolicy":{"volcano":{}},"template":{"metadata":{"annotations":{"scheduling.volcano.sh/queue-name":"default"}},"spec":{"failurePolicy":{"maxRestarts":0,"restartStrategy":"Recreate"},"replicatedJobs":[{"groupName":"default","name":"node","replicas":1,"template":{"metadata":{"labels":{"trainer.kubeflow.org/trainjob-ancestor-step":"trainer"}},"spec":{"activeDeadlineSeconds":900,"backoffLimit":0,"completionMode":"Indexed","completions":1,"parallelism":1,"template":{"metadata":{"finalizers":["modeldev.ani.io/training-exit-evidence"]},"spec":{"automountServiceAccountToken":false,"containers":[{"args":["--data","/inputs/data.csv","--output","/outputs","--learning-rate","0.02","--epochs","3","--batch-size","64","--seed","42"],"command":["python","/opt/mlp/train.py"],"image":"registry.example.test/cpu/mlp@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","imagePullPolicy":"IfNotPresent","name":"node","resources":{"limits":{"cpu":"1","memory":"1Gi"},"requests":{"cpu":"1","memory":"1Gi"}},"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true},"volumeMounts":[{"mountPath":"/inputs","name":"input","readOnly":true,"subPath":"inputs"},{"mountPath":"/outputs","name":"output","readOnly":false,"subPath":"training"},{"mountPath":"/tmp","name":"tmp","readOnly":false}]}],"restartPolicy":"Never","schedulerName":"volcano","securityContext":{"fsGroup":10001,"runAsGroup":10001,"runAsNonRoot":true,"runAsUser":10001,"seccompProfile":{"type":"RuntimeDefault"}},"serviceAccountName":"cpu-training","terminationGracePeriodSeconds":5,"volumes":[{"name":"input","persistentVolumeClaim":{"claimName":"ani-kfp-workspace-99999999-9999-4999-8999-999999999999"}},{"name":"output","persistentVolumeClaim":{"claimName":"ani-kfp-workspace-99999999-9999-4999-8999-999999999999"}},{"emptyDir":{"sizeLimit":"4Gi"},"name":"tmp"}]}}}}}]}}}`
	var object any
	if err := json.Unmarshal([]byte(literal), &object); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return cpup01.RuntimePlan{GeneratorVersion: "ani.modeldev.runtime.v1", APIGroup: "trainer.kubeflow.org", Kind: "TrainingRuntime", Namespace: "cpu-contract-fixture", Name: cpup01.RuntimeName("33333333-3333-4333-8333-333333333333", "99999999-9999-4999-8999-999999999999"), ExpectedSpec: raw, ExpectedSpecSHA256: hex.EncodeToString(digest[:]), DefaultingRules: "trainer-2.1.0-jobset-explicit-v1", TargetJobs: []string{"node"}}
}
