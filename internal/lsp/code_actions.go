package lsp

import "encoding/json"

// codeActions normalizes action metadata without applying anything. The edit and
// command flags let the agent decide whether a follow-up preview/apply workflow
// is possible while keeping this tool read-only.
func codeActions(raw json.RawMessage, resolveSupported bool) ([]CodeAction, error) {
	var items []struct {
		Title       string            `json:"title"`
		Kind        string            `json:"kind"`
		Edit        json.RawMessage   `json:"edit"`
		Command     json.RawMessage   `json:"command"`
		Diagnostics []json.RawMessage `json:"diagnostics"`
		Data        json.RawMessage   `json:"data"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	out := make([]CodeAction, 0, len(items))
	for _, item := range items {
		out = append(out, CodeAction{
			Title:             item.Title,
			Kind:              item.Kind,
			HasEdit:           len(item.Edit) > 0 && string(item.Edit) != "null",
			HasCommand:        len(item.Command) > 0 && string(item.Command) != "null",
			DiagnosticCount:   len(item.Diagnostics),
			ResolveSupported:  resolveSupported,
			ResolveIncomplete: resolveSupported && len(item.Data) > 0 && (len(item.Edit) == 0 || string(item.Edit) == "null") && (len(item.Command) == 0 || string(item.Command) == "null"),
		})
	}
	return out, nil
}
