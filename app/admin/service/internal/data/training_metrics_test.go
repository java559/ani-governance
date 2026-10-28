package data

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func trainingMetricsQuery(start time.Time) TrainingMetricsQuery {
	return TrainingMetricsQuery{
		Namespace: "tenant-training", PodName: "md-pod-0", Container: "node",
		Start: start, End: start.Add(2 * time.Minute), StepSeconds: 60,
	}
}

// The adapter is off unless an operator enables it; a disabled configuration
// never yields a client.
func TestTrainingMetricsReaderIsDisabledByDefault(t *testing.T) {
	if reader, err := NewTrainingMetricsReader(TrainingMetricsConfig{}); reader != nil || err == nil {
		t.Fatalf("disabled adapter built a reader: %v %v", reader, err)
	}
	if _, err := NewTrainingMetricsReader(TrainingMetricsConfig{Enabled: true, Endpoint: "https://prom.example"}); err == nil {
		t.Fatal("non-loopback scheme accepted")
	}
}

// Nothing a caller passes can become a selector: the query is fixed and only
// validated literals are interpolated.
func TestTrainingMetricsRejectsCallerSuppliedSelectors(t *testing.T) {
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	for _, mutate := range []func(*TrainingMetricsQuery){
		func(q *TrainingMetricsQuery) { q.PodName = `.*` },
		func(q *TrainingMetricsQuery) { q.PodName = "md-pod-0" + `"} or vector(1` },
		func(q *TrainingMetricsQuery) { q.Container = "node.*" },
		func(q *TrainingMetricsQuery) { q.Namespace = "tenant/../other" },
		func(q *TrainingMetricsQuery) { q.End = q.Start.Add(25 * time.Hour) },
		func(q *TrainingMetricsQuery) { q.StepSeconds = 10 },
		func(q *TrainingMetricsQuery) { q.StepSeconds = 30; q.End = q.Start.Add(24 * time.Hour) },
	} {
		query := trainingMetricsQuery(start)
		mutate(&query)
		require.Error(t, query.validate())
	}
	require.NoError(t, trainingMetricsQuery(start).validate())
}

// A missing exporter yields an explicit NO_DATA series, and a gap inside the
// window keeps the point count aligned to the requested grid.
func TestTrainingMetricsReportsNoDataAndAlignedGaps(t *testing.T) {
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Query().Get("query"))
		switch {
		case strings.Contains(r.URL.Query().Get("query"), "container_cpu_usage_seconds_total"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{"values": []any{
					[]any{float64(start.Unix()), "1.5"},
					[]any{float64(start.Add(2 * time.Minute).Unix()), "2.5"},
				}}}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success",
				"data":   map[string]any{"resultType": "matrix", "result": []any{}},
			})
		}
	}))
	t.Cleanup(server.Close)

	reader, err := NewTrainingMetricsReader(TrainingMetricsConfig{Enabled: true, Endpoint: server.URL, Timeout: 5 * time.Second})
	require.NoError(t, err)
	series, err := reader.QueryTrainingMetrics(context.Background(), trainingMetricsQuery(start))
	require.NoError(t, err)
	require.Len(t, series, 2)

	cpu := series[0]
	require.Equal(t, "cpu.usage_cores", cpu.MetricCode)
	require.Equal(t, "PARTIAL", cpu.Status)
	require.Len(t, cpu.Points, 3)
	require.NotNil(t, cpu.Points[0].Value)
	require.Nil(t, cpu.Points[1].Value)
	require.Equal(t, "NO_DATA", cpu.Points[1].GapReason)

	memory := series[1]
	require.Equal(t, "memory.working_set_bytes", memory.MetricCode)
	require.Equal(t, "NO_DATA", memory.Status)
	require.Equal(t, "NO_DATA", memory.GapReason)
	for _, point := range memory.Points {
		require.Nil(t, point.Value)
	}

	for _, query := range paths {
		require.NotContains(t, query, ".*", "a caller selector reached the query")
		require.Contains(t, query, `pod="md-pod-0"`)
		require.Contains(t, query, `container="node"`)
	}
}

// An unreachable or ambiguous exporter is reported as unavailable rather than
// as a zero-valued curve.
func TestTrainingMetricsReportsExporterUnavailable(t *testing.T) {
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		reader, err := NewTrainingMetricsReader(TrainingMetricsConfig{Enabled: true, Endpoint: server.URL, Timeout: 2 * time.Second})
		require.NoError(t, err)
		series, err := reader.QueryTrainingMetrics(context.Background(), trainingMetricsQuery(start))
		require.NoError(t, err)
		require.Len(t, series, 2)
		for _, item := range series {
			require.Equal(t, "NO_DATA", item.Status)
			require.Equal(t, "EXPORTER_UNAVAILABLE", item.GapReason)
		}
		server.Close()
	}
}

// Two series for one exact Pod/container means the ownership assumption failed;
// returning either number would be a guess.
func TestTrainingMetricsRejectsAmbiguousSource(t *testing.T) {
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data": map[string]any{"resultType": "matrix", "result": []any{
				map[string]any{"values": []any{[]any{float64(start.Unix()), "1"}}},
				map[string]any{"values": []any{[]any{float64(start.Unix()), "2"}}},
			}},
		})
	}))
	t.Cleanup(server.Close)
	reader, err := NewTrainingMetricsReader(TrainingMetricsConfig{Enabled: true, Endpoint: server.URL, Timeout: 2 * time.Second})
	require.NoError(t, err)
	series, err := reader.QueryTrainingMetrics(context.Background(), trainingMetricsQuery(start))
	require.NoError(t, err)
	for _, item := range series {
		require.Equal(t, "NO_DATA", item.Status)
		require.Equal(t, "EXPORTER_UNAVAILABLE", item.GapReason)
	}
}
