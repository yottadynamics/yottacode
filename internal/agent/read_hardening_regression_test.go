package agent

import (
	"context"
	"strings"
	"testing"
)

func TestReadManyFilesTool_OffsetInsideRuneDropsLeadingFragment(t *testing.T) {
	tmp := t.TempDir()
	writeFile(t, tmp, "a.txt", "aéb\n")

	out, err := newReadMany(tmp).Execute(context.Background(), `{"paths":["a.txt"],"offset":2,"anchors":true}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "\tb") {
		t.Fatalf("window should resume at the next complete rune, got %q", out)
	}
	if !strings.Contains(out, "offset=3") {
		t.Fatalf("receipt should cover the emitted bytes, got %q", out)
	}
	if !strings.Contains(out, "length=2") {
		t.Fatalf("receipt should cover b and its newline, got %q", out)
	}
}

func TestReadFileTool_LongLineDoesNotRequireUnboundedAllocation(t *testing.T) {
	tmp := t.TempDir()
	// This exercises incremental line reads. The returned window remains capped
	// even though the source line is much larger than the output budget.
	writeFile(t, tmp, "long.txt", strings.Repeat("x", 4*maxReadBytes))
	out, err := newReadFile(tmp, false).Execute(context.Background(), `{"path":"long.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "…[truncated]") {
		t.Fatalf("long line should be marked truncated: tail=%q", out[max(0, len(out)-40):])
	}
}
