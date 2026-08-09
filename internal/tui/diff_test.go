package tui

import (
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/colorprofile"
)

// ansiRe strips terminal color codes from highlighter output so tests
// can match against the underlying text without baking in chroma's
// escape sequences.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func stripANSILines(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = stripANSI(x)
	}
	return out
}

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
	// Go source should pick up keyword coloring on the asymmetric
	// (chroma fallback) path — same-line-count edits take the
	// intraline path instead, which deliberately skips chroma. We
	// don't pin the exact color codes — chroma's output may evolve —
	// but the diff should contain at least one ANSI escape,
	// indicating tokens were highlighted rather than streamed plain.
	args := `{"path":"x.go","old_string":"package foo","new_string":"package bar\nfunc x() {}"}`
	got, ok := renderEditDiff(args)
	if !ok {
		t.Fatalf("expected ok")
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("highlighted output should contain ANSI escapes: %q", got)
	}
}

func TestIntralineSpan(t *testing.T) {
	cases := []struct {
		name             string
		oldLine, newLine string
		ds, de, as, ae   int
		ok               bool
	}{
		{"middle change", "count := 1", "count := 2", 9, 10, 9, 10, true},
		{"insertion", "foo()", "foo(bar)", 4, 4, 4, 7, true},
		{"deletion", "foo(bar)", "foo()", 4, 7, 4, 4, true},
		{"identical", "same", "same", 0, 0, 0, 0, false},
		{"full rewrite", "alpha", "omega-zulu", 0, 0, 0, 0, false},
		{"multibyte", "x = \"日本\"", "x = \"日本語\"", 7, 7, 7, 8, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds, de, as, ae, ok := intralineSpan(tc.oldLine, tc.newLine)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if ds != tc.ds || de != tc.de || as != tc.as || ae != tc.ae {
				t.Errorf("spans = del[%d:%d] add[%d:%d], want del[%d:%d] add[%d:%d]",
					ds, de, as, ae, tc.ds, tc.de, tc.as, tc.ae)
			}
		})
	}
}

// Intraline emphasis: a same-line-count replacement renders the
// changed span in the reverse-video emphasis style and the unchanged
// context in the plain state color. Forced color profile — under `go
// test` lipgloss renders plain ASCII, which would make every style
// indistinguishable.
func TestRenderEditDiff_IntralineEmphasis(t *testing.T) {
	prevProfile := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	compat.HasDarkBackground = true
	t.Cleanup(func() { lipgloss.Writer.Profile = prevProfile })

	args := `{"path":"x.go","old_string":"count := 1","new_string":"count := 2"}`
	got, ok := renderEditDiff(args)
	if !ok {
		t.Fatalf("expected ok")
	}
	if want := styleDiffDelEmph.Render("1"); !strings.Contains(got, want) {
		t.Errorf("deleted span %q not emphasized; got %q", want, got)
	}
	if want := styleDiffAddEmph.Render("2"); !strings.Contains(got, want) {
		t.Errorf("added span %q not emphasized; got %q", want, got)
	}
	// Content fidelity survives the styling.
	plain := stripANSI(got)
	for _, want := range []string{"- count := 1", "+ count := 2"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in diff: %q", want, plain)
		}
	}
}

// The card-body variant takes the same intraline path for paired
// replacements and keeps the marker + gutter chrome.
func TestEditFileDiffRows_IntralinePairing(t *testing.T) {
	prevProfile := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	compat.HasDarkBackground = true
	t.Cleanup(func() { lipgloss.Writer.Profile = prevProfile })

	args := `{"path":"x.go","old_string":"limit = 10","new_string":"limit = 99"}`
	rows, ok := editFileDiffRows(args, 80)
	if !ok {
		t.Fatalf("expected ok")
	}
	joined := strings.Join(rows, "\n")
	if want := styleDiffDelEmph.Render("10"); !strings.Contains(joined, want) {
		t.Errorf("deleted span %q not emphasized; got %q", want, joined)
	}
	if want := styleDiffAddEmph.Render("99"); !strings.Contains(joined, want) {
		t.Errorf("added span %q not emphasized; got %q", want, joined)
	}
	plain := stripANSI(joined)
	for _, want := range []string{"│ - limit = 10", "│ + limit = 99"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in rows: %q", want, plain)
		}
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

func TestWriteFileBodyRows_EmptyContentStillRenders(t *testing.T) {
	// A write_file with empty content is a legitimate call (touching a
	// file to clear it). It should still return ok=true with at least
	// one row so the card body isn't completely empty.
	rows, ok := writeFileBodyRows(`{"path":"x.go","content":""}`, 80)
	if !ok {
		t.Fatalf("expected ok=true for empty content")
	}
	if len(rows) == 0 {
		t.Errorf("expected at least one body row even for empty content")
	}
}

func TestWriteFileBodyRows_TruncatesOverflow(t *testing.T) {
	// More than cardBodyLineCap lines should produce a "…N more line(s)"
	// marker as the final row, mirroring editFileDiffRows.
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
