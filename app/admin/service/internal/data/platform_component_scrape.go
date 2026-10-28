package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// PlatformComponentScrapeConfig 是部署期显式配置的 Prometheus 查询入口。
// 未配置（Endpoint 为空）是正常状态：service 组的 scrape_status 全部为
// unknown + reason，其余字段不受影响。
type PlatformComponentScrapeConfig struct {
	Endpoint string
	Timeout  time.Duration
}

// PlatformComponentScrapeConfigFromEnv 从 ANI_PLATFORM_COMPONENTS_PROMETHEUS_URL
// 读取 Prometheus 地址。只接受 http(s)、无内嵌凭据、无 fragment。
func PlatformComponentScrapeConfigFromEnv() (PlatformComponentScrapeConfig, error) {
	raw := strings.TrimSpace(os.Getenv("ANI_PLATFORM_COMPONENTS_PROMETHEUS_URL"))
	if raw == "" {
		return PlatformComponentScrapeConfig{}, nil
	}
	endpoint, err := url.Parse(raw)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" ||
		endpoint.User != nil || endpoint.Fragment != "" {
		return PlatformComponentScrapeConfig{}, errors.New("invalid ANI_PLATFORM_COMPONENTS_PROMETHEUS_URL")
	}
	return PlatformComponentScrapeConfig{Endpoint: strings.TrimSuffix(raw, "/"), Timeout: 5 * time.Second}, nil
}

// PlatformComponentScrapeReader 用 Prometheus 的 `up` 序列判定各 metrics
// Service 的抓取可达性（方案 A，见实施方案 §4.4）。
type PlatformComponentScrapeReader struct {
	endpoint string
	client   *http.Client
}

// NewPlatformComponentScrapeReader 返回 nil 表示未配置，调用方据此把
// service 组置 unknown + reason。
func NewPlatformComponentScrapeReader(config PlatformComponentScrapeConfig) *PlatformComponentScrapeReader {
	if config.Endpoint == "" {
		return nil
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &PlatformComponentScrapeReader{endpoint: config.Endpoint, client: &http.Client{Timeout: timeout}}
}

// ReadScrapeStatuses 一次性查询全部 metrics Service 的 `up`，返回
// Service 名 → scrape_status。查询串里的 Service 名全部来自静态清单
// 常量，不接受任何外部输入。
func (r *PlatformComponentScrapeReader) ReadScrapeStatuses(ctx context.Context) (map[string]string, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("platform component scrape reader not configured")
	}
	names := platformComponentMetricsServices()
	if len(names) == 0 {
		return map[string]string{}, nil
	}
	expression := fmt.Sprintf(`up{namespace="ani-system",service=~"%s"}`, strings.Join(names, "|"))
	values := url.Values{}
	values.Set("query", expression)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint+"/api/v1/query?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus query status %d", response.StatusCode)
	}

	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("prometheus query decode: %w", err)
	}
	if payload.Status != "success" {
		return nil, errors.New("prometheus query failed")
	}

	// 没有序列 = 该 Service 一条 target 都没有（未抓取）；有序列但全部
	// up!=1 = 抓取失败。两者语义不同，必须分开。
	seen := make(map[string]bool, len(names))
	upOne := make(map[string]bool, len(names))
	for _, series := range payload.Data.Result {
		service := series.Metric["service"]
		if service == "" {
			continue
		}
		seen[service] = true
		var raw string
		if json.Unmarshal(series.Value[1], &raw) != nil {
			continue
		}
		if value, err := strconv.ParseFloat(raw, 64); err == nil && value == 1 {
			upOne[service] = true
		}
	}

	statuses := make(map[string]string, len(names))
	for _, name := range names {
		switch {
		case upOne[name]:
			statuses[name] = PlatformComponentScrapeReachable
		case seen[name]:
			statuses[name] = PlatformComponentScrapeUnreachable
		default:
			statuses[name] = PlatformComponentScrapeUnknown
		}
	}
	return statuses, nil
}

// platformComponentMetricsServices 从清单派生需要查询的 Service 名（去重）。
func platformComponentMetricsServices() []string {
	seen := make(map[string]bool, 7)
	names := make([]string, 0, 7)
	for _, entry := range platformComponentRegistry {
		if entry.Group != PlatformComponentGroupService || entry.MetricsServiceName == "" {
			continue
		}
		if seen[entry.MetricsServiceName] {
			continue
		}
		seen[entry.MetricsServiceName] = true
		names = append(names, entry.MetricsServiceName)
	}
	return names
}
