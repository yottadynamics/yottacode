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

// TestMarkdownRenderer_MonochromeThemeStripsColor is a regression test:
// newMarkdownRenderer used to hardcode glamour's "dark" style
// regardless of the active theme, so selecting the "no-color" theme
// muted chroma code-block highlighting (via Highlight: "bw") but left
// assistant markdown prose (headings, bold, links) rendered in
// glamour's baked-in ANSI colors — breaking that theme's "every role
// renders as default terminal foreground" contract. themeMonochrome
// (set from Palette.Monochrome in buildStyles) now switches glamour to
// its colorless "notty" style.
func TestMarkdownRenderer_MonochromeThemeStripsColor(t *testing.T) {
	const doc = "# Heading\n\n**bold** and *italic*"

	colored := newMarkdownRenderer(80).render(doc)
	if !strings.Contains(colored, "\x1b[") {
		t.Fatalf("dark-style renderer should emit ANSI escapes for a heading, got %q", colored)
	}

	prev := themeMonochrome
	themeMonochrome = true
	defer func() { themeMonochrome = prev }()

	mono := newMarkdownRenderer(80).render(doc)
	if strings.Contains(mono, "\x1b[") {
		t.Errorf("monochrome theme should render with no ANSI escapes at all, got %q", mono)
	}
	for _, w := range []string{"Heading", "bold", "italic"} {
		if !strings.Contains(mono, w) {
			t.Errorf("word %q missing from monochrome output: %q", w, mono)
		}
	}
}
