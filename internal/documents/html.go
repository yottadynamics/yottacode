package documents

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// HTMLExtractor strips script/style content and returns visible text
// plus headings, using golang.org/x/net/html's tokenizer (already a
// go.mod dependency) rather than a naive tag strip — a regex-based
// strip can't tell a real close tag from one that merely looks like
// one inside a script string.
type HTMLExtractor struct{}

func (e *HTMLExtractor) Match(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".html" || ext == ".htm"
}

func (e *HTMLExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
	req = req.withDefaults()

	info, err := os.Stat(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	f, err := os.Open(req.Path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("documents: %w", err)
	}
	defer f.Close()

	z := html.NewTokenizer(io.LimitReader(f, req.MaxBytes))

	var (
		title     string
		headings  []string
		text      strings.Builder
		skipDepth int // >0 while inside <script> or <style>
		inTitle   bool
		inHeading bool
		warnings  []string
	)

loop:
	for {
		if err := ctx.Err(); err != nil {
			return ExtractResult{}, err
		}
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			if err := z.Err(); err != io.EOF {
				warnings = append(warnings, fmt.Sprintf("stopped parsing: %v", err))
			}
			break loop
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			switch tok.DataAtom {
			case atom.Script, atom.Style:
				if tt == html.StartTagToken {
					skipDepth++
				}
			case atom.Title:
				inTitle = true
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				inHeading = true
			}
		case html.EndTagToken:
			tok := z.Token()
			switch tok.DataAtom {
			case atom.Script, atom.Style:
				if skipDepth > 0 {
					skipDepth--
				}
			case atom.Title:
				inTitle = false
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				inHeading = false
			}
		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			s := strings.TrimSpace(string(z.Text()))
			if s == "" {
				continue
			}
			if inTitle {
				title = s
			}
			if inHeading {
				headings = append(headings, s)
			}
			if text.Len() > 0 {
				text.WriteByte(' ')
			}
			text.WriteString(s)
		}
	}

	if hasMoreAfterCap(f) {
		warnings = append(warnings, fmt.Sprintf("source file exceeds %d bytes; stopped reading at the byte cap", req.MaxBytes))
	}

	full := text.String()
	if len(full) > req.MaxChars {
		warnings = append(warnings, fmt.Sprintf("showing the first %d of %d characters of visible text", req.MaxChars, len(full)))
	}
	preview := boundedString(full, req.MaxChars)

	shape := "no <title>"
	if title != "" {
		shape = fmt.Sprintf("title: %q", title)
	}
	if len(headings) > 0 {
		const maxHeadings = 10
		shown := headings
		suffix := ""
		if len(shown) > maxHeadings {
			shown = shown[:maxHeadings]
			suffix = ", …"
		}
		shape += fmt.Sprintf("; headings: %s%s", strings.Join(shown, " / "), suffix)
	}

	return ExtractResult{
		Metadata: DocumentMetadata{
			Kind:      "html",
			SizeBytes: info.Size(),
			Shape:     shape,
		},
		Sections: []DocumentSection{{Label: "visible text", Text: preview}},
		Warnings: warnings,
	}, nil
}
