package buf

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"go-wind-admin/tools/localdeps/gow/internal/pkg"
)

const (
	defaultBufConfigFile    = "buf.yaml"
	bufLockFile             = "buf.lock"
	defaultBufGenConfigFile = "buf.gen.yaml"
)

func RunGenerate(cmd *cobra.Command, args []string) error {
	inspector, err := pkg.NewModuleInspectorFromGo(cmd.Context(), "")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "\033[31mERROR: %s\033[m\n", err.Error())
		return err
	}

	// 工具不再自动执行 `go mod tidy`（T13 授权的工作流变更）：
	// 依赖维护改为显式人工 `go mod tidy` 或 T14 阶段处理。

	apiPath := filepath.Join(inspector.Root, "api")
	if !pkg.IsDirExists(apiPath) {
		_, _ = fmt.Fprintf(os.Stderr, "\033[31mERROR: api directory does not exist: %s\033[m\n", apiPath)
		return fmt.Errorf("api directory does not exist: %s", apiPath)
	}

	return GenerateLocked(cmd.Context(), inspector.Root, apiPath)
}

// GenerateFromPath 从指定路径生成 Protobuf 代码。
func GenerateFromPath(ctx context.Context, apiPath string) error {
	// 确保 buf 已安装
	if err := ensureBufInstalled(ctx); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "\033[31mERROR: %s\033[m\n", err.Error())
		return err
	}

	if !pkg.IsDirExists(apiPath) {
		_, _ = fmt.Fprintf(os.Stderr, "\033[31mERROR: api directory does not exist: %s\033[m\n", apiPath)
		return fmt.Errorf("api directory does not exist: %s", apiPath)
	}

	if !isBufConfigExists(apiPath) {
		_, _ = fmt.Fprintf(os.Stderr, "\033[31mERROR: buf config file (%s) does not exist in api directory: %s\033[m\n", defaultBufConfigFile, apiPath)
		return fmt.Errorf("buf config file (%s) does not exist in api directory: %s", defaultBufConfigFile, apiPath)
	}

	if !isBufLockExists(apiPath) {
		// 缺锁不再自动 `buf dep update`（T13 授权的工具副作用收敛）：锁文件属于已核验的生成前提，
		// 自动更新会改动固定依赖。需要更新时由人工显式执行 `buf dep update`。
		_, _ = fmt.Fprintf(os.Stderr, "\033[31mERROR: %s does not exist in %s; run `buf dep update` explicitly instead of letting this tool do it\033[m\n", bufLockFile, apiPath)
		return fmt.Errorf("buf.lock does not exist: %s", filepath.Join(apiPath, bufLockFile))
	}

	fmt.Printf("Generating proto code from YAML files in api directory...\n")

	yamlFiles, err := scanYAMLFiles(apiPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "\033[31mERROR: failed to scan YAML files: %s\033[m\n", err.Error())
		return err
	}

	selected, err := selectActiveTemplates(apiPath, yamlFiles)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "\033[31mERROR: %s\033[m\n", err.Error())
		return err
	}
	yamlFiles = selected

	if len(yamlFiles) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "\033[33mWARNING: no YAML files found in api directory: %s\033[m\n", apiPath)
		return nil
	}

	fmt.Printf("Running `buf generate` in api directory...\n")
	for _, yamlFile := range yamlFiles {
		fmt.Printf("Using template file: %s\n", yamlFile)
		if err = RunBufGenerate(ctx, apiPath, yamlFile); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "\033[31mERROR: %s\033[m\n", err.Error())
			return err
		}
	}

	fmt.Printf("Protobuf Code generation completed successfully.\n")
	return nil
}

// GenerateLocked is the accepted API generation chain: parse every active template, verify every
// tool, pinned plugin and input it names, and only then generate into a staging copy whose output
// is compared and written back. The plain walk above is kept for callers that point at their own
// directory; the repository's `api` command uses this one so that `make api` and a direct
// `gow api` cannot behave differently.
func GenerateLocked(ctx context.Context, root, apiPath string) error {
	if !pkg.IsDirExists(apiPath) {
		return fmt.Errorf("api directory does not exist: %s", apiPath)
	}
	if !isBufConfigExists(apiPath) {
		return fmt.Errorf("buf config file (%s) does not exist in api directory: %s", defaultBufConfigFile, apiPath)
	}
	if !isBufLockExists(apiPath) {
		return fmt.Errorf("buf.lock does not exist: %s; run `buf dep update` explicitly instead of letting this tool do it",
			filepath.Join(apiPath, bufLockFile))
	}

	yamlFiles, err := scanYAMLFiles(apiPath)
	if err != nil {
		return err
	}
	selected, err := selectActiveTemplates(apiPath, yamlFiles)
	if err != nil {
		return err
	}
	templates := make([]*genTemplate, 0, len(selected))
	for _, path := range selected {
		t, err := parseGenTemplate(path)
		if err != nil {
			return err
		}
		templates = append(templates, t)
	}

	roots, err := deriveManagedRoots(root, apiPath, templates)
	if err != nil {
		return err
	}
	if err := preflightGenerate(ctx, root, apiPath, templates); err != nil {
		return err
	}
	return stageAndGenerate(ctx, root, templates, roots)
}

// runBufGenerateIn 在给定目录执行 `buf generate --template <name>`，用于隔离暂存副本内的生成。
func runBufGenerateIn(ctx context.Context, dir, templateName string) error {
	cmd := exec.CommandContext(ctx, "buf", "generate", "--template", templateName)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run `buf generate --template %s` in %s: %w", templateName, dir, err)
	}
	return nil
}

// RunBufDepUpdate 在指定目录执行 `buf dep update`，并将输出转发到标准输出/错误。
func RunBufDepUpdate(ctx context.Context, apiPath string) error {
	cmd := exec.CommandContext(ctx, "buf", "dep", "update")
	cmd.Dir = apiPath
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run `buf dep update`: %w", err)
	}
	return nil
}

// RunBufGenerate 在指定目录执行 `buf generate --template <template>`。
// 如果 template 为空，使用常量 `defaultBufGenConfigFile`。
// 在执行前会调用 ensureBufInstalled 确保 buf 可用，命令输出会直接写入标准输出/错误。
func RunBufGenerate(ctx context.Context, apiPath, template string) error {
	if template == "" {
		template = defaultBufGenConfigFile
	}

	cmd := exec.CommandContext(ctx, "buf", "generate", "--template", template)
	cmd.Dir = apiPath
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run `buf generate --template %s`: %w", template, err)
	}
	return nil
}

// activeGenConfigs 是本仓已验收的活跃生成模板，顺序即执行顺序（与原有目录遍历顺序一致，
// buf.gen.yaml 的 clean: true 仍排在其余模板之前，顺序与上游目录遍历逐字一致）。
var activeGenConfigs = []string{
	"buf.accelerator.gen.yaml",
	"buf.admin.openapi.gen.yaml",
	"buf.aksk.gen.yaml",
	"buf.bootstrap.conf.gen.yaml",
	"buf.gen.yaml",
	"buf.inference.gen.yaml",
	"buf.modeldev.gen.yaml",
	"buf.network.gen.yaml",
	"buf.pagination.gen.yaml",
	"buf.quota.gen.yaml",
	"buf.quota-release.gen.yaml",
	"buf.redact.gen.yaml",
	"buf.validate.gen.yaml",
}

// selectActiveTemplates 只接受已批准的活跃模板，按 activeGenConfigs 排序。
// 缺失、重复或未知模板均失败，避免悄悄漏生成或恢复旧入口。
func selectActiveTemplates(apiPath string, found []string) ([]string, error) {
	byName := make(map[string]string, len(found))
	for _, path := range found {
		name := filepath.Base(path)
		if _, dup := byName[name]; dup {
			return nil, fmt.Errorf("duplicate template name %s under %s", name, apiPath)
		}
		byName[name] = path
	}

	for _, name := range activeGenConfigs {
		if _, ok := byName[name]; !ok {
			return nil, fmt.Errorf("approved template is missing: %s (expected under %s)", name, apiPath)
		}
	}

	for name, path := range byName {
		if !slices.Contains(activeGenConfigs, name) {
			return nil, fmt.Errorf("unclassified template %s: not in activeGenConfigs", path)
		}
	}

	ordered := make([]string, 0, len(activeGenConfigs))
	for _, name := range activeGenConfigs {
		ordered = append(ordered, byName[name])
	}
	return ordered, nil
}

// scanYAMLFiles 仅扫描以 `.gen.yaml` 结尾的文件（忽略大小写），返回匹配的文件路径列表。
func scanYAMLFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// 访问失败时继续遍历其他路径
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(path), ".gen.yaml") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// checkBuffInstalled 执行 `buf --version` 来探测 buf 是否安装。
// 返回版本字符串（例: "buf v1.0.0"）或执行失败的错误。
func checkBufInstalled(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "buf", "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run `buf --version`: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// ensureBufInstalled 只做存在性检查：缺 buf 时明确失败并指向任务私有的固定版本工具，
// 不再 go install @latest（T13 授权的工具副作用收敛；本仓固定 buf v1.60.0）。
func ensureBufInstalled(ctx context.Context) error {
	version, err := checkBufInstalled(ctx)
	if err == nil {
		fmt.Printf("using buf %s\n", version)
		return nil
	}
	return fmt.Errorf("buf is not available: run the task-pinned buf (v1.60.0) on PATH, "+
		"for example BUF=<buf v1.60.0>; the tool never installs tools itself: %w", err)
}

// isBufLockExists 检查 apiPath 下是否存在 `buf.lock` 文件。
func isBufLockExists(apiPath string) bool {
	path := filepath.Join(apiPath, bufLockFile)
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// isBufConfigExists 检查 apiPath 下是否存在 `buf.yaml` 文件。
func isBufConfigExists(apiPath string) bool {
	path := filepath.Join(apiPath, defaultBufConfigFile)
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
