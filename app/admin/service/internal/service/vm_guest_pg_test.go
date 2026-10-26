//go:build quota_pg

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/tests/testutil"
	appcrypto "go-wind-admin/pkg/crypto"
	"google.golang.org/protobuf/encoding/protojson"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type vmGuestRestartPeer struct {
	vmResolvePeer
	test                   *testing.T
	expectedPasswordDigest string
	creates                int
}

func (p *vmGuestRestartPeer) Create(_ context.Context, _, _ string, in *vm.AcceptCreateRequest) (*vm.Acceptance, error) {
	p.creates++
	require.NotNil(p.test, in.GuestCredentials)
	actual := sha256.Sum256([]byte(in.GuestCredentials.Password))
	require.Equal(p.test, p.expectedPasswordDigest, hex.EncodeToString(actual[:]))
	return &vm.Acceptance{OperationId: in.Context.OperationId, ResourceId: in.Context.ResourceId, Accepted: true}, nil
}

// This helper runs after the authenticated unmetered/fence flow has committed.
// Only the downstream Resource peer is controlled; a separate OS process must
// reopen real PG and load the same private master before it can dispatch.
func testVMGuestDurableRestartPostgres(t *testing.T, request context.Context) {
	keyFile := filepath.Join(t.TempDir(), "vm-master.hex")
	require.NoError(t, os.WriteFile(keyFile, []byte(strings.Repeat("ac", 32)), 0o600))
	t.Setenv("ANI_ACCESS_KEY_ENCRYPTION_KEY_FILE", keyFile)
	master, err := appcrypto.LoadAccessKeyCipherFromEnv()
	require.NoError(t, err)
	cipher, err := appcrypto.NewVMGuestCipher(master)
	require.NoError(t, err)
	client := testutil.NewQuotaPGClient(t)
	bctx := testutil.NewBootstrapContext(nil)
	spec := vmFixtureSpec(t)
	spec.Media.InitializationCapability = vm.CloudInitNoCloud
	spec.Media.InitializationVerified = true
	peer := &vmGuestRestartPeer{vmResolvePeer: vmResolvePeer{spec: spec}, test: t}
	binding := NewVMBinding(peer, data.NewVMAuthorizationRepo(client), spec.ClusterId, cipher)
	registry := NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(NewResourceDispatchAdapter(nil, binding)))
	ledger := data.NewQuotaLedgerRepo(bctx, client)
	resolver := data.NewTenantRepo(bctx, client)
	acceptance, err := NewGpuAcceptance(ledger, registry, resolver, nil, binding, nil)
	require.NoError(t, err)
	service := NewVMService(acceptance, binding, resolver, ledger, nil)
	password := "Fixture-" + uuid.NewString() + "!"
	in := &view.CreateVMRequest{Data: &view.CreateVMData{IdempotencyKey: uuid.NewString(), Name: spec.Intent.Name, PresetId: spec.Intent.PresetId, SourceKind: spec.Intent.SourceKind, MediaKey: spec.Intent.MediaKey, SystemDiskSizeGb: spec.Intent.SystemDiskSizeGb, StartAfterCreate: spec.Intent.StartAfterCreate, Network: &view.VMNetworkSelection{VpcId: spec.Intent.Network.VpcId, SubnetId: spec.Intent.Network.SubnetId}, GuestInitialization: &view.CreateGuestInitialization{Username: "ani", Password: password}}}
	result, err := service.CreateVM(request, in)
	require.NoError(t, err)
	principal, err := gpuPrincipal(request)
	require.NoError(t, err)
	stored, err := ledger.GetOperationForUser(request, principal.TenantID, result.OperationId)
	require.NoError(t, err)
	require.NotContains(t, stored.CanonicalRequest, password)
	command, err := commandcontract.DecodeVMCanonical([]byte(stored.CanonicalRequest))
	require.NoError(t, err)
	require.NotEmpty(t, command.GuestCredentialsCiphertext)
	frozen, err := vm.DecodeCreateSpec(command.CreateSpec)
	require.NoError(t, err)
	require.True(t, frozen.Intent.GuestInitialization.PasswordSet)
	require.NotContains(t, string(command.CreateSpec), `"password":`)
	replay, err := service.CreateVM(request, in)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, result.OperationId, replay.OperationId)
	require.Equal(t, 1, peer.calls)
	in.Data.GuestInitialization.Password = password + "x"
	_, err = service.CreateVM(request, in)
	require.Error(t, err, "same key cannot change a secret intent")
	pending, err := service.GetVMOperation(request, &view.GetOperationRequest{VmId: result.ResourceId, OperationId: result.OperationId})
	require.NoError(t, err)
	public, err := protojson.Marshal(pending)
	require.NoError(t, err)
	require.NotContains(t, string(public), password)
	require.NoError(t, client.Client().Close())
	hash := sha256.Sum256([]byte(password))
	child := exec.Command(os.Args[0], "-test.run=^TestVMPostgresGuestRestartChild$", "-test.timeout=90s")
	child.Env = append(os.Environ(), "ANI_VM_GUEST_CHILD_OPERATION="+result.OperationId, "ANI_VM_GUEST_CHILD_PASSWORD_DIGEST="+hex.EncodeToString(hash[:]))
	output, err := child.CombinedOutput()
	require.NoError(t, err, string(output))
	reopened := testutil.NewQuotaPGClient(t)
	reopenedLedger := data.NewQuotaLedgerRepo(bctx, reopened)
	after, err := reopenedLedger.GetOperationForUser(request, principal.TenantID, result.OperationId)
	require.NoError(t, err)
	require.Equal(t, "ACKED", string(after.DispatchState))
	require.Equal(t, stored.CanonicalRequest, after.CanonicalRequest, "dispatch/restart must preserve original encrypted acceptance")
	require.NotNil(t, after.AckJSON)
	require.NotContains(t, *after.AckJSON, password)
}

func TestVMPostgresGuestRestartChild(t *testing.T) {
	operation := os.Getenv("ANI_VM_GUEST_CHILD_OPERATION")
	if operation == "" {
		t.Skip("invoked only by durable restart parent")
	}
	client := testutil.NewQuotaPGClient(t)
	bctx := testutil.NewBootstrapContext(nil)
	master, err := appcrypto.LoadAccessKeyCipherFromEnv()
	require.NoError(t, err)
	cipher, err := appcrypto.NewVMGuestCipher(master)
	require.NoError(t, err)
	peer := &vmGuestRestartPeer{test: t, expectedPasswordDigest: os.Getenv("ANI_VM_GUEST_CHILD_PASSWORD_DIGEST")}
	binding := NewVMBinding(peer, nil, "", cipher)
	registry := NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(NewResourceDispatchAdapter(nil, binding)))
	ledger := data.NewQuotaLedgerRepo(bctx, client)
	claims, err := ledger.ClaimDispatchable(context.Background(), "vm-guest-child-process", time.Minute, 10)
	require.NoError(t, err)
	dispatched := false
	for i := range claims {
		if claims[i].OperationID == operation {
			NewQuotaDispatchWorker(bctx, ledger, registry).dispatchOne(context.Background(), &claims[i])
			dispatched = true
			break
		}
	}
	require.True(t, dispatched)
	require.Equal(t, 1, peer.creates)
}
