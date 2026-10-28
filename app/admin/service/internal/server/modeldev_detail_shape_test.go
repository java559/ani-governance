package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/require"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	modeldevv1 "go-wind-admin/api/gen/go/modeldev/service/v1"
)

// The frontend reads the timeline and curve from the detail body, so their JSON
// field names are part of the contract. This exercises the production route and
// encoder instead of assuming protojson naming.
func TestExecutionDetailHTTPExposesStepsAndMetrics(t *testing.T) {
	started := time.Date(2026, 10, 22, 1, 2, 3, 0, time.UTC)
	step := int64(4)
	reply := &modeldevv1.GetExecutionResponse{Execution: &modeldevv1.ExecutionView{
		ExecutionId: "11111111-2222-4333-8444-555555555555",
		CurrentStep: "COLLECT",
		Steps: []*modeldevv1.ExecutionStepView{
			{Role: "prepare", State: "SUCCEEDED", StartedAt: started.Format(time.RFC3339Nano), FinishedAt: started.Format(time.RFC3339Nano)},
			{Role: "collect", State: "RUNNING", StartedAt: started.Format(time.RFC3339Nano), DeadlineAt: started.Format(time.RFC3339Nano)},
			{Role: "export", State: "PENDING"},
		},
		Metrics: []*modeldevv1.ExecutionMetricView{{Name: "train.loss", DecimalValue: "0.87", Step: &step}},
	}}
	srv := khttp.NewServer(khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, request interface{}) (interface{}, error) { return reply, nil }
	}))
	adminv1.RegisterNetworkServiceHTTPServer(srv, nil)
	registerModelDevHTTP(srv, nil)
	web := httptest.NewServer(srv)
	defer web.Close()

	response, err := web.Client().Get(web.URL + "/api/v1/modeldev/executions/11111111-2222-4333-8444-555555555555")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)

	var body struct {
		CurrentStep string `json:"current_step"`
		Steps       []struct {
			Role       string  `json:"role"`
			State      string  `json:"state"`
			StartedAt  string  `json:"started_at"`
			FinishedAt string  `json:"finished_at"`
			ExitCode   int32   `json:"exit_code"`
			DeadlineAt string  `json:"deadline_at"`
			Extra      *string `json:"extra"`
		} `json:"steps"`
		Metrics []struct {
			Name         string `json:"name"`
			DecimalValue string `json:"decimal_value"`
			Step         string `json:"step"`
		} `json:"metrics"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	require.Equal(t, "COLLECT", body.CurrentStep)
	require.Len(t, body.Steps, 3)
	require.Equal(t, "prepare", body.Steps[0].Role)
	require.Equal(t, "SUCCEEDED", body.Steps[0].State)
	require.Equal(t, started.Format(time.RFC3339Nano), body.Steps[0].StartedAt)
	require.Equal(t, started.Format(time.RFC3339Nano), body.Steps[0].FinishedAt)
	require.Equal(t, "RUNNING", body.Steps[1].State)
	require.Equal(t, started.Format(time.RFC3339Nano), body.Steps[1].DeadlineAt)
	require.Empty(t, body.Steps[1].FinishedAt)
	require.Equal(t, "PENDING", body.Steps[2].State)
	require.Empty(t, body.Steps[2].StartedAt)
	require.Len(t, body.Metrics, 1)
	require.Equal(t, "train.loss", body.Metrics[0].Name)
	require.Equal(t, "0.87", body.Metrics[0].DecimalValue)
	// An int64 in protojson is a string, which is exactly what the frontend parses.
	require.Equal(t, "4", body.Metrics[0].Step)
}
