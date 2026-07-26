package lsp

import (
	"encoding/json"
	"fmt"
	"strings"
)

type rawCodeAction struct {
	Index       int
	Raw         json.RawMessage
	Title       string            `json:"title"`
	Kind        string            `json:"kind"`
	Edit        json.RawMessage   `json:"edit"`
	Command     json.RawMessage   `json:"command"`
	Diagnostics []json.RawMessage `json:"diagnostics"`
	Data        json.RawMessage   `json:"data"`
}

// codeActions normalizes action metadata without applying anything. The edit and
// command flags let the agent decide whether a follow-up preview/apply workflow
// is possible while keeping this tool read-only.
func codeActions(raw json.RawMessage, resolveSupported bool) ([]CodeAction, error) {
	items, err := rawCodeActions(raw)
	if err != nil {
		return nil, err
	}
	out := make([]CodeAction, 0, len(items))
	for _, item := range items {
		out = append(out, codeActionSummary(item, resolveSupported))
	}
	return out, nil
}

func rawCodeActions(raw json.RawMessage) ([]rawCodeAction, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	out := make([]rawCodeAction, 0, len(items))
	for i, item := range items {
		action, err := normalizeCodeAction(item, i, false)
		if err != nil {
			return nil, err
		}
		out = append(out, action)
	}
	return out, nil
}

func normalizeCodeAction(raw json.RawMessage, index int, resolveSupported bool) (rawCodeAction, error) {
	var action rawCodeAction
	if err := json.Unmarshal(raw, &action); err != nil {
		return rawCodeAction{}, err
	}
	action.Index = index
	action.Raw = append(json.RawMessage(nil), raw...)
	return action, nil
}

func codeActionSummary(item rawCodeAction, resolveSupported bool) CodeAction {
	return CodeAction{
		Index:             item.Index,
		Title:             item.Title,
		Kind:              item.Kind,
		HasEdit:           hasJSONValue(item.Edit),
		HasCommand:        hasJSONValue(item.Command),
		DiagnosticCount:   len(item.Diagnostics),
		ResolveSupported:  resolveSupported,
		ResolveIncomplete: resolveSupported && hasJSONValue(item.Data) && !hasJSONValue(item.Edit) && !hasJSONValue(item.Command),
	}
}

func selectCodeAction(raw json.RawMessage, title string, index int) (rawCodeAction, error) {
	items, err := rawCodeActions(raw)
	if err != nil {
		return rawCodeAction{}, fmt.Errorf("parse codeAction response: %w", err)
	}
	if index >= 0 {
		if index >= len(items) {
			return rawCodeAction{}, fmt.Errorf("code action index %d out of range", index)
		}
		return items[index], nil
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return rawCodeAction{}, fmt.Errorf("code action title or index is required")
	}
	for _, item := range items {
		if item.Title == title {
			return item, nil
		}
	}
	return rawCodeAction{}, fmt.Errorf("code action %q not found", title)
}

func hasJSONValue(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}
