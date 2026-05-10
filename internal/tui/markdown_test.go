package tui

import (
	"strings"
	"testing"
)

func TestMarkdownRenderer_PassesThroughPlainText(t *testing.T) {
	r := newMarkdownRenderer(80)
	out := r.render("just plain text")
	// glamour reflows whitespace and injects ANSI styling, so we don't
	// assert the contiguous original string — only that each word survives.
	for _, w := range []string{"just", "plain", "text"} {
		if !strings.Contains(out, w) {
			t.Errorf("word %q missing from rendered output: %q", w, out)
		}
	}
}

func TestMarkdownRenderer_BoldEmitsAnsi(t *testing.T) {
	r := newMarkdownRenderer(80)
	out := r.render("**bold**")
	// Glamour's "dark" style emits ANSI escapes for bold. We don't assert
	// specific codes (style may vary across versions) — just that the output
	// changed from the literal markdown.
	if out == "**bold**" {
		t.Errorf("markdown wasn't rendered: %q", out)
	}
	if !strings.Contains(out, "bold") {
		t.Errorf("rendered output should still contain the word 'bold': %q", out)
	}
}

func TestMarkdownRenderer_NilSafeRender(t *testing.T) {
	var r *markdownRenderer
	got := r.render("anything")
	if got != "anything" {
		t.Errorf("nil renderer should return input unchanged; got %q", got)
	}
}

func TestMarkdownRenderer_NarrowWidthClampsToDefault(t *testing.T) {
	// Width below the safety threshold should fall back without erroring.
	r := newMarkdownRenderer(5)
	if r == nil || r.r == nil {
		t.Errorf("narrow width should still produce a working renderer")
	}
}
