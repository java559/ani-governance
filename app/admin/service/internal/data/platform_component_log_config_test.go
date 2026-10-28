package data

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// platformComponentLogFixtureConfig 生成一份合法配置 JSON，凭据路径指向 dir。
// 路径统一用正斜杠：Windows 的反斜杠在 JSON 字符串里是转义符，直接拼接会产出非法 JSON。
func platformComponentLogFixtureConfig(dir string) string {
	dir = filepath.ToSlash(dir)
	return `{
  "opensearch": {
    "endpoint": "https://ani-opensearch-master.ani-observability.svc.cluster.local:9200",
    "server_name": "ani-opensearch-master.ani-observability.svc.cluster.local",
    "ca_file": "` + dir + `/ca.crt",
    "username_file": "` + dir + `/username",
    "password_file": "` + dir + `/password",
    "index_prefix": "ani-logs-",
    "retention_days": 3,
    "query_timeout_seconds": 2,
    "response_max_bytes": 4194304
  },
  "stream": {
    "open_timeout_seconds": 3,
    "write_timeout_seconds": 2,
    "heartbeat_seconds": 2,
    "replay_limit_max": 200,
    "increment_max": 500,
    "overlap_seconds": 30,
    "max_frame_bytes": 262144,
    "max_buffered_bytes": 1048576,
    "process_connections": 16,
    "user_connections": 4,
    "component_connections": 4
  }
}`
}

// writePlatformComponentLogConfig 在临时目录写凭据与配置文件，返回配置路径与目录。
// CA 内容无需是真证书：本文件只测配置校验，TLS 握手由 reader 测试覆盖。
func writePlatformComponentLogConfig(t *testing.T, mutate func(string) string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"ca.crt", "username", "password"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture-"+name+"\n"), 0o440); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	body := platformComponentLogFixtureConfig(dir)
	if mutate != nil {
		body = mutate(body)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o440); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestPlatformComponentLogConfigNotConfigured 验证"环境变量未设置"不是启动错误，
// 而是显式的未启用状态：接口据此回 503，状态接口不受影响。
func TestPlatformComponentLogConfigNotConfigured(t *testing.T) {
	t.Setenv(PlatformComponentLogConfigEnv, "")
	config, err := PlatformComponentLogConfigFromEnv()
	if !errors.Is(err, ErrPlatformComponentLogNotConfigured) {
		t.Fatalf("unset env must report not-configured, got %v", err)
	}
	if config.Endpoint != "" {
		t.Fatalf("not-configured must return zero config, got %+v", config)
	}
}

// TestPlatformComponentLogConfigLoadsValidFile 验证合法配置逐项映射到运行期结构。
func TestPlatformComponentLogConfigLoadsValidFile(t *testing.T) {
	t.Setenv(PlatformComponentLogConfigEnv, writePlatformComponentLogConfig(t, nil))
	config, err := PlatformComponentLogConfigFromEnv()
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if config.Endpoint != "https://ani-opensearch-master.ani-observability.svc.cluster.local:9200" {
		t.Fatalf("endpoint = %q", config.Endpoint)
	}
	if config.QueryTimeout != 2*time.Second || config.OpenTimeout != 3*time.Second {
		t.Fatalf("timeouts = %v/%v", config.QueryTimeout, config.OpenTimeout)
	}
	if config.ProcessConnections != 16 || config.UserConnections != 4 || config.ComponentConnections != 4 {
		t.Fatalf("connection limits = %d/%d/%d", config.ProcessConnections, config.UserConnections, config.ComponentConnections)
	}
	limits := config.StreamLimits()
	if limits.MaxFrameBytes != 262144 || limits.Heartbeat != 2*time.Second {
		t.Fatalf("stream limits = %+v", limits)
	}
}

// TestPlatformComponentLogConfigRejectsLifetimeSeconds 是本方案的硬约定：
// 8 秒是契约常量，旧草稿里的 lifetime_seconds 必须报错而不是被静默忽略，
// 否则运维会误以为该值生效。
func TestPlatformComponentLogConfigRejectsLifetimeSeconds(t *testing.T) {
	t.Setenv(PlatformComponentLogConfigEnv, writePlatformComponentLogConfig(t, func(body string) string {
		return strings.Replace(body, `"heartbeat_seconds": 2,`, "\"heartbeat_seconds\": 2,\n    \"lifetime_seconds\": 30,", 1)
	}))
	if _, err := PlatformComponentLogConfigFromEnv(); err == nil {
		t.Fatal("lifetime_seconds must be rejected, not silently ignored")
	}
}

// TestPlatformComponentLogConfigRejectsInvalidInputs 覆盖明文、缺凭据文件、
// 非法索引前缀与越界预算；全部必须在启动期失败，不猜默认值。
func TestPlatformComponentLogConfigRejectsInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(string) string
	}{
		{"plain_http", func(body string) string {
			return strings.Replace(body, "https://ani-opensearch", "http://ani-opensearch", 1)
		}},
		{"missing_server_name", func(body string) string {
			return strings.Replace(body, `"server_name": "ani-opensearch-master.ani-observability.svc.cluster.local",`, `"server_name": "",`, 1)
		}},
		{"unreadable_ca", func(body string) string {
			return strings.Replace(body, `"ca_file": "`, `"ca_file": "/nonexistent/`, 1)
		}},
		{"index_prefix_injection", func(body string) string {
			return strings.Replace(body, `"index_prefix": "ani-logs-",`, `"index_prefix": "ani-logs-*/../*",`, 1)
		}},
		{"retention_out_of_range", func(body string) string {
			return strings.Replace(body, `"retention_days": 3,`, `"retention_days": 90,`, 1)
		}},
		{"query_timeout_exceeds_open", func(body string) string {
			return strings.Replace(body, `"query_timeout_seconds": 2,`, `"query_timeout_seconds": 9,`, 1)
		}},
		{"frame_above_response", func(body string) string {
			return strings.Replace(body, `"max_frame_bytes": 262144,`, `"max_frame_bytes": 8388608,`, 1)
		}},
		{"buffered_below_two_frames", func(body string) string {
			return strings.Replace(body, `"max_buffered_bytes": 1048576,`, `"max_buffered_bytes": 262144,`, 1)
		}},
		{"user_above_process", func(body string) string {
			return strings.Replace(body, `"user_connections": 4,`, `"user_connections": 32,`, 1)
		}},
		{"unknown_key", func(body string) string {
			return strings.Replace(body, `"index_prefix": "ani-logs-",`, "\"index_prefix\": \"ani-logs-\",\n    \"unknown_key\": true,", 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(PlatformComponentLogConfigEnv, writePlatformComponentLogConfig(t, tc.mutate))
			if _, err := PlatformComponentLogConfigFromEnv(); err == nil {
				t.Fatalf("%s must be rejected", tc.name)
			}
		})
	}
}

// TestPlatformComponentLogConfigMissingFile 验证路径存在但文件缺失时启动失败。
func TestPlatformComponentLogConfigMissingFile(t *testing.T) {
	t.Setenv(PlatformComponentLogConfigEnv, filepath.Join(t.TempDir(), "absent.json"))
	if _, err := PlatformComponentLogConfigFromEnv(); err == nil {
		t.Fatal("missing config file must fail startup")
	}
}

// TestDefaultPlatformComponentLogStreamLimits 验证未配置时的默认上限与 §6.3 一致。
func TestDefaultPlatformComponentLogStreamLimits(t *testing.T) {
	limits := DefaultPlatformComponentLogStreamLimits()
	if limits.ProcessConnections != 16 || limits.UserConnections != 4 || limits.ComponentConnections != 4 {
		t.Fatalf("default connection limits = %+v", limits)
	}
	if limits.OpenTimeout != 3*time.Second || limits.WriteTimeout != 2*time.Second || limits.Heartbeat != 2*time.Second {
		t.Fatalf("default timeouts = %+v", limits)
	}
	if limits.MaxFrameBytes != 256<<10 {
		t.Fatalf("default max frame = %d", limits.MaxFrameBytes)
	}
}
