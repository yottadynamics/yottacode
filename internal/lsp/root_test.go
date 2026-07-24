package lsp

import (
	"path/filepath"
	"testing"
)

func TestWorkspaceRootUsesLanguageMarkers(t *testing.T) {
	root := t.TempDir()
	goMod := filepath.Join(root, "go.mod")
	writeDetectFile(t, root, "go.mod")
	writeDetectFile(t, root, "cmd/app/main.go")
	lang, _ := ResolveFile(filepath.Join(root, "cmd/app/main.go"))
	if got := WorkspaceRoot(filepath.Join(root, "cmd/app/main.go"), lang, filepath.Join(root, "cmd/app")); got != root {
		t.Fatalf("WorkspaceRoot = %q, want marker root %q (go.mod %s)", got, root, goMod)
	}
}

func TestWorkspaceRootFallsBackWithoutMarkers(t *testing.T) {
	root := t.TempDir()
	writeDetectFile(t, root, "single/main.go")
	lang, _ := ResolveFile(filepath.Join(root, "single/main.go"))
	fallback := filepath.Join(root, "single")
	if got := WorkspaceRoot(filepath.Join(root, "single/main.go"), lang, fallback); got != fallback {
		t.Fatalf("WorkspaceRoot = %q, want fallback %q", got, fallback)
	}
}
