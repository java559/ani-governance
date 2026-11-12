package logger

import (
	"io"
	"log/slog"
	"os"

	"github.com/go-kratos/kratos/v2/log"

	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
)

// newJSON builds the project Logger and the equivalent stdlib logger from one
// handler, so project, Kratos and slog calls all emit the same JSON shape.
func newJSON(writer io.Writer, level slog.Leveler) (*stdLogger, *slog.Logger) {
	if writer == nil {
		writer = io.Discard
	}
	if level == nil {
		level = slog.LevelInfo
	}
	base := slog.NewJSONHandler(writer, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: replaceContractAttr,
	})
	sl := slog.New(&contractHandler{base: base})
	return &stdLogger{l: sl}, sl
}

// NewJSONLogger returns a Logger that emits one single-line JSON object per
// record to writer.
func NewJSONLogger(writer io.Writer, level slog.Leveler) Logger {
	l, _ := newJSON(writer, level)
	return l
}

// NewRuntimeLogger builds the process logger used before configuration is
// loaded and after the final provider takes over.
//
// It writes to stdout, carries the fixed service identity, and never falls back
// to a nop logger: a missing logger is exactly the failure this phase fixes.
func NewRuntimeLogger(appInfo *conf.AppInfo, level slog.Level) (Logger, *slog.Logger) {
	l, sl := newJSON(os.Stdout, level)
	fields := identityFields(appInfo)
	if len(fields) > 0 {
		l = l.With(fields...).(*stdLogger)
		sl = sl.With(fields...)
	}
	return WrapTrace(l), sl
}

// identityFields renders the mandatory service identity attributes.
func identityFields(appInfo *conf.AppInfo) []any {
	if appInfo == nil {
		return []any{"service.name", "unknown", "service.version", "unknown", "service.instance.id", "unknown"}
	}
	name := appInfo.GetAppId()
	if name == "" {
		name = "unknown"
	}
	version := appInfo.GetVersion()
	if version == "" {
		version = "unknown"
	}
	instance := appInfo.GetInstanceId()
	if instance == "" {
		instance = "unknown"
	}
	return []any{
		"service.name", name,
		"service.version", version,
		"service.instance.id", instance,
	}
}

// RuntimeLevel returns the effective process level from ANI_LOG_LEVEL. An
// invalid value has already failed startup, so the final provider only needs a
// best-effort level here.
func RuntimeLevel() slog.Level {
	level, err := ParseRuntimeLevel(os.Getenv("ANI_LOG_LEVEL"))
	if err != nil {
		return slog.LevelInfo
	}
	return level
}

// InstallGlobals makes l the process-wide logger for the project interface, the
// Kratos global logger and the standard library slog default.
func InstallGlobals(l Logger, sl *slog.Logger) {
	if l == nil {
		return
	}
	SetLogger(l)
	if kl := AsKratosLogger(l); kl != nil {
		log.SetLogger(kl)
	}
	if sl != nil {
		slog.SetDefault(sl)
	}
}
