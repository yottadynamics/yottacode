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

// maxHeadings bounds how many headings the shape summary names before
// collapsing the rest into a count.
const maxHeadings = 10

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
		nHeadings int // total seen, including those past the collection cap
		text      = newBoundedTextBuilder(req.Offset, req.MaxChars)
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
				// Only the first maxHeadings are ever rendered, so
				// collecting every heading in a large document just to
				// discard the tail is the same waste as buffering all
				// the text. One extra is retained to distinguish
				// "exactly maxHeadings" from "more than that".
				nHeadings++
				if len(headings) <= maxHeadings {
					headings = append(headings, s)
				}
			}
			text.Add(s)
		}
	}

	if hasMoreAfterCap(f) {
		warnings = append(warnings, fmt.Sprintf("source file exceeds %d bytes; stopped reading at the byte cap", req.MaxBytes))
	}

	if notice := textWindowNotice(req.Offset, req.MaxChars, text.Total(), "visible text"); notice != "" {
		warnings = append(warnings, notice)
	}
	preview := boundedString(text.String(), req.MaxChars)

	shape := "no <title>"
	if title != "" {
		shape = fmt.Sprintf("title: %q", title)
	}
	if len(headings) > 0 {
		shown := headings
		suffix := ""
		if nHeadings > maxHeadings {
			shown = shown[:maxHeadings]
			suffix = fmt.Sprintf(", … (%d total)", nHeadings)
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
