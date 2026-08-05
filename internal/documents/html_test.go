package documents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTMLExtractorMatch(t *testing.T) {
	e := &HTMLExtractor{}
	if !e.Match("page.html") || !e.Match("page.htm") || !e.Match("page.HTML") {
		t.Errorf("HTMLExtractor must match .html/.htm case-insensitively")
	}
	if e.Match("page.xml") {
		t.Errorf("HTMLExtractor must not match .xml")
	}
}

func TestHTMLExtractorStripsScriptAndStyle(t *testing.T) {
	res := mustExtract(t, &HTMLExtractor{}, ExtractRequest{Path: filepath.Join("testdata", "report.html")})

	if res.Metadata.Kind != "html" {
		t.Errorf("Kind = %q, want html", res.Metadata.Kind)
	}
	if !strings.Contains(res.Metadata.Shape, `title: "Quarterly Report"`) {
		t.Errorf("Shape = %q, want it to mention the title", res.Metadata.Shape)
	}
	if !strings.Contains(res.Metadata.Shape, "Results") || !strings.Contains(res.Metadata.Shape, "Details") {
		t.Errorf("Shape = %q, want both headings listed", res.Metadata.Shape)
	}

	text := res.Sections[0].Text
	if !strings.Contains(text, "Revenue grew by 12 percent") {
		t.Errorf("expected body text in extracted content, got %q", text)
	}
	if strings.Contains(text, "console.log") || strings.Contains(text, "color: red") {
		t.Errorf("script/style content leaked into extracted text: %q", text)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings for a small well-formed file: %v", res.Warnings)
	}
}

func TestHTMLExtractorEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.html")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	res := mustExtract(t, &HTMLExtractor{}, ExtractRequest{Path: path})

	if res.Metadata.Shape != "no <title>" {
		t.Errorf("Shape = %q, want %q", res.Metadata.Shape, "no <title>")
	}
}

func TestHTMLExtractorMaxCharsTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.html")
	var b strings.Builder
	b.WriteString("<html><body>")
	for range 50 {
		b.WriteString("<p>some repeated sentence content here.</p>")
	}
	b.WriteString("</body></html>")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res := mustExtract(t, &HTMLExtractor{}, ExtractRequest{Path: path, MaxChars: 100})

	if len(res.Sections[0].Text) > 100+len("…[truncated]") {
		t.Errorf("preview text longer than expected after MaxChars cap: %d bytes", len(res.Sections[0].Text))
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "characters of visible text") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a character-truncation warning, got %v", res.Warnings)
	}
}
