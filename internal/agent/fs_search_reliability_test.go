package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepReportsByteTruncation(t *testing.T) {
	tmp := t.TempDir()
	var b strings.Builder
	for i := 0; i < maxGrepOutBytes/8+100; i++ {
		b.WriteString("needle\n")
	}
	if err := os.WriteFile(filepath.Join(tmp, "x.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := (&GrepTool{Cwd: NewCwdRef(tmp)}).Execute(context.Background(), `{"pattern":"needle","path":".","max_results":100000}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "search root:") || !strings.Contains(out, "output truncated at") {
		t.Fatalf("grep output lacks provenance/truncation marker: %q", out[len(out)-minInt(len(out), 200):])
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
