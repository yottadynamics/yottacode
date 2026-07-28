package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

func TestLSPApplyWorkspaceEditAppliesAndRejectsOutsidePath(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, cwd, "main.go", "package main\nfunc oldName() {}\n")
	tool := &LSPApplyWorkspaceEditTool{lspToolBase: lspToolBase{Cwd: NewCwdRef(cwd)}, WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}}

	args := `{"edit":{"edits":[{"path":"main.go","range":{"start":{"line":1,"character":5},"end":{"line":1,"character":12}},"new_text":"newName"}]}}`
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "applied LSP workspace edit to 1 file") {
		t.Fatalf("unexpected output: %q", out)
	}
	gotBytes, err := os.ReadFile(tool.PathsToSnapshot(cwd, args)[0])
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	got := string(gotBytes)
	if !strings.Contains(got, "func newName()") {
		t.Fatalf("workspace edit not applied: %q", got)
	}

	outside := `{"edit":{"edits":[{"path":"../outside.go","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}},"new_text":"x"}]}}`
	if _, err := tool.Execute(context.Background(), outside); err == nil {
		t.Fatal("expected outside-cwd edit to be rejected")
	}
}

func TestLSPApplyWorkspaceEditPathsToSnapshot(t *testing.T) {
	cwd := t.TempDir()
	tool := &LSPApplyWorkspaceEditTool{lspToolBase: lspToolBase{Cwd: NewCwdRef(cwd)}}
	args := `{"edit":{"edits":[{"path":"main.go","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":0}},"new_text":"x"}]}}`
	paths := tool.PathsToSnapshot(cwd, args)
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "main.go") {
		t.Fatalf("PathsToSnapshot = %#v", paths)
	}
}

func TestLSPRenamePreviewIncludesApplyPayload(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, cwd, "main.go", "package main\nfunc oldName() {}\n")
	base := lspToolBase{Cwd: NewCwdRef(cwd), NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
		return &fakeLSPClient{}, nil
	}}
	tool := &LSPRenamePreviewTool{lspToolBase: base}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","line":1,"character":5,"new_name":"newName"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "apply_payload:") || !strings.Contains(out, `"edits"`) || !strings.Contains(out, "---") {
		t.Fatalf("preview should include diff and apply payload, got %q", out)
	}
}

func TestLSPRenamePreviewInvalidPositionReturnsUnavailable(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, cwd, "main.go", "package main\nfunc oldName() {}\n")
	base := lspToolBase{Cwd: NewCwdRef(cwd), NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
		return &fakeLSPClient{renameErr: lspci.ErrInvalidRenamePosition}, nil
	}}
	tool := &LSPRenamePreviewTool{lspToolBase: base}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","line":0,"character":0,"new_name":"newName"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "unavailable: rename is not valid at this position") {
		t.Fatalf("expected unavailable invalid-position message, got %q", out)
	}
}

func TestLSPCodeActionPreviewIncludesApplyPayload(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, cwd, "main.go", "package main\nfunc main() {}\n")
	base := lspToolBase{Cwd: NewCwdRef(cwd), NewClient: func(context.Context, lspci.Language, string) (lspClient, error) {
		return &fakeLSPClient{actions: []lspci.CodeAction{{Index: 0, Kind: "quickfix", Title: "Add comment", HasEdit: true}}}, nil
	}}
	tool := &LSPCodeActionPreviewTool{lspToolBase: base}
	out, err := tool.Execute(context.Background(), `{"path":"main.go","line":1,"character":0,"index":0}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"apply_payload:", `"edits"`, `"preview_hash"`, "@@", "// fixed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("preview output %q does not contain %q", out, want)
		}
	}
}

func TestLSPApplyWorkspaceEditRejectsStalePreview(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, cwd, "main.go", "package main\nfunc oldName() {}\n")
	tool := &LSPApplyWorkspaceEditTool{lspToolBase: lspToolBase{Cwd: NewCwdRef(cwd)}, WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}}
	args := `{"edit":{"edits":[{"path":"main.go","range":{"start":{"line":1,"character":5},"end":{"line":1,"character":12}},"new_text":"newName","preview_hash":"deadbeef"}]}}`
	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "stale preview") {
		t.Fatalf("expected stale preview error, got %v", err)
	}
}

func TestLSPApplyWorkspaceEditRejectsOverlappingRanges(t *testing.T) {
	cwd := t.TempDir()
	writeFile(t, cwd, "main.go", "package main\nfunc oldName() {}\n")
	tool := &LSPApplyWorkspaceEditTool{lspToolBase: lspToolBase{Cwd: NewCwdRef(cwd)}, WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}}
	args := `{"edit":{"edits":[{"path":"main.go","range":{"start":{"line":1,"character":5},"end":{"line":1,"character":12}},"new_text":"newName"},{"path":"main.go","range":{"start":{"line":1,"character":6},"end":{"line":1,"character":10}},"new_text":"xxx"}]}}`
	if _, err := tool.Execute(context.Background(), args); err == nil || !strings.Contains(err.Error(), "overlapping edit range") {
		t.Fatalf("expected overlapping edit error, got %v", err)
	}
}
