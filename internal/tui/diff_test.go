package tui

import (
	"regexp"
	"strings"
	"testing"
)

// ansiRe strips terminal color codes from highlighter output so tests
// can match against the underlying text without baking in chroma's
// escape sequences.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestRenderEditDiff_HappyPath(t *testing.T) {
	args := `{"path":"main.go","old_string":"foo","new_string":"bar"}`
	got, ok := renderEditDiff(args)
	if !ok {
		t.Fatalf("expected ok=true for valid edit_file args")
	}
	plain := stripANSI(got)
	if !strings.Contains(plain, "main.go") {
		t.Errorf("path missing from diff: %q", plain)
	}
	if !strings.Contains(plain, "- foo") {
		t.Errorf("old line missing: %q", plain)
	}
	if !strings.Contains(plain, "+ bar") {
		t.Errorf("new line missing: %q", plain)
	}
}

func TestRenderEditDiff_MultiLine(t *testing.T) {
	args := `{"path":"x.go","old_string":"a\nb","new_string":"c\nd"}`
	got, ok := renderEditDiff(args)
	if !ok {
		t.Fatalf("expected ok")
	}
	plain := stripANSI(got)
	for _, want := range []string{"- a", "- b", "+ c", "+ d"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in diff: %q", want, plain)
		}
	}
}

func TestRenderEditDiff_AppliesSyntaxHighlighting(t *testing.T) {
	// Go source should pick up keyword coloring. We don't pin the
	// exact color codes — chroma's output may evolve — but the diff
	// should contain at least one ANSI escape, indicating tokens were
	// highlighted rather than streamed plain.
	args := `{"path":"x.go","old_string":"package foo","new_string":"package bar"}`
	got, ok := renderEditDiff(args)
	if !ok {
		t.Fatalf("expected ok")
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("highlighted output should contain ANSI escapes: %q", got)
	}
}

func TestRenderEditDiff_ReplaceAllAnnotation(t *testing.T) {
	args := `{"path":"x","old_string":"a","new_string":"b","replace_all":true}`
	got, ok := renderEditDiff(args)
	if !ok {
		t.Fatalf("expected ok")
	}
	if !strings.Contains(got, "replace_all") {
		t.Errorf("replace_all annotation missing: %q", got)
	}
}

func TestRenderEditDiff_BadJSON(t *testing.T) {
	if _, ok := renderEditDiff(`{not json`); ok {
		t.Errorf("expected ok=false for malformed JSON")
	}
}

func TestRenderEditDiff_MissingFields(t *testing.T) {
	if _, ok := renderEditDiff(`{}`); ok {
		t.Errorf("expected ok=false for empty args")
	}
	if _, ok := renderEditDiff(`{"path":"x"}`); ok {
		t.Errorf("expected ok=false when old_string is missing")
	}
}

func TestWriteFileBodyRows_HappyPath(t *testing.T) {
	args := `{"path":"main.go","content":"package main\n\nfunc main() {}\n"}`
	rows, ok := writeFileBodyRows(args, 80)
	if !ok {
		t.Fatalf("expected ok=true for valid write_file args")
	}
	if len(rows) == 0 {
		t.Fatalf("expected non-empty body rows")
	}
	plain := stripANSI(strings.Join(rows, "\n"))
	for _, want := range []string{"+ package main", "+ func main() {}"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in body: %q", want, plain)
		}
	}
}

func TestWriteFileBodyRows_BadJSON(t *testing.T) {
	if _, ok := writeFileBodyRows(`{not json`, 80); ok {
		t.Errorf("expected ok=false for malformed JSON")
	}
}

func TestWriteFileBodyRows_MissingPath(t *testing.T) {
	if _, ok := writeFileBodyRows(`{"content":"hi"}`, 80); ok {
		t.Errorf("expected ok=false when path is missing")
	}
}

func TestWriteFileBodyRows_TruncatesOverflow(t *testing.T) {
	var lines []string
	for i := 0; i < cardBodyLineCap+5; i++ {
		lines = append(lines, "line")
	}
	args := `{"path":"x.go","content":"` + strings.Join(lines, "\\n") + `"}`
	rows, ok := writeFileBodyRows(args, 80)
	if !ok {
		t.Fatalf("expected ok")
	}
	last := stripANSI(rows[len(rows)-1])
	if !strings.Contains(last, "more line(s)") {
		t.Errorf("expected truncation marker as final row, got: %q", last)
	}
}
