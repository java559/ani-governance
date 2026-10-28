package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file is the pre-deployment integration adapter for training resource
// metrics. It is deliberately NOT part of any product wiring: the product path
// is the cross-repository observability training target (design doc §6.6). The
// only PromQL in the repository lives here, and no caller can supply it.

const (
	trainingMetricsRetentionHint = "24h"
	trainingMetricsMaxWindow     = 24 * time.Hour
	trainingMetricsMinStep       = 30
	trainingMetricsMaxStep       = 3600
	trainingMetricsMaxPoints     = 400
)

// TrainingMetricsQuery is one already-authorized exact target. The Pod name is
// always resolved by the server from the execution's Pod list; a caller cannot
// supply a selector or a regular expression.
type TrainingMetricsQuery struct {
	Namespace   string
	PodName     string
	Container   string
	Start, End  time.Time
	StepSeconds uint32
}

// TrainingMetricPoint keeps a gap explicit instead of writing a synthetic zero.
type TrainingMetricPoint struct {
	At        time.Time
	Value     *float64
	GapReason string
}

// TrainingMetricSeries reports per-series availability, so a detail page can
// show a precise empty state rather than four empty arrays.
type TrainingMetricSeries struct {
	MetricCode string
	Unit       string
	Pod        string
	Container  string
	Status     string
	GapReason  string
	Points     []TrainingMetricPoint
}

func (query TrainingMetricsQuery) validate() error {
	if query.Namespace == "" || len(query.Namespace) > 253 || strings.ContainsAny(query.Namespace, `/\.*+?[](){}|^$`) {
		return errors.New("invalid training metrics namespace")
	}
	if query.PodName == "" || len(query.PodName) > 253 || strings.ContainsAny(query.PodName, `.*+?[](){}|^$/\`) {
		return errors.New("invalid training metrics pod")
	}
	if query.Container == "" || len(query.Container) > 63 || strings.ContainsAny(query.Container, `.*+?[](){}|^$/\`) {
		return errors.New("invalid training metrics container")
	}
	if query.Start.IsZero() || query.End.IsZero() || !query.End.After(query.Start) || query.End.Sub(query.Start) > trainingMetricsMaxWindow {
		return errors.New("invalid training metrics window")
	}
	if query.StepSeconds < trainingMetricsMinStep || query.StepSeconds > trainingMetricsMaxStep {
		return errors.New("invalid training metrics step")
	}
	if int(query.End.Sub(query.Start)/time.Second)/int(query.StepSeconds)+1 > trainingMetricsMaxPoints {
		return errors.New("training metrics step yields too many points")
	}
	return nil
}

// TrainingMetricsConfig is explicit deployment material. Enabled is false by
// default and must never be set from a public request.
type TrainingMetricsConfig struct {
	Enabled  bool
	Endpoint string
	Timeout  time.Duration
	SkipTLS  bool
	// No credentials are read here: the integration endpoint is expected to be
	// reachable only from the operator network.
}

// TrainingMetricsReader is the narrow seam a future observability training
// target can implement without changing callers.
type TrainingMetricsReader interface {
	QueryTrainingMetrics(context.Context, TrainingMetricsQuery) ([]TrainingMetricSeries, error)
}

type prometheusTrainingMetricsReader struct {
	endpoint string
	client   *http.Client
}

// NewTrainingMetricsReader returns an adapter only when the operator enabled
// it. A disabled configuration is a normal answer, never an implicit client.
func NewTrainingMetricsReader(config TrainingMetricsConfig) (TrainingMetricsReader, error) {
	if !config.Enabled {
		return nil, errors.New("training metrics integration adapter is disabled")
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "http" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" || config.Timeout <= 0 {
		return nil, errors.New("invalid training metrics integration configuration")
	}
	return &prometheusTrainingMetricsReader{endpoint: strings.TrimSuffix(config.Endpoint, "/"), client: &http.Client{Timeout: config.Timeout}}, nil
}

// trainingMetricsDefinitions fixes both the metric codes and their queries.
var trainingMetricsDefinitions = []struct {
	code, unit, query string
}{
	{"cpu.usage_cores", "cores", `sum by (namespace, pod) (rate(container_cpu_usage_seconds_total{namespace="%s",pod="%s",container="%s"}[5m]))`},
	{"memory.working_set_bytes", "bytes", `sum by (namespace, pod) (container_memory_working_set_bytes{namespace="%s",pod="%s",container="%s"})`},
}

func (reader *prometheusTrainingMetricsReader) QueryTrainingMetrics(ctx context.Context, query TrainingMetricsQuery) ([]TrainingMetricSeries, error) {
	if reader == nil || reader.client == nil || query.validate() != nil {
		return nil, errors.New("invalid training metrics query")
	}
	series := make([]TrainingMetricSeries, 0, len(trainingMetricsDefinitions))
	for _, definition := range trainingMetricsDefinitions {
		// Only already-validated exact literals are folded into the fixed query.
		expression := fmt.Sprintf(definition.query, query.Namespace, query.PodName, query.Container)
		points, err := reader.rangePoints(ctx, expression, query)
		if err != nil {
			series = append(series, TrainingMetricSeries{
				MetricCode: definition.code, Unit: definition.unit, Pod: query.PodName, Container: query.Container,
				Status: "NO_DATA", GapReason: "EXPORTER_UNAVAILABLE",
			})
			continue
		}
		item := TrainingMetricSeries{MetricCode: definition.code, Unit: definition.unit, Pod: query.PodName, Container: query.Container, Points: points}
		present := 0
		for _, point := range points {
			if point.Value != nil {
				present++
			}
		}
		switch {
		case present == 0:
			item.Status, item.GapReason = "NO_DATA", "NO_DATA"
		case present < len(points):
			item.Status, item.GapReason = "PARTIAL", "NO_DATA"
		default:
			item.Status = "OK"
		}
		series = append(series, item)
	}
	return series, nil
}

// trainingMetricValue accepts the string-encoded sample value the Prometheus
// HTTP API emits, so a numeric fixture and a real response decode identically.
func trainingMetricValue(raw json.RawMessage) (float64, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		value, err := strconv.ParseFloat(text, 64)
		return value, err == nil
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

// rangePoints aligns every step to the requested grid so a missing sample is an
// explicit gap, not a silently shorter list.
func (reader *prometheusTrainingMetricsReader) rangePoints(ctx context.Context, expression string, query TrainingMetricsQuery) ([]TrainingMetricPoint, error) {
	values := url.Values{}
	values.Set("query", expression)
	values.Set("start", strconv.FormatInt(query.Start.Unix(), 10))
	values.Set("end", strconv.FormatInt(query.End.Unix(), 10))
	values.Set("step", strconv.FormatUint(uint64(query.StepSeconds), 10))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, reader.endpoint+"/api/v1/query_range?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	response, err := reader.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("training metrics upstream unavailable")
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil || payload.Status != "success" {
		return nil, errors.New("training metrics upstream protocol error")
	}
	if payload.Data.ResultType != "" && payload.Data.ResultType != "vector" && payload.Data.ResultType != "matrix" {
		return nil, errors.New("training metrics upstream result type")
	}
	samples := map[int64]float64{}
	if len(payload.Data.Result) > 1 {
		// More than one series for one exact Pod/container means the ownership
		// assumption failed; reporting a number would be guesswork.
		return nil, errors.New("training metrics upstream ambiguous source")
	}
	if len(payload.Data.Result) == 1 {
		for _, raw := range payload.Data.Result[0].Values {
			var at float64
			value, ok := trainingMetricValue(raw[1])
			if json.Unmarshal(raw[0], &at) != nil || !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				continue
			}
			samples[int64(at)] = value
		}
	}
	count := int(query.End.Sub(query.Start)/time.Second)/int(query.StepSeconds) + 1
	points := make([]TrainingMetricPoint, 0, count)
	for index := 0; index < count; index++ {
		at := query.Start.Add(time.Duration(index) * time.Duration(query.StepSeconds) * time.Second)
		point := TrainingMetricPoint{At: at, GapReason: "NO_DATA"}
		if value, found := samples[at.Unix()]; found {
			sample := value
			point.Value, point.GapReason = &sample, ""
		}
		points = append(points, point)
	}
	return points, nil
}
