//go:build quota_pg

package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type InstanceResourcePeer struct{ Address, CAFile, CertFile, KeyFile, TerminalAddress, SGClientCertFile, SGClientKeyFile, SGServerCertFile, SGServerKeyFile, GovControlCertFile, GovControlKeyFile string }

func StartInstanceResourcePeer(t *testing.T, gpu *acc.GpuRequest) *InstanceResourcePeer {
	t.Helper()
	binary := os.Getenv("RESOURCE_INSTANCE_TEST_BINARY")
	require.True(t, filepath.IsAbs(binary), "fixed Resource subprocess binary required")
	dir := t.TempDir()
	raw, e := protojson.Marshal(gpu)
	require.NoError(t, e)
	cmd := exec.Command(binary, "-test.run=^TestInstanceGovernanceFixture$", "-test.timeout=5m", "-test.v")
	cmd.Env = append(os.Environ(), "INSTANCE_JOINT_FIXTURE_DIR="+dir, "INSTANCE_JOINT_GPU_JSON="+string(raw))
	input, e := cmd.StdinPipe()
	require.NoError(t, e)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_, _ = fmt.Fprintln(input, "stop")
		_ = input.Close()
		select {
		case e := <-done:
			require.NoError(t, e, "Resource fixture output: %s", output.String())
			t.Log("Resource durable intake subprocess exit=0; isolated PG cleaned")
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("Resource fixture cleanup deadline")
		}
	})
	var info InstanceResourcePeer
	require.Eventually(t, func() bool {
		raw, e := os.ReadFile(filepath.Join(dir, "fixture.json"))
		return e == nil && json.Unmarshal(raw, &info) == nil
	}, 20*time.Second, 20*time.Millisecond, "Resource fixture startup")
	t.Setenv("ANI_INSTANCE_ADDR", info.Address)
	t.Setenv("ANI_INSTANCE_CA", info.CAFile)
	t.Setenv("ANI_INSTANCE_CERT", info.CertFile)
	t.Setenv("ANI_INSTANCE_KEY", info.KeyFile)
	return &info
}
