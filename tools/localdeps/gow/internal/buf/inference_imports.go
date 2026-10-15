package buf

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// stageInferenceImports reads the exact Go module versions selected by this
// consumer. Imported Proto definitions remain temporary generation inputs;
// only Governance-owned files are generated into the managed output roots.
func stageInferenceImports(ctx context.Context, root, apiPath string) error {
	if _, err := os.Stat(filepath.Join(root, "api/protos/inference/service/v1/owner.proto")); os.IsNotExist(err) {
		return nil
	}
	for _, input := range []struct{ module, source, target string }{
		{"github.com/liangzai006/ani-inference-service", "api", "inference"},
		{"github.com/zhangzhe-ctrl/ani-accelerator-service", "api/protos", "accelerator"},
	} {
		cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", input.module)
		cmd.Dir = root
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("download the pinned %s module before API generation: %w", input.module, err)
		}
		if strings.TrimSpace(string(output)) == "" {
			return fmt.Errorf("download the pinned %s module before API generation: module directory is empty", input.module)
		}
		source := filepath.Join(strings.TrimSpace(string(output)), input.source)
		target := filepath.Join(apiPath, ".upstream", input.target)
		if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".proto") {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("non-regular upstream Proto input: %s", path)
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			dest := filepath.Join(target, rel)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			return copyFile(path, dest)
		}); err != nil {
			return fmt.Errorf("stage pinned %s Proto input: %w", input.module, err)
		}
	}
	return nil
}
