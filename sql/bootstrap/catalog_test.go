package bootstrap

import (
	"github.com/stretchr/testify/require"
	"go-wind-admin/pkg/permcode"
	"os"
	"strings"
	"testing"
)

func TestCatalogValidation(t *testing.T) {
	_, err := Catalog([]byte("openapi: 3.0.0\ninfo: {title: test, version: '1'}\n"))
	require.Error(t, err)
	_, err = Catalog([]byte("openapi: 3.0.0\ninfo: {title: test, version: '1'}\npaths:\n  /unknown:\n    get:\n      operationId: Unknown_Get\n      tags: [UnknownService]\n"))
	require.ErrorContains(t, err, "business module")
}

// The deployment catalog must describe the actual Kratos path variables. A
// camelCase OpenAPI alias creates a different DB authorization route even when
// the public URL is the same, preventing the tenant from reading its own VM.
func TestVMCatalogUsesRuntimePathVariables(t *testing.T) {
	document, err := os.ReadFile("../../app/admin/service/cmd/server/assets/openapi.yaml")
	require.NoError(t, err)
	catalog, err := Catalog(document)
	require.NoError(t, err)
	count := 0
	for _, route := range catalog {
		if route.Module != "VMService" {
			continue
		}
		count++
		require.NotContains(t, route.Path, "{vmId}")
		require.NotContains(t, route.Path, "{operationId}")
		require.NotContains(t, route.Path, "{snapshotId}")
		resolved, err := permcode.Resolve(route.Method, route.Path)
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(resolved.Code, "vm:"))
	}
	require.Equal(t, 25, count)
}
