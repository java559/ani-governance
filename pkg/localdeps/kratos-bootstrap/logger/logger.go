package logger

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/go-kratos/kratos/v2/log"

	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
)

// NewLogger 动态创建日志实例，返回项目 Logger 接口。
//
// 内部通过工厂注册表查找后端（zap/logrus/fluent 等）。
// 工厂现在直接返回项目 Logger 接口，无需 Kratos 适配。
func NewLogger(cfg *conf.Logger) (Logger, error) {
	if cfg == nil {
		return nil, nil
	}

	if cfg.GetType() == "" || cfg.GetType() == string(Std) {
		return NewStdLogger(), nil
	}

	// normalize to lower case for lookup
	typ := Type(strings.ToLower(cfg.GetType()))

	f, ok := GetFactory(typ)
	if !ok {
		available := ListFactories()
		strs := make([]string, 0, len(available))
		for _, t := range available {
			strs = append(strs, string(t))
		}
		sort.Strings(strs)
		return nil, fmt.Errorf("unsupported logger type: %s; available: %v", typ, strs)
	}

	lg, err := f(cfg)
	if err != nil {
		return nil, fmt.Errorf("create logger %s: %w", typ, err)
	}
	if lg == nil {
		return nil, fmt.Errorf("logger factory %s returned nil logger", typ)
	}
	return lg, nil
}

// NewKratosLogger 动态创建日志实例，返回 Kratos log.Logger。
// 旧 API 兼容入口，内部通过 AsKratosLogger 适配项目 Logger。
// 新代码请使用 [NewLogger]。
func NewKratosLogger(cfg *conf.Logger) (log.Logger, error) {
	lg, err := NewLogger(cfg)
	if err != nil {
		return nil, err
	}
	if lg == nil {
		return nil, nil
	}
	return AsKratosLogger(lg), nil
}

// NewLoggerProvider 创建主服务日志记录器，返回项目 Logger 接口。
//
// 主进程固定使用单行 JSON stdout handler（与早期初始化 logger 同源），
// 并附加服务身份字段：service.name / service.version / service.instance.id。
// Kratos 原生字段 ts / caller / service.id 不再输出，改由 handler 统一映射
// 为 time / source / service.instance.id（见 json.go）。
//
// 防御性行为保留：cfg 或 appInfo 为空、或后端创建失败时回退到同一 JSON logger，
// 但绝不回退到 nop —— 缺少日志会让客户侧彻底看不到错误。
func NewLoggerProvider(cfg *conf.Logger, appInfo *conf.AppInfo) Logger {
	l := NewStdLogger()

	fields := make([]any, 0, 6)
	if appInfo != nil {
		if name := appInfo.GetAppId(); name != "" {
			fields = append(fields, "service.name", name)
		}
		if version := appInfo.GetVersion(); version != "" {
			fields = append(fields, "service.version", version)
		}
		if instance := appInfo.GetInstanceId(); instance != "" {
			fields = append(fields, "service.instance.id", instance)
		}
	}
	if len(fields) > 0 {
		l = l.With(fields...)
	}

	// 自动注入 trace_id / span_id（从 Logger 接口的 ctx 参数提取）
	return WrapTrace(l)
}

// NewKratosStdLogger 创建一个 Kratos 内置的控制台日志记录器。
// 旧 API 兼容入口，新代码请使用 [NewStdLogger]（slog 后端，无 Kratos 依赖）。
func NewKratosStdLogger() log.Logger {
	return log.NewStdLogger(os.Stdout)
}
