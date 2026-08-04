package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveFileMapsSupportedExtensions(t *testing.T) {
	cases := map[string]string{
		"main.go":       "go",
		"app.ts":        "typescript",
		"app.tsx":       "typescript",
		"plain.js":      "typescript",
		"component.JSX": "typescript",
		"script.py":     "python",
		"lib.rs":        "rust",
	}
	for path, want := range cases {
		got, ok := ResolveFile(path)
		if !ok {
			t.Fatalf("ResolveFile(%q) did not match", path)
		}
		if got.ID != want {
			t.Errorf("ResolveFile(%q) = %q, want %q", path, got.ID, want)
		}
	}
	if _, ok := ResolveFile("README.md"); ok {
		t.Errorf("unsupported extension should not resolve")
	}
}

func TestResolveIDAndInstallHints(t *testing.T) {
	lang, ok := ResolveID(" python ")
	if !ok || lang.ID != "python" {
		t.Fatalf("ResolveID(python) = %+v, %v", lang, ok)
	}
	if !strings.Contains(lang.InstallHint, "pyright") {
		t.Errorf("python install hint should mention pyright: %q", lang.InstallHint)
	}
	if _, ok := ResolveID("ruby"); ok {
		t.Errorf("unknown language id should not resolve")
	}
}

func TestLanguageSafeInitializationOptions(t *testing.T) {
	rust, ok := ResolveID("rust")
	if !ok || len(rust.InitializationOptions) == 0 {
		t.Fatalf("rust should carry safe initialization options: %+v", rust)
	}
	ts, ok := ResolveID("typescript")
	if !ok || len(ts.InitializationOptions) == 0 {
		t.Fatalf("typescript should carry safe initialization options: %+v", ts)
	}
	custom := ApplyOverrides(rust, map[string][]string{"rust": {"custom-rust-analyzer"}})
	if len(custom.InitializationOptions) != 0 {
		t.Fatalf("custom server overrides should not inherit built-in settings: %+v", custom.InitializationOptions)
	}
}

func TestDetectWorkspaceAggregatesLanguagesAndSkipsHeavyDirs(t *testing.T) {
	tmp := t.TempDir()
	writeDetectFile(t, tmp, "main.go")
	writeDetectFile(t, tmp, "pkg/extra.go")
	writeDetectFile(t, tmp, "web/app.ts")
	writeDetectFile(t, tmp, "node_modules/skip.ts")
	writeDetectFile(t, tmp, ".hidden/skip.py")
	langs, err := DetectWorkspace(context.Background(), tmp, 100)
	if err != nil {
		t.Fatalf("DetectWorkspace: %v", err)
	}
	counts := map[string]int{}
	for _, lang := range langs {
		counts[lang.ID] = lang.FilesAvailable
		if lang.Name == "" || lang.InstallHint == "" || len(lang.Command) == 0 {
			t.Errorf("detected language missing metadata: %+v", lang)
		}
		switch lang.ID {
		case "go":
			if SyntaxMode(lang.ID) != "parser" {
				t.Errorf("Go should report parser syntax fallback")
			}
		case "typescript", "python", "rust":
			if SyntaxMode(lang.ID) != "regex" {
				t.Errorf("%s should report regex syntax fallback", lang.ID)
			}
		}
	}
	if counts["go"] != 2 {
		t.Errorf("go count = %d, want 2", counts["go"])
	}
	if counts["typescript"] != 1 {
		t.Errorf("typescript count = %d, want 1 (node_modules skipped)", counts["typescript"])
	}
	if _, ok := counts["python"]; ok {
		t.Errorf("hidden python file should be skipped: %+v", counts)
	}
}

func TestDetectWorkspaceHonorsMaxFiles(t *testing.T) {
	tmp := t.TempDir()
	writeDetectFile(t, tmp, "a.txt")
	writeDetectFile(t, tmp, "b.go")
	langs, err := DetectWorkspace(context.Background(), tmp, 1)
	if err != nil {
		t.Fatalf("DetectWorkspace: %v", err)
	}
	if len(langs) != 0 {
		t.Errorf("maxFiles=1 should stop before the second supported file, got %+v", langs)
	}
}

func writeDetectFile(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
