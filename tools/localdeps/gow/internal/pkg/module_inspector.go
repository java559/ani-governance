package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ModuleInspector 保存项目根目录和模块名。
type ModuleInspector struct {
	Root    string // 含有 `go.mod` 的目录
	ModPath string // go module path
}

// NewModuleInspectorFromGo 使用 `go list -m -json` 在 startDir（或当前目录）执行，
// 返回该目录所属模块的路径和项目根目录。workspace 中只选择 startDir 的模块，
// 不会将其它 workspace 模块作为兜底。若执行失败，会向上遍历父目录重试。
func NewModuleInspectorFromGo(ctx context.Context, startDir string) (*ModuleInspector, error) {
	if startDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		startDir = wd
	}
	startDir, err := filepath.Abs(startDir)
	if err != nil {
		return nil, err
	}

	g := NewGoCmdWithTimeout("", 5*time.Minute)
	out, err := g.RunUpwardUntilSucceeds(ctx, startDir, "list", "-m", "-json")
	if err != nil {
		return nil, err
	}

	// GOMOD identifies the nearest module, including a nested module that is
	// excluded from the workspace. Directory-prefix matching alone can select
	// that module's parent incorrectly.
	goModOut, err := NewGoCmdWithTimeout(startDir, 30*time.Second).Output(ctx, "env", "GOMOD")
	if err != nil {
		return nil, err
	}
	goMod := strings.TrimSpace(string(goModOut))
	if filepath.Base(goMod) != "go.mod" {
		return nil, fmt.Errorf("directory %q does not belong to a module", startDir)
	}
	goMod, err = filepath.EvalSymlinks(goMod)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(goMod)

	// In a workspace, go list emits one JSON object per main module.
	decoder := json.NewDecoder(bytes.NewReader(out))
	var inspector *ModuleInspector
	for {
		var info struct {
			Path string `json:"Path"`
			Dir  string `json:"Dir"`
		}
		if err := decoder.Decode(&info); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if info.Dir == "" || info.Path == "" {
			continue
		}
		dir, err := filepath.EvalSymlinks(info.Dir)
		if err != nil {
			return nil, err
		}
		if dir == root {
			inspector = &ModuleInspector{Root: root, ModPath: info.Path}
		}
	}
	if inspector == nil {
		return nil, fmt.Errorf("module for directory %q is not present in go list output", startDir)
	}
	return inspector, nil
}
