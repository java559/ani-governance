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
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

type InstanceResourcePeer struct{ Address, CAFile, CertFile, KeyFile, TerminalAddress, SGClientCertFile, SGClientKeyFile, SGServerCertFile, SGServerKeyFile, GovControlCertFile, GovControlKeyFile, PresetID, ClusterID string }

func StartInstanceResourcePeer(t *testing.T, gpu *acc.GpuRequest, profile *acc.GpuProfile) *InstanceResourcePeer {
	t.Helper()
	binary := os.Getenv("RESOURCE_INSTANCE_TEST_BINARY")
	require.True(t, filepath.IsAbs(binary), "fixed Resource subprocess binary required")
	dir := t.TempDir()
	raw := []byte("null")
	referenceJSON := []byte("null")
	var e error
	if gpu != nil {
		require.NotNil(t, profile)
		require.NotNil(t, profile.Spec)
		spec := profile.Spec
		reference := &res.GpuPresetReference{ProfileId: gpu.ProfileId, ProfileVersion: gpu.ProfileVersion, SpecDigest: profile.SpecDigest, DevicesPerInstance: gpu.DevicesPerReplica, ClusterId: gpu.ClusterId, PoolId: gpu.PoolId, Mode: spec.Mode, ModelKey: spec.ModelKey, CoreLimitPercent: spec.CoreLimitPercent, MemoryMibPerDevice: spec.MemoryMibPerDevice}
		raw, e = protojson.Marshal(gpu)
		require.NoError(t, e)
		referenceJSON, e = protojson.Marshal(reference)
		require.NoError(t, e)
	}
	cmd := exec.Command(binary, "-test.run=^TestInstanceGovernanceFixture$", "-test.timeout=5m", "-test.v")
	cmd.Env = append(os.Environ(), "INSTANCE_JOINT_FIXTURE_DIR="+dir, "INSTANCE_JOINT_GPU_JSON="+string(raw), "INSTANCE_JOINT_GPU_REFERENCE_JSON="+string(referenceJSON))
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
	require.NotEmpty(t, info.PresetID, "Resource fixture must create a real PG preset")
	require.NotEmpty(t, info.ClusterID, "Resource fixture must return its configured cluster")
	t.Setenv("ANI_INSTANCE_CLUSTER_ID", info.ClusterID)
	t.Setenv("ANI_INSTANCE_ADDR", info.Address)
	t.Setenv("ANI_INSTANCE_CA", info.CAFile)
	t.Setenv("ANI_INSTANCE_CERT", info.CertFile)
	t.Setenv("ANI_INSTANCE_KEY", info.KeyFile)
	return &info
}
