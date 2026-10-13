//go:build quota_pg

package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func StartInstanceSessionPeer(t *testing.T, resource *InstanceResourcePeer, cluster string) {
	t.Helper()
	binary := os.Getenv("SG_INSTANCE_TEST_BINARY")
	require.True(t, filepath.IsAbs(binary))
	root := t.TempDir()
	config, e := json.Marshal(map[string]string{"CAFile": resource.CAFile, "ResourceAddress": resource.TerminalAddress, "SGClientCertFile": resource.SGClientCertFile, "SGClientKeyFile": resource.SGClientKeyFile, "SGServerCertFile": resource.SGServerCertFile, "SGServerKeyFile": resource.SGServerKeyFile, "ClusterID": cluster, "RedisAddress": os.Getenv("SG_INSTANCE_REDIS_ADDR")})
	require.NoError(t, e)
	path := filepath.Join(root, "config.json")
	require.NoError(t, os.WriteFile(path, config, 0600))
	cmd := exec.Command(binary, "-test.run=^TestInstanceSessionFixture$", "-test.timeout=5m", "-test.v")
	cmd.Env = append(os.Environ(), "SG_INSTANCE_FIXTURE_DIR="+root, "SG_INSTANCE_CONFIG_FILE="+path)
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
			require.NoError(t, e, "SG fixture: %s", output.String())
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Error("SG fixture cleanup deadline")
		}
	})
	var ready struct{ Address string }
	require.Eventually(t, func() bool {
		raw, e := os.ReadFile(filepath.Join(root, "ready.json"))
		return e == nil && json.Unmarshal(raw, &ready) == nil
	}, 20*time.Second, 20*time.Millisecond, "SG fixture readiness")
	t.Setenv("ANI_INSTANCE_SG_ADDR", ready.Address)
	t.Setenv("ANI_INSTANCE_SG_SERVER_NAME", "ani-session-gateway-server")
	t.Setenv("ANI_INSTANCE_SG_CA_FILE", resource.CAFile)
	t.Setenv("ANI_INSTANCE_SG_CERT_FILE", resource.GovControlCertFile)
	t.Setenv("ANI_INSTANCE_SG_KEY_FILE", resource.GovControlKeyFile)
}
