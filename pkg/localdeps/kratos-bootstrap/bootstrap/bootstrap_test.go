package bootstrap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-kratos/kratos/v2"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

func initApp(ctx *Context) (*kratos.App, func(), error) {
	app := NewApp(ctx)
	return app, func() {
	}, nil
}

// Environment markers used to run the real bootstrap path in a child process.
const (
	bootstrapChildEnv   = "ANI_BOOTSTRAP_NAME_VERSION_CHILD"
	bootstrapConfEnv    = "ANI_BOOTSTRAP_NAME_VERSION_CONF"
	bootstrapObserved   = "bootstrap observed "
	minimalBootstrapYML = "env: unittest\n"
)

func TestBootstrapWithNameVersion(t *testing.T) {
	if os.Getenv(bootstrapChildEnv) == "1" {
		runBootstrapWithNameVersionChild(t)
		return
	}

	// The bootstrap path fills package-level state that a test cannot reset:
	// the parsed-config singleton in bConfig and the command flags bound to the
	// root cobra command. Running the real path in a child test binary keeps the
	// parent process, and the other tests in this file, untouched.
	confPath := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(confPath, []byte(minimalBootstrapYML), 0o600); err != nil {
		t.Fatalf("write the temporary bootstrap config: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestBootstrapWithNameVersion", "-test.v")
	cmd.Env = append(os.Environ(), bootstrapChildEnv+"=1", bootstrapConfEnv+"="+confPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the child run of the real bootstrap path failed: %v\n%s", err, out)
	}
	// The child asserts the values itself; the parent only requires that it ran
	// them to completion and reported the observed name and version.
	for _, want := range []string{
		bootstrapObserved,
		`version="v0.0.1"`,
		"--- PASS: TestBootstrapWithNameVersion",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("child output is missing %q\n%s", want, out)
		}
	}
}

// runBootstrapWithNameVersionChild keeps the original semantics of this test --
// build a Context with an app id and version, hand it to RunApp through the real
// config-loading path and require a nil return -- while making the run
// self-contained: a temporary config file, no registry, tracer, daemon or
// listener, and a bounded stop driven by the server lifecycle itself.
func runBootstrapWithNameVersionChild(t *testing.T) {
	confPath := os.Getenv(bootstrapConfEnv)
	if confPath == "" {
		t.Fatalf("%s is not set", bootstrapConfEnv)
	}

	serviceName := "test"
	version := "v0.0.1"

	ctx := NewContext(context.Background(), &conf.AppInfo{
		Project: "",
		AppId:   serviceName,
		Version: version,
	})

	srv := &lifecycleServer{started: make(chan struct{})}
	var app *kratos.App

	err := RunApp(ctx, func(c *Context) (*kratos.App, func(), error) {
		created := NewApp(c, srv)
		app = created
		// Close the run through the app's own Stop once the server reports that
		// it started: no sleeping, no signal sent to the process.
		go func() {
			<-srv.started
			if stopErr := created.Stop(); stopErr != nil {
				t.Errorf("Stop: %v", stopErr)
			}
		}()
		return created, func() { srv.cleanup.Store(true) }, nil
	}, func(root *cobra.Command) {
		// SetArgs on the root command is the supported hook: it replaces the
		// ../../configs default and the inherited `go test` arguments.
		root.SetArgs([]string{"--conf", confPath, "--env", "unittest", "--daemon=false"})
	})
	if err != nil {
		t.Fatalf("RunApp: %v", err)
	}

	// NewApp composes the registered name as Project + "/" + AppId, and
	// AdjustAppInfo supplies the project default when the caller leaves it empty.
	t.Logf("%sname=%q version=%q", bootstrapObserved, app.Name(), app.Version())
	assert.NotEmpty(t, ctx.appInfo.Project, "the app info should carry a project after the default adjustment")
	assert.Equal(t, ctx.appInfo.Project+"/"+serviceName, app.Name())
	assert.Equal(t, version, app.Version())
	assert.True(t, srv.startCalled.Load(), "the test server was never started")
	assert.True(t, srv.stopCalled.Load(), "the test server was never stopped, so the run was not bounded by Stop")
	assert.True(t, srv.cleanup.Load(), "the cleanup func returned by initApp never ran")
}

// lifecycleServer is a transport.Server that reports its own start and stop, so a
// bootstrap run can be bounded without touching the network or the process signal
// handlers.
type lifecycleServer struct {
	started chan struct{}
	once    sync.Once

	startCalled atomic.Bool
	stopCalled  atomic.Bool
	cleanup     atomic.Bool
}

func (s *lifecycleServer) Start(context.Context) error {
	s.startCalled.Store(true)
	s.once.Do(func() { close(s.started) })
	return nil
}

func (s *lifecycleServer) Stop(context.Context) error {
	s.stopCalled.Store(true)
	return nil
}

func TestNewInstanceId(t *testing.T) {
	instanceId := NewInstanceId("gowind-test-service", "1.0.0", "127.0.0.1", "8000")
	t.Logf("InstanceId: %s", instanceId)
}

// captureLogger records Info calls so the startup banner can be asserted without
// capturing process stdout.
type captureLogger struct {
	infos []capturedInfo
}

type capturedInfo struct {
	msg     string
	keyvals []any
}

func (c *captureLogger) Debug(context.Context, string, ...any) {}
func (c *captureLogger) Warn(context.Context, string, ...any)  {}
func (c *captureLogger) Error(context.Context, string, ...any) {}
func (c *captureLogger) Info(_ context.Context, msg string, keyvals ...any) {
	c.infos = append(c.infos, capturedInfo{msg: msg, keyvals: keyvals})
}
func (c *captureLogger) With(...any) bLogger.Logger { return c }

// TestPrintAppInfoUsesStructuredLogger pins the wiring that keeps the startup
// banner off the plain-text fallback: when the context carries a logger,
// PrintAppInfo must emit one structured runtime.log record instead of the
// legacy "name (pid@host) version=..." line.
func TestPrintAppInfoUsesStructuredLogger(t *testing.T) {
	l := &captureLogger{}
	c := NewContextWithParam(context.Background(), &conf.AppInfo{
		Name:       "ani-governance",
		AppId:      "ani-governance",
		Version:    "1.0.0",
		InstanceId: "instance-1",
	}, nil, l)

	c.PrintAppInfo()

	if len(l.infos) != 1 {
		t.Fatalf("expected one structured app info record, got %d", len(l.infos))
	}
	rec := l.infos[0]
	if rec.msg != "application info" {
		t.Fatalf("unexpected message: %q", rec.msg)
	}
	kv := map[any]any{rec.keyvals[0]: rec.keyvals[1], rec.keyvals[2]: rec.keyvals[3]}
	if kv["event"] != "runtime.log" {
		t.Fatalf("app info record must carry event=runtime.log, got %v", kv["event"])
	}
}
