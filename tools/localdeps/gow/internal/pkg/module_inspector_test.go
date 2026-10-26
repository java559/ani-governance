package pkg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewModuleInspectorFromGo(t *testing.T) {
	inspector, err := NewModuleInspectorFromGo(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Root: %s, ModPath: %s", inspector.Root, inspector.ModPath)
}

func TestNewModuleInspectorFromGoWorkspace(t *testing.T) {
	root := t.TempDir()
	first := writeInspectorModule(t, root, "first", "example.invalid/first")
	second := writeInspectorModule(t, root, "second", "example.invalid/second")
	writeInspectorModule(t, first, "excluded", "example.invalid/excluded")
	work := filepath.Join(root, "go.work")
	if err := os.WriteFile(work, []byte("go 1.26.10\n\nuse (\n ./first\n ./second\n)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", work)

	for _, tc := range []struct {
		name, start, wantRoot, wantPath string
	}{
		{"first root", first, first, "example.invalid/first"},
		{"first subdirectory", filepath.Join(first, "internal", "nested"), first, "example.invalid/first"},
		{"second subdirectory", filepath.Join(second, "internal", "nested"), second, "example.invalid/second"},
		{"workspace root is not a module", root, "", ""},
		{"excluded nested module is not its parent", filepath.Join(first, "excluded"), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			inspector, err := NewModuleInspectorFromGo(ctx, tc.start)
			if tc.wantRoot == "" {
				if err == nil {
					t.Fatalf("selected unrelated module: %+v", inspector)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if inspector.Root != tc.wantRoot || inspector.ModPath != tc.wantPath {
				t.Fatalf("got %+v, want root %q and module %q", inspector, tc.wantRoot, tc.wantPath)
			}
		})
	}
}

func TestNewModuleInspectorFromGoSingleModule(t *testing.T) {
	root := writeInspectorModule(t, t.TempDir(), "single", "example.invalid/single")
	// Only this standalone fixture disables a workspace; the actual project test
	// above keeps the caller's workspace, and the multi-module fixture uses one.
	t.Setenv("GOWORK", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	inspector, err := NewModuleInspectorFromGo(ctx, filepath.Join(root, "internal", "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if inspector.Root != root || inspector.ModPath != "example.invalid/single" {
		t.Fatalf("unexpected standalone module: %+v", inspector)
	}
}

func writeInspectorModule(t *testing.T, parent, name, modulePath string) string {
	t.Helper()
	root := filepath.Join(parent, name)
	if err := os.MkdirAll(filepath.Join(root, "internal", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.26.10\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}
