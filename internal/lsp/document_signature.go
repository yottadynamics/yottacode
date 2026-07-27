package lsp

import "encoding/json"

type documentSymbolItem struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	SelectionRange struct {
		Start Position `json:"start"`
	} `json:"selectionRange"`
	Range    TextRange            `json:"range"`
	Children []documentSymbolItem `json:"children"`
}

// documentSymbols normalizes both LSP document-symbol response shapes:
// hierarchical DocumentSymbol objects and flat SymbolInformation objects.
func documentSymbols(path string, raw json.RawMessage) ([]Symbol, error) {
	if responseHasSelectionRanges(raw) {
		var tree []documentSymbolItem
		if err := json.Unmarshal(raw, &tree); err != nil {
			return nil, err
		}
		out := make([]Symbol, 0, len(tree))
		flattenDocumentSymbols(path, "", tree, &out)
		return out, nil
	}

	var flat []struct {
		Name          string `json:"name"`
		Kind          int    `json:"kind"`
		ContainerName string `json:"containerName"`
		Location      struct {
			URI   string `json:"uri"`
			Range TextRange `json:"range"`
		} `json:"location"`
	}
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, err
	}
	out := make([]Symbol, 0, len(flat))
	for _, item := range flat {
		path := uriToPath(item.Location.URI)
		out = append(out, Symbol{Name: item.Name, Kind: symbolKindName(item.Kind), Container: item.ContainerName, Location: Location{Path: path, Line: item.Location.Range.Start.Line, Character: item.Location.Range.Start.Character}, Range: item.Location.Range})
	}
	return out, nil
}

func responseHasSelectionRanges(raw json.RawMessage) bool {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return false
	}
	for _, item := range items {
		if _, ok := item["selectionRange"]; ok {
			return true
		}
	}
	return false
}

func flattenDocumentSymbols(path, container string, items []documentSymbolItem, out *[]Symbol) {
	for _, item := range items {
		rng := item.Range
		if rng.Start == (Position{}) && rng.End == (Position{}) {
			rng = TextRange{Start: item.SelectionRange.Start, End: item.SelectionRange.Start}
		}
		*out = append(*out, Symbol{Name: item.Name, Kind: symbolKindName(item.Kind), Container: container, Location: Location{Path: path, Line: item.SelectionRange.Start.Line, Character: item.SelectionRange.Start.Character}, Range: rng})
		flattenDocumentSymbols(path, item.Name, item.Children, out)
	}
}

func signatureHelp(raw json.RawMessage) (SignatureHelp, error) {
	var msg struct {
		Signatures []struct {
			Label         string `json:"label"`
			Documentation any    `json:"documentation"`
			Parameters    []struct {
				Label         any `json:"label"`
				Documentation any `json:"documentation"`
			} `json:"parameters"`
		} `json:"signatures"`
		ActiveSignature int `json:"activeSignature"`
		ActiveParameter int `json:"activeParameter"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return SignatureHelp{}, err
	}
	out := SignatureHelp{ActiveSignature: msg.ActiveSignature, ActiveParameter: msg.ActiveParameter}
	for _, sig := range msg.Signatures {
		info := SignatureInformation{Label: sig.Label, Documentation: markupText(sig.Documentation)}
		for _, param := range sig.Parameters {
			info.Parameters = append(info.Parameters, ParameterInformation{Label: parameterLabel(sig.Label, param.Label), Documentation: markupText(param.Documentation)})
		}
		out.Signatures = append(out.Signatures, info)
	}
	return out, nil
}

func parameterLabel(signature string, raw any) string {
	switch x := raw.(type) {
	case string:
		return x
	case []any:
		if len(x) == 2 {
			start, ok1 := numberIndex(x[0])
			end, ok2 := numberIndex(x[1])
			if ok1 && ok2 && start >= 0 && end >= start && end <= len(signature) {
				return signature[start:end]
			}
		}
	}
	return ""
}

func numberIndex(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int(f), f == float64(int(f))
}
