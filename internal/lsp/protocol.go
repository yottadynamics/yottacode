package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type lspDiagnostic struct {
	Range struct {
		Start Position `json:"start"`
	} `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

type lspCallHierarchyItem struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	Detail         string `json:"detail"`
	URI            string `json:"uri"`
	SelectionRange struct {
		Start Position `json:"start"`
	} `json:"selectionRange"`
}

func hoverText(raw json.RawMessage) (string, error) {
	var msg struct {
		Contents any `json:"contents"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", fmt.Errorf("parse hover response: %w", err)
	}
	return markupText(msg.Contents), nil
}

func markupText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if value, ok := x["value"].(string); ok {
			return value
		}
		if language, ok := x["language"].(string); ok {
			if value, ok := x["value"].(string); ok {
				return "```" + language + "\n" + value + "\n```"
			}
		}
	case []any:
		parts := make([]string, 0, len(x))
		for _, item := range x {
			if text := strings.TrimSpace(markupText(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func normalizeDiagnostics(path string, in []lspDiagnostic) []Diagnostic {
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		out = append(out, Diagnostic{Path: path, Line: d.Range.Start.Line, Character: d.Range.Start.Character, Severity: severityName(d.Severity), Source: d.Source, Message: d.Message})
	}
	return out
}

func severityName(n int) string {
	switch n {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "unknown"
	}
}

func normalizeCalls(method string, raw json.RawMessage) []CallHierarchyItem {
	direction := "incoming"
	field := "from"
	if method == "callHierarchy/outgoingCalls" {
		direction = "outgoing"
		field = "to"
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil
	}
	out := make([]CallHierarchyItem, 0, len(rows))
	for _, row := range rows {
		var item lspCallHierarchyItem
		if err := json.Unmarshal(row[field], &item); err != nil {
			continue
		}
		out = append(out, CallHierarchyItem{Name: item.Name, Kind: symbolKindName(item.Kind), Detail: item.Detail, Direction: direction, Location: Location{Path: uriToPath(item.URI), Line: item.SelectionRange.Start.Line, Character: item.SelectionRange.Start.Character}})
	}
	return out
}

func readTextBestEffort(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func languageIDForPath(path string) string {
	if lang, ok := ResolveFile(path); ok {
		return lang.ID
	}
	return "plaintext"
}
