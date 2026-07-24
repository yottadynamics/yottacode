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
	writeSmokeFile(t, root, "package.json", `{"devDependencies":{"typescript":"latest"}}`)
	writeSmokeFile(t, root, "index.ts", "export function smokeTarget(): number { return 1 }\n")
	smokeLanguageServer(t, Language{ID: "typescript", Name: "TypeScript/JavaScript", Extensions: []string{".ts"}, Command: []string{"typescript-language-server", "--stdio"}}, root, "smokeTarget")
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
	if _, err := exec.LookPath(lang.Command[0]); err != nil {
		t.Skipf("%s not installed", lang.Command[0])
	}
	ctx := context.Background()
	client, err := NewClient(ctx, lang, root)
	if err != nil {
		t.Fatalf("NewClient(%s): %v", lang.Command[0], err)
	}
	defer client.Close()
	items, err := client.WorkspaceSymbols(ctx, query)
	if err != nil {
		t.Fatalf("WorkspaceSymbols(%s): %v", query, err)
	}
	for _, item := range items {
		if strings.Contains(item.Name, query) {
			return
		}
	}
	t.Fatalf("workspace symbols for %s did not include target; got %d items", query, len(items))
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
