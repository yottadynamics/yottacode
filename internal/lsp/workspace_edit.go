package lsp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// TextRange is an LSP half-open range in zero-based line/UTF-16 character
// coordinates. The current applier handles ASCII/UTF-8 byte-compatible ranges;
// callers should keep non-ASCII range conversion covered before broadening use.
type TextRange struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// TextEdit is one text replacement proposed by an LSP WorkspaceEdit.
type TextEdit struct {
	Path    string    `json:"path"`
	Range   TextRange `json:"range"`
	NewText string    `json:"new_text"`
}

// WorkspaceEdit is the normalized, path-keyed edit set yottacode previews and
// applies through its own validators rather than delegating writes to the server.
type WorkspaceEdit struct {
	Edits []TextEdit `json:"edits"`
}

func workspaceEdit(raw json.RawMessage) (WorkspaceEdit, error) {
	var msg struct {
		Changes map[string][]struct {
			Range   TextRange `json:"range"`
			NewText string    `json:"newText"`
		} `json:"changes"`
		DocumentChanges []struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []struct {
				Range   TextRange `json:"range"`
				NewText string    `json:"newText"`
			} `json:"edits"`
		} `json:"documentChanges"`
	}
	if len(raw) == 0 || string(raw) == "null" {
		return WorkspaceEdit{}, nil
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return WorkspaceEdit{}, err
	}
	out := WorkspaceEdit{}
	for uri, edits := range msg.Changes {
		path := uriToPath(uri)
		for _, edit := range edits {
			out.Edits = append(out.Edits, TextEdit{Path: path, Range: edit.Range, NewText: edit.NewText})
		}
	}
	for _, change := range msg.DocumentChanges {
		path := uriToPath(change.TextDocument.URI)
		for _, edit := range change.Edits {
			out.Edits = append(out.Edits, TextEdit{Path: path, Range: edit.Range, NewText: edit.NewText})
		}
	}
	return out, nil
}

func (e WorkspaceEdit) Paths() []string {
	seen := map[string]bool{}
	for _, edit := range e.Edits {
		seen[edit.Path] = true
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func ApplyTextEdits(text string, edits []TextEdit) (string, error) {
	ordered := append([]TextEdit(nil), edits...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Range.Start.Line != ordered[j].Range.Start.Line {
			return ordered[i].Range.Start.Line > ordered[j].Range.Start.Line
		}
		return ordered[i].Range.Start.Character > ordered[j].Range.Start.Character
	})
	out := text
	for _, edit := range ordered {
		start, err := offsetForPosition(out, edit.Range.Start)
		if err != nil {
			return "", err
		}
		end, err := offsetForPosition(out, edit.Range.End)
		if err != nil {
			return "", err
		}
		if end < start {
			return "", fmt.Errorf("invalid edit range: end before start")
		}
		out = out[:start] + edit.NewText + out[end:]
	}
	return out, nil
}

func offsetForPosition(text string, pos Position) (int, error) {
	if pos.Line < 0 || pos.Character < 0 {
		return 0, fmt.Errorf("negative LSP position")
	}
	line, col, offset := 0, 0, 0
	for offset < len(text) {
		if line == pos.Line && col == pos.Character {
			return offset, nil
		}
		if text[offset] == '\n' {
			line++
			col = 0
			offset++
			continue
		}
		_, width := firstRune(text[offset:])
		offset += width
		col++
	}
	if line == pos.Line && col == pos.Character {
		return offset, nil
	}
	return 0, fmt.Errorf("position %d:%d outside document", pos.Line, pos.Character)
}

func firstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

func WorkspaceEditSummary(edit WorkspaceEdit) string {
	paths := edit.Paths()
	if len(paths) == 0 {
		return "(no edits)\n"
	}
	var b strings.Builder
	for _, path := range paths {
		count := 0
		for _, e := range edit.Edits {
			if e.Path == path {
				count++
			}
		}
		fmt.Fprintf(&b, "%s\t%d edit(s)\n", path, count)
	}
	return b.String()
}
