package documents

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// XMLExtractor streams an XML document token by token (encoding/xml's
// Decoder), so a decode error partway through still returns everything
// parsed before it instead of failing the whole call — malformed
// real-world XML is common enough that "here's what we got" beats an
// all-or-nothing error.
type XMLExtractor struct{}

func (e *XMLExtractor) Match(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".xml")
}

func (e *XMLExtractor) Extract(ctx context.Context, req ExtractRequest) (ExtractResult, error) {
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

	dec := xml.NewDecoder(io.LimitReader(f, req.MaxBytes))

	var (
		root      string
		depth     int
		elemCount = map[string]int{}
		text      strings.Builder
		warnings  []string
	)

	for {
		if err := ctx.Err(); err != nil {
			return ExtractResult{}, err
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("stopped parsing: %v", err))
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 && root == "" {
				root = t.Name.Local
			}
			elemCount[t.Name.Local]++
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if s := strings.TrimSpace(string(t)); s != "" {
				if text.Len() > 0 {
					text.WriteByte(' ')
				}
				text.WriteString(s)
			}
		}
	}

	if hasMoreAfterCap(f) {
		warnings = append(warnings, fmt.Sprintf("source file exceeds %d bytes; stopped reading at the byte cap", req.MaxBytes))
	}

	full := text.String()
	if len(full) > req.MaxChars {
		warnings = append(warnings, fmt.Sprintf("showing the first %d of %d characters of text content", req.MaxChars, len(full)))
	}
	preview := boundedString(full, req.MaxChars)

	label := "no root element"
	if root != "" {
		label = fmt.Sprintf("root <%s>", root)
	}

	return ExtractResult{
		Metadata: DocumentMetadata{
			Kind:      "xml",
			SizeBytes: info.Size(),
			Shape:     summarizeXMLShape(root, elemCount),
		},
		Sections: []DocumentSection{{Label: label, Text: preview}},
		Warnings: warnings,
	}, nil
}

// summarizeXMLShape renders the root element name plus its most
// frequent child element tags, capped so a deeply varied document
// doesn't produce an unbounded shape line.
func summarizeXMLShape(root string, counts map[string]int) string {
	if root == "" {
		return "empty document"
	}
	type tagCount struct {
		name  string
		count int
	}
	tags := make([]tagCount, 0, len(counts))
	for name, count := range counts {
		if name == root {
			continue
		}
		tags = append(tags, tagCount{name, count})
	}
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].count != tags[j].count {
			return tags[i].count > tags[j].count
		}
		return tags[i].name < tags[j].name
	})
	const maxTags = 8
	if len(tags) > maxTags {
		tags = tags[:maxTags]
	}
	if len(tags) == 0 {
		return fmt.Sprintf("root <%s>, no child elements", root)
	}
	parts := make([]string, len(tags))
	for i, tc := range tags {
		parts[i] = fmt.Sprintf("%s(%d)", tc.name, tc.count)
	}
	return fmt.Sprintf("root <%s>, elements: %s", root, strings.Join(parts, ", "))
}
