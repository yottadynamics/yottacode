package agent

import (
	"context"
	"strings"
	"testing"
)

func TestPRReadinessContextTool_FlagsGeneratedLocalArtifacts(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, "tracked.txt", "v1\n")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, "tracked.txt", "v2\n")
	writeFile(t, tmp, ".cache/go-build/00/a", "compiled\n")
	writeFile(t, tmp, ".config/go/telemetry/local/go@v1.count", "counter\n")
	writeFile(t, tmp, "go/pkg/mod/example.com/mod@v1.0.0/go.mod", "module example.com/mod\n")

	tool := &PRReadinessContextTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, leaked := range []string{".cache/go-build/00/a", ".config/go/telemetry/local/go@v1.count", "go/pkg/mod/example.com/mod@v1.0.0/go.mod"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("generated artifact %q leaked into readiness output:\n%s", leaked, out)
		}
	}
	for _, want := range []string{
		"dirty: yes",
		"tracked.txt",
		"artifact_cleanup_required: yes",
		"generated_artifacts: omitted 3 generated local artifact file(s) under .cache/, .config/, go/",
		"cleanup: remove repo-local generated artifact directories before opening a PR",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("readiness output missing %q:\n%s", want, out)
		}
	}
}

func TestPRReadinessContextTool_FlagsIgnoredGeneratedLocalArtifacts(t *testing.T) {
	tmp := gitInit(t)
	writeFile(t, tmp, ".gitignore", "/.cache/\n/.config/\n")
	writeFile(t, tmp, "f.txt", "v1\n")
	gitRun(t, tmp, "add", ".gitignore", "f.txt")
	gitCommit(t, tmp, "base")
	writeFile(t, tmp, ".cache/go-build/00/a", "compiled\n")
	writeFile(t, tmp, ".config/go/telemetry/local/go@v1.count", "counter\n")

	tool := &PRReadinessContextTool{Cwd: NewCwdRef(tmp)}
	out, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, leaked := range []string{".cache/go-build/00/a", ".config/go/telemetry/local/go@v1.count"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("generated artifact %q leaked into readiness context:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "artifact_cleanup_required: yes") || !strings.Contains(out, "omitted 2 generated local artifact file(s) under .cache/, .config/") {
		t.Fatalf("expected ignored artifact cleanup warning, got:\n%s", out)
	}
}
