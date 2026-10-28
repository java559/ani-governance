package data

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// 日志读取层的错误分类。HTTP 层据此映射状态码，其他错误一律按内部错误处理，
// 不把 OpenSearch 的细节交给调用方。
var (
	ErrPlatformComponentLogUnavailable = errors.New("component log backend unavailable")
	ErrPlatformComponentLogTimeout     = errors.New("component log query timeout")
	ErrPlatformComponentLogRejected    = errors.New("component log backend rejected the credential")
)

// PlatformComponentLogReader 用只读身份查询 ani-logs-* 索引。
//
// 设计边界（实施方案 §6）：
//   - 只读；只查注册表派生的 namespace/Pod 范围，不接受任何请求输入；
//   - 只有两种查询：回放（按时间倒序取最近 N 条）与窗口增量（按入库时间正序）；
//   - 响应体超过上限时把 size 降到 1/4 重试一次，仍超限则显式失败；
//   - 禁止跟随重定向到其他主机，禁止回退明文。
type PlatformComponentLogReader struct {
	config PlatformComponentLogConfig
	client *http.Client
	index  string

	mu            sync.Mutex
	ingestedAtOK  bool
	ingestedAtSet bool
	ingestedAtAt  time.Time
}

// NewPlatformComponentLogReader 按配置构造 reader。返回 nil 表示未配置。
func NewPlatformComponentLogReader(config PlatformComponentLogConfig) (*PlatformComponentLogReader, error) {
	if config.Endpoint == "" {
		return nil, ErrPlatformComponentLogNotConfigured
	}
	ca, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, fmt.Errorf("component log reader: read ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("component log reader: ca file contains no certificate")
	}
	username, err := readPlatformComponentLogSecret(config.UsernameFile)
	if err != nil {
		return nil, fmt.Errorf("component log reader: read username: %w", err)
	}
	password, err := readPlatformComponentLogSecret(config.PasswordFile)
	if err != nil {
		return nil, fmt.Errorf("component log reader: read password: %w", err)
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   config.QueryTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
			ServerName: config.ServerName,
		},
		TLSHandshakeTimeout:   config.QueryTimeout,
		ResponseHeaderTimeout: config.QueryTimeout,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
	}
	reader := &PlatformComponentLogReader{
		config: config,
		index:  config.IndexPrefix + "*",
		client: &http.Client{
			Transport: transport,
			Timeout:   config.QueryTimeout + time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	reader.client.Transport = &platformComponentLogAuth{base: transport, username: username, password: password}
	return reader, nil
}

// Close 释放 idle 连接。
func (r *PlatformComponentLogReader) Close() {
	if r == nil || r.client == nil {
		return
	}
	if closer, ok := r.client.Transport.(io.Closer); ok {
		_ = closer.Close()
	}
}

// Limits 暴露 HTTP 层需要的超时与配额参数。
func (r *PlatformComponentLogReader) Limits() PlatformComponentLogStreamLimits {
	if r == nil {
		return DefaultPlatformComponentLogStreamLimits()
	}
	return r.config.StreamLimits()
}

// downloadedAtField 报告当前索引是否已具备 ani_log.ingested_at。
//
// 该字段由共享 ingest pipeline 写入，属于"允许改"与"不许改"两种评审结论的分叉点
// （实施方案 §6.2.1 / §6.2.5）。这里在运行时探测，两种环境都能工作：
//   - 字段存在：增量按入库时间窗口查询，迟到日志可读；
//   - 字段缺失：退化为按事件时间窗口，属于已登记的已知限制。
//
// 探测结果缓存 5 分钟，避免每轮查询都多打一次 mapping。
func (r *PlatformComponentLogReader) downloadedAtField(ctx context.Context) bool {
	r.mu.Lock()
	if r.ingestedAtSet && time.Since(r.ingestedAtAt) < 5*time.Minute {
		ok := r.ingestedAtOK
		r.mu.Unlock()
		return ok
	}
	r.mu.Unlock()

	ok := r.probeIngestedAt(ctx)

	r.mu.Lock()
	r.ingestedAtOK = ok
	r.ingestedAtSet = true
	r.ingestedAtAt = time.Now()
	r.mu.Unlock()
	return ok
}

func (r *PlatformComponentLogReader) probeIngestedAt(ctx context.Context) bool {
	call, cancel := context.WithTimeout(ctx, r.config.QueryTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(call, http.MethodGet, r.config.Endpoint+"/"+r.index+"/_mapping/field/ani_log.ingested_at", nil)
	if err != nil {
		return false
	}
	response, err := r.client.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return false
	}
	// 有字段映射时返回 {"<index>":{"mappings":{"ani_log.ingested_at":{...}}}}；
	// 没有该字段时 OpenSearch 返回空对象 {}。
	return bytes.Contains(raw, []byte("ingested_at"))
}

// ReplayQuery 描述一次回放查询。
type ReplayQuery struct {
	Scope PlatformComponentLogScope
	Limit int
}

// IncrementQuery 描述一次窗口增量查询。
type IncrementQuery struct {
	Scope   PlatformComponentLogScope
	From    time.Time
	To      time.Time
	MaxSize int
}

// Replay 查询最近 Limit 条并翻转成正序。
func (r *PlatformComponentLogReader) Replay(ctx context.Context, query ReplayQuery) (PlatformComponentLogBatch, error) {
	if query.Limit <= 0 {
		return PlatformComponentLogBatch{}, nil
	}
	body := r.baseQuery(query.Scope)
	body["size"] = query.Limit
	body["sort"] = []map[string]any{{"@timestamp": map[string]string{"order": "desc"}}}
	batch, err := r.search(ctx, body, query.Limit)
	if err != nil {
		return PlatformComponentLogBatch{}, err
	}
	entries := batch.Entries
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	batch.Entries = entries
	return batch, nil
}

// Increment 查询 [From, To] 内新入库的记录，按查询用时间字段正序。
func (r *PlatformComponentLogReader) Increment(ctx context.Context, query IncrementQuery) (PlatformComponentLogBatch, error) {
	if query.MaxSize <= 0 {
		return PlatformComponentLogBatch{}, nil
	}
	field := "@timestamp"
	if r.downloadedAtField(ctx) {
		field = "ani_log.ingested_at"
	}
	body := r.baseQuery(query.Scope)
	body["size"] = query.MaxSize
	body["sort"] = []map[string]any{{field: map[string]string{"order": "asc"}}}
	body["query"].(map[string]any)["bool"].(map[string]any)["filter"] = append(
		body["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any),
		map[string]any{"range": map[string]any{field: map[string]any{
			"gte": formatPlatformComponentLogTime(query.From),
			"lte": formatPlatformComponentLogTime(query.To),
		}}},
	)
	return r.search(ctx, body, query.MaxSize)
}

// baseQuery 构造与组件范围绑定的查询骨架。所有取值都来自注册表，
// 不拼接任何请求输入。
func (r *PlatformComponentLogReader) baseQuery(scope PlatformComponentLogScope) map[string]any {
	return map[string]any{
		"_source": []string{
			"@timestamp", "log", "log_payload", "stream",
			"ani_log.level", "ani_log.message",
			"kubernetes.namespace_name", "kubernetes.pod_name", "kubernetes.container_name",
		},
		"query": map[string]any{
			"bool": map[string]any{
				"filter": []any{
					map[string]any{"term": map[string]any{"kubernetes.namespace_name.keyword": scope.Namespace}},
					map[string]any{"regexp": map[string]any{"kubernetes.pod_name.keyword": scope.PodPattern}},
				},
				// 防御性冗余：命名规则已排除现有 sandbox，仍显式排除含 sandbox 的 Pod。
				"must_not": []any{
					map[string]any{"wildcard": map[string]any{"kubernetes.pod_name.keyword": "*sandbox*"}},
				},
			},
		},
	}
}

// search 执行检索，处理响应体上限降级。
func (r *PlatformComponentLogReader) search(ctx context.Context, body map[string]any, size int) (PlatformComponentLogBatch, error) {
	batch, tooLarge, err := r.doSearch(ctx, body, size)
	if err != nil {
		return PlatformComponentLogBatch{}, err
	}
	if !tooLarge {
		return batch, nil
	}
	reduced := size / 4
	if reduced < 1 {
		return PlatformComponentLogBatch{}, fmt.Errorf("%w: response exceeds limit", ErrPlatformComponentLogUnavailable)
	}
	body["size"] = reduced
	batch, tooLarge, err = r.doSearch(ctx, body, reduced)
	if err != nil {
		return PlatformComponentLogBatch{}, err
	}
	if tooLarge {
		return PlatformComponentLogBatch{}, fmt.Errorf("%w: response exceeds limit", ErrPlatformComponentLogUnavailable)
	}
	return batch, nil
}

func (r *PlatformComponentLogReader) doSearch(ctx context.Context, body map[string]any, size int) (PlatformComponentLogBatch, bool, error) {
	call, cancel := context.WithTimeout(ctx, r.config.QueryTimeout)
	defer cancel()
	payload, err := json.Marshal(body)
	if err != nil {
		return PlatformComponentLogBatch{}, false, err
	}
	request, err := http.NewRequestWithContext(call, http.MethodPost, r.config.Endpoint+"/"+r.index+"/_search", bytes.NewReader(payload))
	if err != nil {
		return PlatformComponentLogBatch{}, false, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return PlatformComponentLogBatch{}, false, platformComponentLogTransportError(err, ctx)
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return PlatformComponentLogBatch{}, false, fmt.Errorf("%w: status %d", ErrPlatformComponentLogRejected, response.StatusCode)
	case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return PlatformComponentLogBatch{}, false, fmt.Errorf("%w: status %d", ErrPlatformComponentLogUnavailable, response.StatusCode)
	default:
		return PlatformComponentLogBatch{}, false, fmt.Errorf("%w: status %d", ErrPlatformComponentLogUnavailable, response.StatusCode)
	}

	// 多读 1 字节用于判定超限，避免把超限响应当成正常结果解析。
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(r.config.ResponseMaxBytes)+1))
	if err != nil {
		return PlatformComponentLogBatch{}, false, platformComponentLogTransportError(err, ctx)
	}
	if len(raw) > r.config.ResponseMaxBytes {
		return PlatformComponentLogBatch{}, true, nil
	}

	var decoded platformComponentLogSearchResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return PlatformComponentLogBatch{}, false, fmt.Errorf("%w: decode response", ErrPlatformComponentLogUnavailable)
	}
	if decoded.TimedOut {
		// 后端返回部分结果时必须显式超时，不能把未读完的窗口当成完整结果。
		return PlatformComponentLogBatch{}, false, fmt.Errorf("%w: backend timed out", ErrPlatformComponentLogTimeout)
	}
	return platformComponentLogBatchFrom(decoded, size), false, nil
}

type platformComponentLogSearchResponse struct {
	TimedOut bool `json:"timed_out"`
	Hits     struct {
		Hits []struct {
			Index  string          `json:"_index"`
			ID     string          `json:"_id"`
			Source json.RawMessage `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

type platformComponentLogSource struct {
	Timestamp string `json:"@timestamp"`
	Log       string `json:"log"`
	Stream    string `json:"stream"`
	Kubernetes struct {
		NamespaceName string `json:"namespace_name"`
		PodName       string `json:"pod_name"`
		ContainerName string `json:"container_name"`
	} `json:"kubernetes"`
	AniLog struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	} `json:"ani_log"`
	LogPayload json.RawMessage `json:"log_payload"`
}

// platformComponentLogBatchFrom 把检索结果归一成六字段，并统计被跳过的条数。
//
// 兜底规则（实施方案 §4.1）：
//   - container/pod/stream 缺失 → 空串（不编造 stdout）；
//   - level 缺失 → unknown；
//   - message 依次回退 log → ani_log.message → 序列化后的 log_payload，都没有则跳过并计数；
//   - @timestamp 缺失或不可解析 → 跳过并计数（不拿当前时间冒充）。
func platformComponentLogBatchFrom(decoded platformComponentLogSearchResponse, size int) PlatformComponentLogBatch {
	batch := PlatformComponentLogBatch{RequestedSize: size}
	batch.Entries = make([]PlatformComponentLogEntry, 0, len(decoded.Hits.Hits))
	for _, hit := range decoded.Hits.Hits {
		var source platformComponentLogSource
		if len(hit.Source) != 0 {
			if err := json.Unmarshal(hit.Source, &source); err != nil {
				batch.Skipped++
				continue
			}
		}
		timestamp, err := time.Parse(time.RFC3339Nano, source.Timestamp)
		if err != nil || source.Timestamp == "" {
			batch.Skipped++
			continue
		}
		message := platformComponentLogMessage(source)
		if message == "" {
			batch.Skipped++
			continue
		}
		level := strings.ToLower(strings.TrimSpace(source.AniLog.Level))
		if level == "" {
			level = "unknown"
		}
		batch.Entries = append(batch.Entries, PlatformComponentLogEntry{
			Container: source.Kubernetes.ContainerName,
			Level:     level,
			Message:   message,
			Pod:       source.Kubernetes.PodName,
			Stream:    source.Stream,
			Timestamp: timestamp.UTC(),
			Index:     hit.Index,
			ID:        hit.ID,
		})
	}
	return batch
}

// platformComponentLogMessage 实现三级回退。
func platformComponentLogMessage(source platformComponentLogSource) string {
	if source.Log != "" {
		return source.Log
	}
	if source.AniLog.Message != "" {
		return source.AniLog.Message
	}
	if len(source.LogPayload) != 0 && string(source.LogPayload) != "null" {
		var compact bytes.Buffer
		if json.Compact(&compact, source.LogPayload) == nil {
			return compact.String()
		}
		return string(source.LogPayload)
	}
	return ""
}

func platformComponentLogTransportError(err error, parent context.Context) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: %v", ErrPlatformComponentLogTimeout, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrPlatformComponentLogTimeout, err)
	}
	return fmt.Errorf("%w: %v", ErrPlatformComponentLogUnavailable, err)
}

// formatPlatformComponentLogTime 输出带毫秒的 UTC 时间，与 @timestamp 的
// strict_date_optional_time 可直接比较。
func formatPlatformComponentLogTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

// platformComponentLogAuth 注入 Basic 认证；凭据只来自受限文件，永不透传前端 Token。
type platformComponentLogAuth struct {
	base     http.RoundTripper
	username string
	password string
}

func (t *platformComponentLogAuth) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.SetBasicAuth(t.username, t.password)
	return t.base.RoundTrip(clone)
}

func (t *platformComponentLogAuth) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// readPlatformComponentLogSecret 读取挂载的单行凭据文件，去掉行尾空白。
func readPlatformComponentLogSecret(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("empty credential file")
	}
	return value, nil
}
