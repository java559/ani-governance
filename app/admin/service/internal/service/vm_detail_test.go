package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestVMDetailProjectsFrozenConfigurationAndSafeRuntime(t *testing.T) {
	spec := vmFixtureSpec(t)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	spec.Intent.GuestInitialization = &vm.GuestInitialization{Method: "CLOUD_INIT_NOCLOUD", Username: "initial-user", SshPublicKey: string(ssh.MarshalAuthorizedKey(key)), PasswordSet: true, CredentialIntentDigest: strings.Repeat("secret", 10)}
	spec.Media.Name = "Creation image"
	spec.Media.Namespace = "private-native-namespace"
	spec.Media.PvcName = "private-source-pvc"
	spec.Compute.Name = "Creation preset"
	zero := uint64(0)
	owner := &vm.VirtualMachine{Id: "vm-id", FrozenSpec: spec, CreatedAt: "2026-10-30T00:00:00Z", Runtime: &vm.VMRuntime{State: "AVAILABLE", Hostname: "guest-a", FilesystemsState: "AVAILABLE", Filesystems: []*vm.GuestFilesystem{{MountPoint: "/", UsedBytes: &zero}}}, Relations: &vm.VMRelations{Vpc: &vm.ResourceReference{Id: spec.Network.VpcId, Name: "Current VPC", State: "RESOLVED"}}}
	out := new(view.VirtualMachine)
	require.NoError(t, vmWire(owner, out))
	vmSummary(spec, out)
	require.Equal(t, "Creation preset", out.PresetName)
	require.Equal(t, "Creation image", out.SourceMedia.Name)
	require.Equal(t, spec.Media.Firmware, out.Hardware.Firmware)
	require.Equal(t, ssh.FingerprintSHA256(key), out.Initialization.SshKeyFingerprint)
	require.Equal(t, "Current VPC", out.Relations.Vpc.Name)
	require.Equal(t, "guest-a", out.Runtime.Hostname)
	require.NotNil(t, out.Runtime.Filesystems[0].UsedBytes)
	require.Equal(t, uint64(0), out.Runtime.Filesystems[0].GetUsedBytes())
	require.Equal(t, "2026-10-30T00:00:00Z", out.CreatedAt)
	raw, err := protojson.Marshal(out)
	require.NoError(t, err)
	for _, secret := range []string{"private-source-pvc", "private-native-namespace", spec.Intent.GuestInitialization.SshPublicKey, "credentialIntentDigest", "passwordSet", "frozenSpec"} {
		require.NotContains(t, string(raw), secret)
	}
	snapshot := new(view.VMSnapshot)
	require.NoError(t, vmWire(&vm.Snapshot{SnapshotId: "snapshot-id", Name: "before-upgrade", CreatedAt: "2026-10-30T01:00:00Z"}, snapshot))
	require.Equal(t, "before-upgrade", snapshot.Name)
	require.Equal(t, "2026-10-30T01:00:00Z", snapshot.CreatedAt)
}
