package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelDevManagedCommandsRequirePrivateAuthenticationMaterial(t *testing.T) {
	private := t.TempDir()
	token := filepath.Join(private, "token")
	request := filepath.Join(private, "request.json")
	require.NoError(t, os.WriteFile(token, []byte("synthetic-untrusted-token-must-not-print"), 0644))
	require.NoError(t, os.Chmod(token, 0644))
	require.NoError(t, os.WriteFile(request, []byte(`{}`), 0600))
	for _, command := range []string{"modeldev-enable", "modeldev-import-release", "modeldev-import-csv", "modeldev-import-finetuning-dataset", "modeldev-inspect", "modeldev-reconcile", "modeldev-cleanup-plan", "modeldev-cleanup-apply"} {
		t.Run(command, func(t *testing.T) {
			var output bytes.Buffer
			err := runAdmin(context.Background(), []string{command, "--conf", private, "--token-file", token, "--request-file", request}, &output)
			require.EqualError(t, err, "modeldev management requires a nonempty regular token file with mode 0600")
			require.Empty(t, output.String())
		})
	}
}
