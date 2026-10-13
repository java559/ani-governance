//go:build quota_pg

package testutil

import (
	"bytes"
	"encoding/json"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type InstanceAcceleratorPeer struct {
	Address    string          `json:"address"`
	ServerName string          `json:"server_name"`
	CAFile     string          `json:"ca_file"`
	CertFile   string          `json:"gov_cert_file"`
	KeyFile    string          `json:"gov_key_file"`
	SeedFile   string          `json:"seed_file"`
	Request    *acc.GpuRequest `json:"-"`
}

func StartInstanceAcceleratorPeer(t *testing.T, tenant, actor string) *InstanceAcceleratorPeer {
	t.Helper()
	binary := os.Getenv("ACC_INSTANCE_TEST_BINARY")
	require.True(t, filepath.IsAbs(binary))
	root := t.TempDir()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, e)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	cmd := exec.Command(binary, "-test.run=^TestJointPersistentRuntime$", "-test.timeout=5m", "-test.v")
	cmd.Env = append(os.Environ(), "ACC_JOINT_ROOT="+root, "ACC_JOINT_ADDR="+address, "ACC_JOINT_TENANT="+tenant, "ACC_JOINT_TENANT_ACTOR="+actor)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		require.NoError(t, os.WriteFile(filepath.Join(root, "stop"), []byte("stop"), 0600))
		select {
		case e := <-done:
			require.NoError(t, e, "Acc fixture: %s", output.String())
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("Acc fixture cleanup deadline")
		}
	})
	peer := new(InstanceAcceleratorPeer)
	require.Eventually(t, func() bool {
		raw, e := os.ReadFile(filepath.Join(root, "ready.json"))
		return e == nil && json.Unmarshal(raw, peer) == nil
	}, 20*time.Second, 20*time.Millisecond, "Acc fixture readiness")
	raw, e := os.ReadFile(peer.SeedFile)
	require.NoError(t, e)
	var seed map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &seed))
	peer.Request = new(acc.GpuRequest)
	require.NoError(t, protojson.Unmarshal(seed["request"], peer.Request))
	peer.Request.Replicas = 1
	peer.Request.ContainerName = "workspace"
	return peer
}
