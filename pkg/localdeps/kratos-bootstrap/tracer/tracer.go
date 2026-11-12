package tracer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	traceSdk "go.opentelemetry.io/otel/sdk/trace"
	semConv "go.opentelemetry.io/otel/semconv/v1.4.0"

	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
)

var (
	// tpInstance holds the currently active global tracer provider (interface type, nil-able)
	tpMu       sync.Mutex
	tpInstance *traceSdk.TracerProvider
)

// NewTracerExporter 构建 exporter：优先使用注册表中的 factory。
// exporterName 不能为空，cfg 传入给 factory 用于读取 endpoint/insecure/headers 等信息。
func NewTracerExporter(ctx context.Context, cfg *conf.Tracer) (traceSdk.SpanExporter, error) {
	if cfg == nil {
		return nil, errors.New("tracer: tracer cfg is nil")
	}
	if cfg.GetExporter() == "" {
		return nil, errors.New("tracer: exporter name is empty")
	}

	if f, ok := GetExporterFactory(cfg.GetExporter()); ok {
		return f(ctx, cfg)
	}
	return nil, fmt.Errorf("tracer: unknown exporter %q; available: %v", cfg.GetExporter(), ListExporterNames())
}

// ShutdownTracerProvider gracefully shuts down the active global tracer provider (if set).
// Safe to call multiple times; returns error if shutdown fails.
func ShutdownTracerProvider(ctx context.Context) error {
	tpMu.Lock()
	defer tpMu.Unlock()
	if tpInstance == nil {
		return nil
	}
	if err := tpInstance.Shutdown(ctx); err != nil {
		return err
	}
	tpInstance = nil
	return nil
}

// NewTracerProvider 创建 tracer provider 并设置为全局 provider。
// 注：为了更好的资源管理，可以使用 NewTracerProviderWithShutdown 获得 shutdown 函数。
func NewTracerProvider(ctx context.Context, cfg *conf.Tracer, appInfo *conf.AppInfo) error {
	_, _, err := NewTracerProviderWithShutdown(ctx, cfg, appInfo)
	return err
}

// NewTracerProviderWithShutdown 返回 (tp, shutdownFunc, error)，推荐在 main 中使用并在退出时调用 shutdownFunc(ctx)
//
// 即使没有配置 exporter，也会建立有效的 SDK TracerProvider 并固定启用 W3C
// TraceContext 传播：本期的日志关联依赖有效的 span 上下文，而导出器不是前置条件。
func NewTracerProviderWithShutdown(ctx context.Context, cfg *conf.Tracer, appInfo *conf.AppInfo) (*traceSdk.TracerProvider, func(context.Context) error, error) {
	// do not mutate caller cfg; use local defaults
	sampler := 1.0
	env := "dev"
	if cfg != nil {
		if value := cfg.GetSampler(); value != 0 {
			sampler = value
		}
		if value := cfg.GetEnv(); value != "" {
			env = value
		}
	}

	var attrs []attribute.KeyValue
	attrs = append(attrs, attribute.String("service.env", env))
	if appInfo != nil {
		attrs = append(attrs,
			semConv.ServiceNamespaceKey.String(appInfo.GetProject()),
			semConv.ServiceNameKey.String(appInfo.GetAppId()),
			semConv.ServiceVersionKey.String(appInfo.GetVersion()),
			semConv.ServiceInstanceIDKey.String(appInfo.GetInstanceId()),
		)
	}

	opts := []traceSdk.TracerProviderOption{
		traceSdk.WithSampler(traceSdk.ParentBased(traceSdk.TraceIDRatioBased(sampler))),
		traceSdk.WithResource(resource.NewSchemaless(attrs...)),
	}

	// NOTE: cfg.GetBatcher() historically used as exporter name in this project.
	// Consider renaming conf.Tracer fields to `Exporter`/`ExporterName` in the future.
	if cfg != nil && len(cfg.GetEndpoint()) > 0 {
		exp, err := NewTracerExporter(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		opts = append(opts, traceSdk.WithBatcher(exp))
	}

	tp := traceSdk.NewTracerProvider(opts...)

	// defensive check (NewTracerProvider does not return nil in normal cases)
	if tp == nil {
		return nil, nil, errors.New("tracer: create tracer provider failed")
	}

	// set global provider and keep reference for shutdown
	tpMu.Lock()
	// shutdown previous provider if present
	if tpInstance != nil {
		_ = tpInstance.Shutdown(ctx)
	}
	tpInstance = tp
	tpMu.Unlock()

	// set global tracer provider
	otel.SetTracerProvider(tp)

	// W3C TraceContext 固定启用；baggage 仅在配置显式打开时附加。
	enableTraceContext := true
	enableBaggage := false
	if cfg != nil {
		enableBaggage = cfg.GetEnableBaggage()
	}
	otel.SetTextMapPropagator(NewCompositePropagator(enableTraceContext, enableBaggage))

	shutdown := func(c context.Context) error {
		// shutdown global provider if it's still the same one
		return ShutdownTracerProvider(c)
	}

	return tp, shutdown, nil
}

// NewCompositePropagator 构建一个复合 propagator。
// - enableTraceContext: 是否包含 W3C TraceContext
// - enableBaggage: 是否包含 Baggage
// 返回值保证非 nil，默认回退为 TraceContext。
func NewCompositePropagator(enableTraceContext, enableBaggage bool) propagation.TextMapPropagator {
	var parts []propagation.TextMapPropagator
	if enableTraceContext {
		parts = append(parts, propagation.TraceContext{})
	}
	if enableBaggage {
		parts = append(parts, propagation.Baggage{})
	}

	switch len(parts) {
	case 0:
		return propagation.TraceContext{}
	case 1:
		return parts[0]
	default:
		return propagation.NewCompositeTextMapPropagator(parts...)
	}
}
