package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/edit/hashline"
)

func TestApplyHashlineTool_ReplacesAnchoredSpan(t *testing.T) {
	tmp := t.TempDir()
	body := []byte("alpha\nbeta\ngamma\n")
	writeFile(t, tmp, "a.txt", string(body))
	anchor := mustHashlineAnchor(t, body, 6, 4)
	tool := &ApplyHashlineTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}

	out, err := tool.Execute(context.Background(), applyHashlineArgsJSON(t, "a.txt", anchor, "beta", "BETA"))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "applied 1 hashline hunk") || !strings.Contains(out, "-beta") || !strings.Contains(out, "+BETA") {
		t.Fatalf("output missing capped diff/result: %q", out)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "a.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "alpha\nBETA\ngamma\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestApplyHashlineTool_RejectsHashMismatchWithoutWriting(t *testing.T) {
	tmp := t.TempDir()
	body := []byte("alpha\nbeta\n")
	writeFile(t, tmp, "a.txt", string(body))
	anchor := hashline.Anchor{Offset: 6, Length: 4, Hash: "0000000000000000"}
	tool := &ApplyHashlineTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}

	_, err := tool.Execute(context.Background(), applyHashlineArgsJSON(t, "a.txt", anchor, "beta", "BETA"))
	if err == nil || !strings.Contains(err.Error(), "old bytes do not match anchor hash") || !strings.Contains(err.Error(), "read_file") {
		t.Fatalf("err = %v, want recoverable stale hash hint", err)
	}
	got, err := os.ReadFile(filepath.Join(tmp, "a.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("file changed on stale hash: %q", got)
	}
}

func TestApplyHashlineTool_RejectsDeniedPath(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".yottacode", "permissions.local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("old\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	anchor := mustHashlineAnchor(t, body, 0, len(body))
	tool := &ApplyHashlineTool{
		Cwd:       NewCwdRef(tmp),
		WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp), DenyExact: DefaultDenyPaths(tmp)},
	}

	_, err := tool.Execute(context.Background(), applyHashlineArgsJSON(t, ".yottacode/permissions.local.json", anchor, "old\n", "new\n"))
	if err == nil || !strings.Contains(err.Error(), "deny list") {
		t.Fatalf("err = %v, want deny-list refusal", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("denied file changed: %q", got)
	}
}

func TestApplyHashlineTool_RequiresApprovalAndSnapshotsPath(t *testing.T) {
	tmp := t.TempDir()
	tool := &ApplyHashlineTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"path":"a.txt","offset":0,"length":3,"hash":"2c26b46b68ffc68f","old":"foo","new":"bar"}`
	if !tool.RequiresApproval(args) {
		t.Fatal("apply_hashline must require approval")
	}
	snaps := tool.PathsToSnapshot(tmp, args)
	if len(snaps) != 1 || snaps[0] != filepath.Join(tmp, "a.txt") {
		t.Fatalf("snapshots = %#v", snaps)
	}
}

func TestApplyHashlineTool_OutputDiffIsCapped(t *testing.T) {
	tmp := t.TempDir()
	oldLines := make([]string, 120)
	newLines := make([]string, 120)
	for i := range oldLines {
		oldLines[i] = "old"
		newLines[i] = "new"
	}
	oldText := strings.Join(oldLines, "\n") + "\n"
	newText := strings.Join(newLines, "\n") + "\n"
	writeFile(t, tmp, "a.txt", oldText)
	anchor := mustHashlineAnchor(t, []byte(oldText), 0, len(oldText))
	tool := &ApplyHashlineTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}

	out, err := tool.Execute(context.Background(), applyHashlineArgsJSON(t, "a.txt", anchor, oldText, newText))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "…[truncated diff]") {
		t.Fatalf("output should contain truncated diff marker, got %q", out)
	}
}

func mustHashlineAnchor(t *testing.T, src []byte, offset, length int) hashline.Anchor {
	t.Helper()
	anchor, err := hashline.HashSpan(src, offset, length)
	if err != nil {
		t.Fatalf("HashSpan: %v", err)
	}
	return anchor
}

func applyHashlineArgsJSON(t *testing.T, path string, anchor hashline.Anchor, oldText, newText string) string {
	t.Helper()
	payload := map[string]any{
		"path":   path,
		"offset": anchor.Offset,
		"length": anchor.Length,
		"hash":   anchor.Hash,
		"old":    oldText,
		"new":    newText,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(b)
}
