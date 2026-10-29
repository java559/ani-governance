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

func TestInferenceKeyCatalogIsSeparateFromPlatformKeys(t *testing.T) {
	document, err := os.ReadFile("../../app/admin/service/cmd/server/assets/openapi.yaml")
	require.NoError(t, err)
	catalog, err := Catalog(document)
	require.NoError(t, err)
	expected := map[string]string{
		"POST /api/v1/inference-keys":                      "inference:key:create",
		"GET /api/v1/inference-keys":                       "inference:key:read",
		"GET /api/v1/inference-keys/{key_id}":              "inference:key:read",
		"DELETE /api/v1/inference-keys/{key_id}":           "inference:key:revoke",
		"POST /api/v1/inference-keys/{key_id}:rotate":      "inference:key:rotate",
		"POST /api/v1/inference-keys/{key_id}:bind-policy": "inference:key:bind-policy",
		"POST /api/v1/inference-policies":                  "inference:policy:create",
		"GET /api/v1/inference-policies":                   "inference:policy:read",
		"GET /api/v1/inference-policies/{policy_id}":       "inference:policy:read",
		"PUT /api/v1/inference-policies/{policy_id}":       "inference:policy:update",
		"DELETE /api/v1/inference-policies/{policy_id}":    "inference:policy:delete",
	}
	count, platformCount := 0, 0
	for _, route := range catalog {
		if strings.HasPrefix(route.Path, "/api/v1/auth/api-keys") {
			platformCount++
			require.Equal(t, "AccessKeyService", route.Module)
			require.Equal(t, "SYSTEM", route.BusinessModule)
		}
		if route.Module != "InferenceKeyService" {
			continue
		}
		count++
		require.Equal(t, "INFERENCE", route.BusinessModule)
		grant, err := permcode.Resolve(route.Method, route.Path)
		require.NoError(t, err)
		require.Equal(t, expected[route.Method+" "+route.Path], grant.Code)
	}
	require.Equal(t, len(expected), count)
	require.Equal(t, 6, platformCount)
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
	require.Equal(t, 26, count) // Includes GetVMDNSDefaults alongside the original VM routes.
}
