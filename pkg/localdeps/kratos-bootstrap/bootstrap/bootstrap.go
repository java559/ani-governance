package bootstrap

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/spf13/cobra"

	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/transport"

	bConfig "go-wind-admin/pkg/localdeps/kratos-bootstrap/config"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	bRegistry "go-wind-admin/pkg/localdeps/kratos-bootstrap/registry"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/tracer"
	"go-wind-admin/pkg/telemetry"
)

// ready marks that the application finished startup. It distinguishes
// process.start_failed from process.failed on the terminal error path.
var ready atomic.Bool

// Ready reports whether the application reached its AfterStart hook.
func Ready() bool { return ready.Load() }

// ProcessStep returns the event name for a terminal startup/runtime error.
func ProcessStep() string {
	if ready.Load() {
		return "process.failed"
	}
	return "process.start_failed"
}

// NewApp 创建应用程序
func NewApp(ctx *Context, srv ...transport.Server) *kratos.App {
	var opts []kratos.Option
	if ctx.logger != nil {
		// 项目 Logger 通过 AsKratosLogger 适配为 Kratos log.Logger
		opts = append(opts, kratos.Logger(bLogger.AsKratosLogger(ctx.logger)))
		opts = append(opts, kratos.AfterStart(func(context.Context) error {
			ready.Store(true)
			ctx.logger.Info(context.Background(), "service ready", "event", "process.ready")
			return nil
		}))
		opts = append(opts, kratos.BeforeStop(func(context.Context) error {
			ctx.logger.Info(context.Background(), "service stopping", "event", "process.stopping")
			return nil
		}))
		opts = append(opts, kratos.AfterStop(func(context.Context) error {
			ctx.logger.Info(context.Background(), "service stopped", "event", "process.stopped")
			return nil
		}))
	}
	if ctx.registrar != nil {
		opts = append(opts, kratos.Registrar(ctx.registrar))
	}
	if len(srv) > 0 {
		opts = append(opts, kratos.Server(srv...))
	}

	if ctx.appInfo.Metadata != nil {
		opts = append(opts, kratos.Metadata(ctx.appInfo.Metadata))
	}
	if ctx.appInfo.AppId != "" {
		registerName := ctx.appInfo.Project + "/" + ctx.appInfo.AppId
		// 根据注册中心类型规范化 AppId
		if bConfig.GetBootstrapConfig().Registry != nil && bConfig.GetBootstrapConfig().Registry.GetType() != "" {
			registerName = bRegistry.NormalizeForRegistry(registerName, bConfig.GetBootstrapConfig().Registry.GetType())
		}
		opts = append(opts, kratos.Name(registerName))
	}
	if ctx.appInfo.Version != "" {
		opts = append(opts, kratos.Version(ctx.appInfo.Version))
	}
	if ctx.appInfo.InstanceId != "" {
		opts = append(opts, kratos.ID(ctx.appInfo.InstanceId))
	}

	return kratos.New(opts...)
}

// RunApp 运行应用程序并允许在执行前对 root 命令做定制。
// opts 可用于注册子命令、对 root 添加 flag 或其他修改。
func RunApp(ctx *Context, initApp InitAppFunc, opts ...func(root *cobra.Command)) error {
	if ctx == nil {
		return fmt.Errorf("bootstrap context is nil")
	}

	// 注入命令行参数
	root := NewRootCmd(flags, func(cmd *cobra.Command, args []string) error {
		return bootstrap(ctx, initApp)
	})

	// 允许调用方定制 root（如添加子命令、注册额外 flag 等）
	for _, opt := range opts {
		if opt != nil {
			opt(root)
		}
	}

	// 如果 flags 实现了 Register，就在 Execute 前注册到命令上，确保 cobra 能解析这些 flag
	if rb, ok := interface{}(flags).(interface{ Register(cmd *cobra.Command) }); ok {
		rb.Register(root)
	}

	if err := root.Execute(); err != nil {
		return err
	}
	return nil
}

// bootstrap 应用引导启动
func bootstrap(ctx *Context, initApp InitAppFunc) error {
	// 打印应用信息（同字段的 JSON 记录，不再混入无 level 的裸文本）
	ctx.PrintAppInfo()

	var err error

	// load configs
	if err = bConfig.LoadBootstrapConfig(flags.Conf); err != nil {
		return startupError(ctx, "config", err)
	}

	// get bootstrap config
	ctx.config = bConfig.GetBootstrapConfig()
	if ctx.config == nil {
		return startupError(ctx, "config", fmt.Errorf("bootstrap config is nil"))
	}

	// init logger: 最终 logger 接管后再次设全局同源 logger，
	// 让 Kratos 全局 logger 与标准 slog 与项目 logger 保持一致。
	ctx.logger = bLogger.NewLoggerProvider(ctx.config.Logger, ctx.appInfo)
	if ctx.logger == nil {
		return startupError(ctx, "logger", fmt.Errorf("init logger failed"))
	}
	_, stdLogger := bLogger.NewRuntimeLogger(ctx.appInfo, bLogger.RuntimeLevel())
	bLogger.InstallGlobals(ctx.logger, stdLogger)

	// init registrar
	ctx.registrar, err = bRegistry.NewRegistrar(ctx.config.Registry)
	if err != nil {
		return startupError(ctx, "registry", err)
	}

	// init tracer
	if err = tracer.NewTracerProvider(ctx.Context(), ctx.config.Trace, ctx.appInfo); err != nil {
		return startupError(ctx, "tracer", err)
	}

	// init app
	app, cleanup, err := initApp(ctx)
	if err != nil {
		return startupError(ctx, "clients", err)
	}
	defer cleanup()

	// run the app.
	if err = app.Run(); err != nil {
		return startupError(ctx, "server", err)
	}

	return nil
}

// startupError records the failed initialisation stage with its real error
// before returning it along the original failure path.
func startupError(ctx *Context, step string, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.logger != nil {
		fields := append([]any{"event", ProcessStep(), "step", step}, telemetry.ErrorFields(err)...)
		ctx.logger.Error(context.Background(), "startup failed", fields...)
	}
	return err
}
