package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoplsSmoke(t *testing.T) {
	smokeLanguageServer(t, Language{ID: "go", Name: "Go", Extensions: []string{".go"}, Command: []string{"gopls"}}, ".", "NewClient")
}

func TestTypeScriptLanguageServerSmoke(t *testing.T) {
	root := t.TempDir()
	writeSmokeFile(t, root, "package.json", `{"devDependencies":{"typescript":"^5.9.0"}}`)
	installTypeScriptForSmoke(t, root)
	writeSmokeFile(t, root, "index.ts", "export function smokeTarget(): number { return 1 }\n")
	lang := Language{ID: "typescript", Name: "TypeScript/JavaScript", Extensions: []string{".ts"}, Command: []string{"typescript-language-server", "--stdio"}}
	if tsserverPath := os.Getenv("YOTTACODE_LSP_TYPESCRIPT_TSSERVER"); tsserverPath != "" {
		lang.InitializationOptions = map[string]any{"tsserver": map[string]any{"path": tsserverPath, "fallbackPath": tsserverPath}}
	}
	smokeLanguageServer(t, lang, root, "smokeTarget")
}

func TestPyrightLanguageServerSmoke(t *testing.T) {
	root := t.TempDir()
	writeSmokeFile(t, root, "pyproject.toml", "[project]\nname = \"yc-smoke\"\nversion = \"0.0.0\"\n")
	writeSmokeFile(t, root, "main.py", "def smoke_target():\n    return 1\n")
	smokeLanguageServer(t, Language{ID: "python", Name: "Python", Extensions: []string{".py"}, Command: []string{"pyright-langserver", "--stdio"}}, root, "smoke_target")
}

func TestRustAnalyzerSmoke(t *testing.T) {
	root := t.TempDir()
	writeSmokeFile(t, root, "Cargo.toml", "[package]\nname = \"yc_smoke\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	writeSmokeFile(t, root, filepath.Join("src", "lib.rs"), "pub fn smoke_target() -> i32 { 1 }\n")
	smokeLanguageServer(t, Language{ID: "rust", Name: "Rust", Extensions: []string{".rs"}, Command: []string{"rust-analyzer"}}, root, "smoke_target")
}

func smokeLanguageServer(t *testing.T, lang Language, root, query string) {
	t.Helper()
	required := os.Getenv("YOTTACODE_LSP_SMOKE_REQUIRED") == "1"
	if _, err := exec.LookPath(lang.Command[0]); err != nil {
		smokeUnavailable(t, required, "%s not installed", lang.Command[0])
		return
	}
	ctx := context.Background()
	client, err := NewClient(ctx, lang, root)
	if err != nil {
		smokeUnavailable(t, required, "%s installed but not usable for smoke test: %v", lang.Command[0], err)
		return
	}
	defer client.Close()
	items, err := client.WorkspaceSymbols(ctx, query)
	if err != nil {
		smokeUnavailable(t, required, "%s workspace symbols unavailable for smoke test: %v", lang.Command[0], err)
		return
	}
	found := false
	for _, item := range items {
		if strings.Contains(item.Name, query) {
			found = true
			break
		}
	}
	workflowPath := smokeWorkflowPath(root, lang)
	if !found {
		if workflowPath == "" {
			smokeUnavailable(t, required, "%s workspace symbols for %s did not include target; got %d items", lang.Command[0], query, len(items))
			return
		}
		docItems, err := client.DocumentSymbols(ctx, workflowPath)
		if err != nil {
			smokeUnavailable(t, required, "%s document symbols unavailable for smoke test after empty workspace symbols: %v", lang.Command[0], err)
			return
		}
		for _, item := range docItems {
			if strings.Contains(item.Name, query) {
				found = true
				break
			}
		}
		if !found {
			smokeUnavailable(t, required, "%s symbols for %s did not include target; workspace=%d document=%d", lang.Command[0], query, len(items), len(docItems))
			return
		}
	}
	if workflowPath == "" {
		return
	}
	if _, err := client.DocumentSymbols(ctx, workflowPath); err != nil {
		smokeUnavailable(t, required, "%s document symbols unavailable for smoke test: %v", lang.Command[0], err)
		return
	}
	if _, err := client.Diagnostics(ctx, workflowPath); err != nil {
		t.Logf("%s diagnostics unavailable for smoke test: %v", lang.Command[0], err)
	}
	if client.caps.Formatting {
		if _, err := client.FormatPreview(ctx, workflowPath); err != nil {
			t.Logf("%s formatting unavailable for smoke test: %v", lang.Command[0], err)
		}
	}
}

func smokeWorkflowPath(root string, lang Language) string {
	switch lang.ID {
	case "go":
		return filepath.Join(root, "internal", "lsp", "client.go")
	case "typescript":
		return filepath.Join(root, "index.ts")
	case "python":
		return filepath.Join(root, "main.py")
	case "rust":
		return filepath.Join(root, "src", "lib.rs")
	default:
		return ""
	}
}

func installTypeScriptForSmoke(t *testing.T, root string) {
	t.Helper()
	if os.Getenv("YOTTACODE_LSP_SMOKE_REQUIRED") != "1" {
		return
	}
	if _, err := exec.LookPath("npm"); err != nil {
		smokeUnavailable(t, true, "npm not installed for TypeScript smoke workspace setup")
	}
	cmd := exec.Command("npm", "install", "--silent")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		smokeUnavailable(t, true, "npm install for TypeScript smoke workspace failed: %v\n%s", err, strings.TrimSpace(string(out)))
	}
}

func writeSmokeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func smokeUnavailable(t *testing.T, required bool, format string, args ...any) {
	t.Helper()
	if required {
		t.Fatalf(format, args...)
		return
	}
	t.Skipf(format, args...)
}
