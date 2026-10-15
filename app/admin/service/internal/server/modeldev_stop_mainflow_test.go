//go:build modeldev_pg && modeldev_contract

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"google.golang.org/protobuf/encoding/protojson"
)

// Normally the provider marker follows a real optimizer step and committed
// RUNNING history. Stop-before-delivery instead starts the same worker only
// after the real BFF Stop commits. No marker fabricates the durable stop intent.
func verifyModelDevBFFStopMainFlow(t *testing.T, ctx context.Context, web *httptest.Server, token, otherToken, deniedToken, operationID, executionID string, tenantID uint32, startAfterStop func()) {
	t.Helper()
	readMarker := func(name string, out any) {
		t.Helper()
		path := filepath.Join(filepath.Dir(os.Getenv("ANI_MODELDEV_MAINFLOW_STARTUP")), name)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			info, err := os.Lstat(path)
			if err == nil {
				require.True(t, info.Mode().IsRegular() && info.Mode().Perm() == 0600 && info.Size() <= 4096)
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()
				require.NoError(t, decoder.Decode(out))
				require.Equal(t, io.EOF, decoder.Decode(new(any)))
				return
			}
			require.True(t, os.IsNotExist(err), "provider marker must be accessible")
			select {
			case <-ctx.Done():
				t.Fatal("CPU10_STOP_MAIN_FLOW: provider did not reach " + name)
			case <-ticker.C:
			}
		}
	}
	if startAfterStop == nil {
		var started struct {
			OperationID string `json:"operation_id"`
			ExecutionID string `json:"execution_id"`
		}
		readMarker("training-started.json", &started)
		require.Equal(t, operationID, started.OperationID)
		require.Equal(t, executionID, started.ExecutionID)
	}
	request := func(method, path, accessToken string) (int, []byte) {
		req, err := http.NewRequestWithContext(ctx, method, web.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("x-ani-actor", "governance:user:7")
		req.Header.Set("x-ani-authorized-method", "forged")
		res, err := web.Client().Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(res.Body, 16385))
		require.NoError(t, err)
		require.LessOrEqual(t, len(raw), 16384)
		require.Equal(t, "no-store", res.Header.Get("Cache-Control"))
		return res.StatusCode, raw
	}
	path := "/api/v1/modeldev/executions/" + executionID
	status, _ := request("POST", path+":stop", otherToken)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = request("POST", path+":stop", deniedToken)
	require.Equal(t, http.StatusForbidden, status)
	status, raw := request("POST", path+":stop", token)
	require.Equal(t, http.StatusAccepted, status, "CPU10_BFF_STOP_MAIN_FLOW: actual BFF Stop must be accepted")
	var first modeldevv1.StopExecutionResponse
	require.NoError(t, protojson.Unmarshal(raw, &first))
	require.Equal(t, operationID, first.OperationId)
	require.Equal(t, executionID, first.ExecutionId)
	require.True(t, first.StopRequested)
	require.Equal(t, uint64(1), first.IntentGeneration)
	require.False(t, first.Replayed)
	require.NotContains(t, string(raw), "close_generation")
	require.NotContains(t, string(raw), "CLOSED")
	t.Logf("BFF_STOP_ACCEPTED execution=%s operation=%s at=%s intent_generation=1", executionID, operationID, time.Now().UTC().Format(time.RFC3339Nano))
	status, raw = request("POST", path+":stop", token)
	require.Equal(t, http.StatusAccepted, status)
	var replay modeldevv1.StopExecutionResponse
	require.NoError(t, protojson.Unmarshal(raw, &replay))
	require.Equal(t, first.IntentGeneration, replay.IntentGeneration)
	require.True(t, replay.Replayed)
	if startAfterStop != nil {
		// Both current-authorized Stop requests committed before any delivery.
		// The production worker sends the durable close before the queued create.
		startAfterStop()
		t.Log("BFF_STOP_BEFORE_DELIVERY: current-authorized Stop and replay committed before worker started")
	}
	var closed struct {
		OperationID     string `json:"operation_id"`
		ExecutionID     string `json:"execution_id"`
		CloseGeneration uint64 `json:"close_generation"`
	}
	readMarker("closed.json", &closed)
	require.Equal(t, operationID, closed.OperationID)
	require.Equal(t, executionID, closed.ExecutionID)
	require.Positive(t, closed.CloseGeneration)
	status, raw = request("GET", path, token)
	require.Equal(t, http.StatusOK, status)
	var detail modeldevv1.ExecutionView
	require.NoError(t, protojson.Unmarshal(raw, &detail))
	require.Equal(t, executionID, detail.GetExecutionId())
	require.True(t, detail.GetStopRequested())
	require.Equal(t, closed.CloseGeneration, detail.GetCloseGeneration())
	require.Equal(t, "CLOSED", detail.GetCloseState())
	observer := openModelDevHTTPPG(t, os.Getenv("ANI_TEST_DATABASE_DSN"))
	ackContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state, createState string
		err := observer.DB().QueryRowContext(appViewer.NewSystemViewerContext(ackContext), `SELECT close_dispatch_state,dispatch_state FROM sys_modeldev_acceptances WHERE tenant_id=$1 AND operation_id=$2 AND execution_id=$3`, tenantID, operationID, executionID).Scan(&state, &createState)
		require.NoError(t, err)
		if state == "ACKED" && (startAfterStop == nil || createState == "ACKED") {
			break
		}
		select {
		case <-ackContext.Done():
			t.Fatal("close delivery ACK did not become durable")
		case <-ticker.C:
		}
	}
	if startAfterStop != nil {
		t.Log("BFF_STOP_BEFORE_CLOSED: close and late create both durably ACKED; real BFF Query observed CLOSED")
	}
	t.Logf("BFF_STOP_CLOSED execution=%s at=%s close_generation=%d; real current-authorized query and independent close ACKED", executionID, time.Now().UTC().Format(time.RFC3339Nano), closed.CloseGeneration)
}
