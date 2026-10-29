package data

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// PlatformComponentLogConfigEnv 是日志 reader 配置文件的路径环境变量。
// 未设置表示"日志接口未启用"：接口返回 503，状态接口不受影响。
const PlatformComponentLogConfigEnv = "ANI_COMPONENT_LOGS_CONFIG"

// platformComponentLogConfigFile 是配置文件的结构（键名即对外合同，见实施方案 §11.2）。
type platformComponentLogConfigFile struct {
	OpenSearch struct {
		Endpoint            string `json:"endpoint"`
		ServerName          string `json:"server_name"`
		CAFile              string `json:"ca_file"`
		UsernameFile        string `json:"username_file"`
		PasswordFile        string `json:"password_file"`
		IndexPrefix         string `json:"index_prefix"`
		RetentionDays       int    `json:"retention_days"`
		QueryTimeoutSeconds int    `json:"query_timeout_seconds"`
		ResponseMaxBytes    int    `json:"response_max_bytes"`
	} `json:"opensearch"`
	Stream struct {
		OpenTimeoutSeconds   int `json:"open_timeout_seconds"`
		WriteTimeoutSeconds  int `json:"write_timeout_seconds"`
		HeartbeatSeconds     int `json:"heartbeat_seconds"`
		ReplayLimitMax       int `json:"replay_limit_max"`
		IncrementMax         int `json:"increment_max"`
		OverlapSeconds       int `json:"overlap_seconds"`
		MaxFrameBytes        int `json:"max_frame_bytes"`
		MaxBufferedBytes     int `json:"max_buffered_bytes"`
		ProcessConnections   int `json:"process_connections"`
		UserConnections      int `json:"user_connections"`
		ComponentConnections int `json:"component_connections"`
	} `json:"stream"`
}

// PlatformComponentLogConfig 是校验后的日志读取配置。
type PlatformComponentLogConfig struct {
	Endpoint      string
	ServerName    string
	CAFile        string
	UsernameFile  string
	PasswordFile  string
	IndexPrefix   string
	RetentionDays int

	QueryTimeout     time.Duration
	ResponseMaxBytes int

	OpenTimeout          time.Duration
	WriteTimeout         time.Duration
	Heartbeat            time.Duration
	ReplayLimitMax       int
	IncrementMax         int
	Overlap              time.Duration
	MaxFrameBytes        int
	MaxBufferedBytes     int
	ProcessConnections   int
	UserConnections      int
	ComponentConnections int
}

// platformComponentLogDefault* 是未显式配置时的取值（与实施方案 §6.3 一致）。
const (
	platformComponentLogDefaultQueryTimeout = 2 * time.Second
	platformComponentLogDefaultResponseMax  = 4 << 20
	platformComponentLogDefaultOpenTimeout  = 3 * time.Second
	platformComponentLogDefaultWriteTimeout = 2 * time.Second
	platformComponentLogDefaultHeartbeat    = 2 * time.Second
	platformComponentLogDefaultIncrement    = 500
	platformComponentLogDefaultOverlap      = 30 * time.Second
	platformComponentLogDefaultMaxFrame     = 256 << 10
	platformComponentLogDefaultMaxBuffered  = 1 << 20
	platformComponentLogDefaultProcessConns = 16
	platformComponentLogDefaultUserConns    = 4
	platformComponentLogDefaultCompConns    = 4
)

// ErrPlatformComponentLogNotConfigured 表示环境变量未设置：日志接口显式不可用。
var ErrPlatformComponentLogNotConfigured = errors.New("platform component log reader not configured")

// PlatformComponentLogStreamLimits 是 HTTP 层需要的流保护参数。
//
// 单独成结构是为了让未配置日志依赖的实例也有确定的上限：接口回 503 之前
// 仍要能正确计算并释放"打开中"的计数，不能因为配置缺失退化成无限额。
type PlatformComponentLogStreamLimits struct {
	OpenTimeout          time.Duration
	WriteTimeout         time.Duration
	Heartbeat            time.Duration
	MaxFrameBytes        int
	ProcessConnections   int
	UserConnections      int
	ComponentConnections int
}

// StreamLimits 把校验后的配置投影成 HTTP 层的保护参数。
func (c PlatformComponentLogConfig) StreamLimits() PlatformComponentLogStreamLimits {
	return PlatformComponentLogStreamLimits{
		OpenTimeout:          c.OpenTimeout,
		WriteTimeout:         c.WriteTimeout,
		Heartbeat:            c.Heartbeat,
		MaxFrameBytes:        c.MaxFrameBytes,
		ProcessConnections:   c.ProcessConnections,
		UserConnections:      c.UserConnections,
		ComponentConnections: c.ComponentConnections,
	}
}

// DefaultPlatformComponentLogStreamLimits 是未配置日志依赖时使用的默认上限，
// 取值与 §6.3 参数表一致。
func DefaultPlatformComponentLogStreamLimits() PlatformComponentLogStreamLimits {
	return PlatformComponentLogStreamLimits{
		OpenTimeout:          platformComponentLogDefaultOpenTimeout,
		WriteTimeout:         platformComponentLogDefaultWriteTimeout,
		Heartbeat:            platformComponentLogDefaultHeartbeat,
		MaxFrameBytes:        platformComponentLogDefaultMaxFrame,
		ProcessConnections:   platformComponentLogDefaultProcessConns,
		UserConnections:      platformComponentLogDefaultUserConns,
		ComponentConnections: platformComponentLogDefaultCompConns,
	}
}

// PlatformComponentLogConfigFromEnv 读取并校验日志 reader 配置。
//
// 三种结果：
//   - 环境变量为空：返回 (零值, ErrPlatformComponentLogNotConfigured)，调用方用 nil
//     reader 表达"未启用"，接口回 503；这不是启动错误。
//   - 文件缺失/不可解析/字段非法：返回 error，调用方应启动失败，不猜默认值。
//   - 通过：返回校验后的配置。
func PlatformComponentLogConfigFromEnv() (PlatformComponentLogConfig, error) {
	path := strings.TrimSpace(os.Getenv(PlatformComponentLogConfigEnv))
	if path == "" {
		return PlatformComponentLogConfig{}, ErrPlatformComponentLogNotConfigured
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return PlatformComponentLogConfig{}, fmt.Errorf("read component log config: %w", err)
	}
	var file platformComponentLogConfigFile
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		// 未知键也在此被拒：旧草稿里的 lifetime_seconds 必须暴露成错误，
		// 而不是被静默忽略后让人误以为 8 秒可配。
		return PlatformComponentLogConfig{}, fmt.Errorf("parse component log config: %w", err)
	}
	return file.validate()
}

func (f platformComponentLogConfigFile) validate() (PlatformComponentLogConfig, error) {
	config := PlatformComponentLogConfig{}
	endpoint := strings.TrimSpace(f.OpenSearch.Endpoint)
	if !strings.HasPrefix(endpoint, "https://") {
		return config, errors.New("component log config: opensearch endpoint must be https")
	}
	if strings.Contains(endpoint, "?") || strings.Contains(endpoint, "#") {
		return config, errors.New("component log config: opensearch endpoint must not carry query or fragment")
	}
	config.Endpoint = strings.TrimSuffix(endpoint, "/")
	config.ServerName = strings.TrimSpace(f.OpenSearch.ServerName)
	if config.ServerName == "" {
		return config, errors.New("component log config: opensearch server_name is required")
	}
	config.CAFile = strings.TrimSpace(f.OpenSearch.CAFile)
	config.UsernameFile = strings.TrimSpace(f.OpenSearch.UsernameFile)
	config.PasswordFile = strings.TrimSpace(f.OpenSearch.PasswordFile)
	for name, value := range map[string]string{
		"ca_file": config.CAFile, "username_file": config.UsernameFile, "password_file": config.PasswordFile,
	} {
		if value == "" {
			return config, fmt.Errorf("component log config: opensearch %s is required", name)
		}
		if _, err := os.Stat(value); err != nil {
			return config, fmt.Errorf("component log config: opensearch %s unreadable: %w", name, err)
		}
	}
	config.IndexPrefix = strings.TrimSpace(f.OpenSearch.IndexPrefix)
	if config.IndexPrefix == "" {
		config.IndexPrefix = "ani-logs-"
	}
	// 索引前缀会拼进查询路径，只允许字母数字和连字符，避免路径注入。
	for _, r := range config.IndexPrefix {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '.' {
			return config, errors.New("component log config: index_prefix must be lowercase alphanumeric with - or .")
		}
	}
	config.RetentionDays = f.OpenSearch.RetentionDays
	if config.RetentionDays <= 0 {
		config.RetentionDays = 3
	}
	if config.RetentionDays > 30 {
		return config, errors.New("component log config: retention_days out of range")
	}

	config.QueryTimeout = platformComponentLogSeconds(f.OpenSearch.QueryTimeoutSeconds, platformComponentLogDefaultQueryTimeout)
	config.ResponseMaxBytes = platformComponentLogPositive(f.OpenSearch.ResponseMaxBytes, platformComponentLogDefaultResponseMax)
	config.OpenTimeout = platformComponentLogSeconds(f.Stream.OpenTimeoutSeconds, platformComponentLogDefaultOpenTimeout)
	config.WriteTimeout = platformComponentLogSeconds(f.Stream.WriteTimeoutSeconds, platformComponentLogDefaultWriteTimeout)
	config.Heartbeat = platformComponentLogSeconds(f.Stream.HeartbeatSeconds, platformComponentLogDefaultHeartbeat)
	config.ReplayLimitMax = platformComponentLogPositive(f.Stream.ReplayLimitMax, PlatformComponentLogMaxLimit)
	config.IncrementMax = platformComponentLogPositive(f.Stream.IncrementMax, platformComponentLogDefaultIncrement)
	config.Overlap = platformComponentLogSeconds(f.Stream.OverlapSeconds, platformComponentLogDefaultOverlap)
	config.MaxFrameBytes = platformComponentLogPositive(f.Stream.MaxFrameBytes, platformComponentLogDefaultMaxFrame)
	config.MaxBufferedBytes = platformComponentLogPositive(f.Stream.MaxBufferedBytes, platformComponentLogDefaultMaxBuffered)
	config.ProcessConnections = platformComponentLogPositive(f.Stream.ProcessConnections, platformComponentLogDefaultProcessConns)
	config.UserConnections = platformComponentLogPositive(f.Stream.UserConnections, platformComponentLogDefaultUserConns)
	config.ComponentConnections = platformComponentLogPositive(f.Stream.ComponentConnections, platformComponentLogDefaultCompConns)

	if config.ResponseMaxBytes > 64<<20 {
		return config, errors.New("component log config: response_max_bytes out of range")
	}
	if config.MaxFrameBytes < 1024 || config.MaxFrameBytes > config.ResponseMaxBytes {
		return config, errors.New("component log config: max_frame_bytes out of range")
	}
	if config.MaxBufferedBytes < 2*config.MaxFrameBytes || config.MaxBufferedBytes > 8<<20 {
		return config, errors.New("component log config: max_buffered_bytes out of range")
	}
	if config.Overlap <= 0 || config.Overlap > 5*time.Minute {
		return config, errors.New("component log config: overlap_seconds out of range")
	}
	if config.OpenTimeout <= 0 || config.OpenTimeout > 30*time.Second {
		return config, errors.New("component log config: open_timeout_seconds out of range")
	}
	if config.QueryTimeout <= 0 || config.QueryTimeout > config.OpenTimeout {
		return config, errors.New("component log config: query_timeout_seconds must be positive and not exceed open_timeout_seconds")
	}
	if config.WriteTimeout <= 0 || config.WriteTimeout > 30*time.Second {
		return config, errors.New("component log config: write_timeout_seconds out of range")
	}
	if config.Heartbeat <= 0 || config.Heartbeat > 30*time.Second {
		return config, errors.New("component log config: heartbeat_seconds out of range")
	}
	if config.ReplayLimitMax < 1 || config.ReplayLimitMax > PlatformComponentLogMaxLimit {
		return config, fmt.Errorf("component log config: replay_limit_max must be within 1..%d", PlatformComponentLogMaxLimit)
	}
	if config.IncrementMax < 1 || config.IncrementMax > 5000 {
		return config, errors.New("component log config: increment_max out of range")
	}
	if config.ProcessConnections < 1 || config.UserConnections < 1 || config.ComponentConnections < 1 {
		return config, errors.New("component log config: connection limits must be positive")
	}
	if config.UserConnections > config.ProcessConnections || config.ComponentConnections > config.ProcessConnections {
		return config, errors.New("component log config: per-user/per-component limits must not exceed process limit")
	}
	return config, nil
}

func platformComponentLogSeconds(value int, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Second
}

func platformComponentLogPositive(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
