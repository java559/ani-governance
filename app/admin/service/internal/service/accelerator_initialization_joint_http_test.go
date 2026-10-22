package service

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go-wind-admin/app/admin/service/internal/data"
)

type initializationJointSeed struct {
	Address           string `json:"address"`
	ServerName        string `json:"server_name"`
	CAFile            string `json:"ca_file"`
	CertFile          string `json:"cert_file"`
	KeyFile           string `json:"key_file"`
	DiscoveryRevision string `json:"discovery_revision"`
	NodeUID           string `json:"node_uid"`
	NodeName          string `json:"node_name"`
	BaselineRef       string `json:"baseline_ref"`
}

func initializationJointGrant(method, path string) bool {
	for _, action := range []string{"accelerator:initialization:read", "accelerator:discovery:list", "accelerator:initialization:create", "accelerator:initialization:retry", "accelerator:pool:create", "accelerator:supply-group:adopt", "accelerator:profile:publish", "accelerator:supply-group:set-admission"} {
		route := acceleratorInitializationActions[action]
		if route.method == method && route.path == path {
			return true
		}
	}
	return method == "GET" && path == "/api/v1/admin/accelerator/initializations/{operation_id}"
}

func initializationJointInput(t *testing.T) initializationJointSeed {
	t.Helper()
	encoded, err := os.ReadFile(os.Getenv("GOV_ACC_INITIALIZATION_JOINT_SEED_FILE"))
	require.NoError(t, err)
	var seed initializationJointSeed
	require.NoError(t, json.Unmarshal(encoded, &seed))
	return seed
}

func initializationJointConfig(t *testing.T) data.AcceleratorClientConfig {
	t.Helper()
	seed := initializationJointInput(t)
	return data.AcceleratorClientConfig{Address: seed.Address, ServerName: seed.ServerName, CAFile: seed.CAFile, CertFile: seed.CertFile, KeyFile: seed.KeyFile, Timeout: 3 * time.Second}
}

func runAcceleratorInitializationJointHTTP(t *testing.T, request func(string, string, string, any, int) map[string]any, platformToken, tenantToken string) {
	t.Helper()
	seed := initializationJointInput(t)
	const root = "/api/v1/admin/accelerator/"
	request("GET", root+"initialization", "", nil, http.StatusUnauthorized)
	request("GET", root+"initialization", tenantToken, nil, http.StatusForbidden)
	summary := request("GET", root+"initialization", platformToken, nil, http.StatusOK)
	require.Equal(t, true, summary["registered"])
	discovered := request("GET", root+"discovered-nodes?page=1&pageSize=20", platformToken, nil, http.StatusOK)
	require.Equal(t, "1", discovered["total"])
	require.Equal(t, seed.DiscoveryRevision, discovered["discovery_revision"])
	create := map[string]any{"data": map[string]any{
		"idempotency_key": uuid.NewString(), "discovery_revision": seed.DiscoveryRevision,
		"pools":    []any{map[string]any{"client_key": "pool", "new_pool": map[string]any{"display_name": "joint initialized pool"}}},
		"groups":   []any{map[string]any{"client_key": "group", "new_group": map[string]any{"pool_key": "pool", "display_name": "joint initialized group", "nodes": []any{map[string]any{"uid": seed.NodeUID, "name": seed.NodeName}}, "baseline_ref": seed.BaselineRef, "queue_name": "default"}}},
		"profiles": []any{map[string]any{"client_key": "profile", "group_key": "group", "display_name": "joint 8GiB", "memory_mib_per_device": "8192", "core_limit_percent": 25, "max_devices_per_replica": 1}},
	}}
	accepted := request("POST", root+"initializations", platformToken, create, http.StatusOK)
	require.Equal(t, "ACCEPTED", accepted["state"])
	operationID, ok := accepted["operation_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, operationID)
	replay := request("POST", root+"initializations", platformToken, create, http.StatusOK)
	require.Equal(t, operationID, replay["operation_id"])
	operationPath := root + "initializations/" + operationID
	observe := func(want string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			operation := request("GET", operationPath, platformToken, nil, http.StatusOK)
			state := operation["state"]
			if state == want {
				return operation
			}
			require.NotEqual(t, "FAILED", state, "%v", operation)
			require.NotEqual(t, "CONFLICT", state, "%v", operation)
			require.True(t, time.Now().Before(deadline), "did not reach %s: %v", want, operation)
			time.Sleep(10 * time.Millisecond)
		}
	}
	blocked := observe("BLOCKED")
	require.Equal(t, true, blocked["retryable"])
	retry := map[string]any{"data": map[string]any{"idempotency_key": uuid.NewString(), "expected_version": blocked["version"]}}
	request("POST", operationPath+":retry", tenantToken, retry, http.StatusForbidden)
	request("POST", operationPath+":retry", platformToken, retry, http.StatusOK)
	completed := observe("SUCCEEDED")
	groups := completed["groups"].([]any)
	require.Len(t, groups, 1)
	require.Equal(t, "OPENED", groups[0].(map[string]any)["auto_open_result"])
	require.Equal(t, "ADMISSION_OPEN", groups[0].(map[string]any)["admission"])
	profiles := completed["profiles"].([]any)
	require.Len(t, profiles, 1)
	require.Equal(t, "PROFILE_ACTIVE", profiles[0].(map[string]any)["recorded_state"])
	summary = request("GET", root+"initialization", platformToken, nil, http.StatusOK)
	require.Equal(t, true, summary["configuration_complete"])
	require.Equal(t, operationID, summary["latest_operation"].(map[string]any)["operation_id"])
	t.Log("PASS real generated HTTP/JWT/token-cache/DB policy/Casbin/BFF/mTLS/ACC domain/PG/worker; Kubernetes discovery and label writes are external boundary fixtures")
}
