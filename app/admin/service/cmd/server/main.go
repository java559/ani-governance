package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/transport"
	"github.com/go-kratos/kratos/v2/transport/http"
	"go-wind-admin/pkg/localdeps/kratos-transport/transport/asynq"
	"go-wind-admin/pkg/localdeps/kratos-transport/transport/sse"

	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/telemetry"
	//_ "github.com/tx7do/kratos-bootstrap/config/consul"
	//_ "github.com/tx7do/kratos-bootstrap/config/etcd"
	//_ "github.com/tx7do/kratos-bootstrap/config/kubernetes"
	//_ "github.com/tx7do/kratos-bootstrap/config/nacos"
	//_ "github.com/tx7do/kratos-bootstrap/config/polaris"

	//_ "github.com/tx7do/kratos-bootstrap/logger/aliyun"
	//_ "github.com/tx7do/kratos-bootstrap/logger/fluent"
	//_ "github.com/tx7do/kratos-bootstrap/logger/logrus"
	//_ "github.com/tx7do/kratos-bootstrap/logger/tencent"
	//_ "github.com/tx7do/kratos-bootstrap/logger/zap"
	//_ "github.com/tx7do/kratos-bootstrap/logger/zerolog"

	//_ "github.com/tx7do/kratos-bootstrap/registry/consul"
	//_ "github.com/tx7do/kratos-bootstrap/registry/etcd"
	//_ "github.com/tx7do/kratos-bootstrap/registry/eureka"
	//_ "github.com/tx7do/kratos-bootstrap/registry/kubernetes"
	//_ "github.com/tx7do/kratos-bootstrap/registry/nacos"
	//_ "github.com/tx7do/kratos-bootstrap/registry/polaris"
	//_ "github.com/tx7do/kratos-bootstrap/registry/servicecomb"
	//_ "github.com/tx7do/kratos-bootstrap/registry/zookeeper"

	//_ "github.com/tx7do/kratos-bootstrap/tracer"

	appCrypto "go-wind-admin/pkg/crypto"
	"go-wind-admin/pkg/serviceid"
)

var version = "1.0.0"

// go build -ldflags "-X main.version=x.y.z"

func newApp(
	ctx *bootstrap.Context,
	hs *http.Server,
	as *asynq.Server,
	ss *sse.Server,
	extraServers ...transport.Server,
) *kratos.App {
	// asynq / sse 在配置缺失时返回 nil（typed-nil）。
	// kratos 的 app.Run() 会对每个 server 调 Start()/Stop()，
	// 把 typed-nil 指针当作 transport.Server 解引用会 panic。
	// 因此这里跳过未配置的服务器，使"未配置 asynq/SSE"的部署能正常启动。
	servers := []transport.Server{hs}
	if as != nil {
		servers = append(servers, as)
	}
	if ss != nil {
		servers = append(servers, ss)
	}
	for _, s := range extraServers {
		if s != nil {
			servers = append(servers, s)
		}
	}
	return bootstrap.NewApp(ctx, servers...)
}

// appInfo returns the single AppInfo used by both the early logger and the
// bootstrap context, so process.starting and process.ready report the same
// instance identity. The id is generated locally; it never queries Kubernetes.
func appInfo() *conf.AppInfo {
	ai := &conf.AppInfo{
		Project: serviceid.ProjectName,
		AppId:   "ani-governance", // Runtime name; retain upstream role codes and Redis key namespace.
		Version: version,
	}
	bootstrap.AdjustAppInfo(ai)
	return ai
}

func runApp() error {
	// 日志必须在读取配置、数据库、证书与业务组件之前可用。
	info := appInfo()
	level, levelErr := bLogger.ParseRuntimeLevel(os.Getenv("ANI_LOG_LEVEL"))
	logger, stdLogger := bLogger.NewRuntimeLogger(info, level)
	bLogger.InstallGlobals(logger, stdLogger)

	if levelErr != nil {
		// 非法阈值不得静默降级：先记录启动失败，再按原失败路径退出。
		logger.Error(context.Background(), "invalid log configuration",
			"event", "process.start_failed", "step", "logger",
			"error", levelErr.Error(), "error_type", "config", "error_source", "local",
			"log_format", "json")
		return levelErr
	}

	logger.Info(context.Background(), "service starting",
		"event", "process.starting", "log_format", "json", "log_level", levelName(level))

	// 初始化全局加密器（供 task payload 等路径对敏感配置做 AES-GCM 加密）。
	// 通过环境变量 GOWIND_CRYPTO_KEY 提供密钥；未设置时加密功能禁用（no-op）。
	// 注意：EncryptPayload 只在真正加密时才标记 IsEncryptedKey，因此未配置密钥
	// 时 payload 以明文存储且不会被误判为已加密——调用方无需额外处理。
	initGlobalEncryptorFromEnv(logger)

	// 带上已建好的 JSON logger：bootstrap 内的 PrintAppInfo 在配置加载与
	// logger provider 初始化之前执行，否则会落到纯文本兜底分支，破坏
	// 「stdout 只输出单行 JSON」的合同要求。
	ctx := bootstrap.NewContextWithParam(context.Background(), info, nil, logger)
	return bootstrap.RunApp(ctx, initApp)
}

func levelName(level slog.Level) string {
	if level < slog.LevelInfo {
		return "DEBUG"
	}
	return "INFO"
}

// initGlobalEncryptorFromEnv 从环境变量读取加密密钥并初始化全局加密器。
func initGlobalEncryptorFromEnv(logger bLogger.Logger) {
	key := os.Getenv("GOWIND_CRYPTO_KEY")
	if key == "" {
		logger.Warn(context.Background(), "GOWIND_CRYPTO_KEY not set, global encryption disabled (payloads stored in plaintext)")
		return
	}
	if err := appCrypto.InitGlobalEncryptor(key, true); err != nil {
		logger.Error(context.Background(), "init global encryptor failed, encryption disabled",
			telemetry.ErrorFields(fmt.Errorf("init global encryptor: %w", err))...)
	}
}

func main() {
	if err := safely(runApp); err != nil {
		reportExit(err)
		os.Exit(1)
	}
}

// safely converts a panic in the startup path into a returned error so it can
// be recorded with its real stack instead of a bare runtime trace.
func safely(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &panicError{value: fmt.Sprint(r), stack: string(debug.Stack())}
		}
	}()
	return fn()
}

type panicError struct {
	value string
	stack string
}

func (e *panicError) Error() string { return "panic: " + e.value }

// reportExit records the terminal failure with the real error and, for a panic,
// the captured stack. It runs after the logger has been installed; a failure
// before that still reaches stderr through the default logger.
func reportExit(err error) {
	logger := bLogger.GetLogger()
	var pe *panicError
	if peErr, ok := err.(*panicError); ok {
		pe = peErr
	}
	if logger == nil {
		return
	}
	if pe != nil {
		logger.Error(context.Background(), "service panicked",
			append([]any{"event", "process.panic", "error_type", "panic", "error_source", "local", "stack", pe.stack},
				telemetry.ErrorFields(err)...)...)
		return
	}
	logger.Error(context.Background(), "service terminated",
		append([]any{"event", "process.start_failed", "step", "startup"},
			telemetry.ErrorFields(err)...)...)
}
