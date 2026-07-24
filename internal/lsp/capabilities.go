package lsp

import "encoding/json"

func parseServerCapabilities(raw json.RawMessage) serverCapabilities {
	// Missing or malformed capabilities are treated as empty only for production
	// clients. Unit tests that construct Client directly leave capOK=false and
	// bypass capability checks so JSON-RPC framing tests stay focused.
	var msg struct {
		Capabilities struct {
			WorkspaceSymbolProvider any `json:"workspaceSymbolProvider"`
			DefinitionProvider      any `json:"definitionProvider"`
			ReferencesProvider      any `json:"referencesProvider"`
			HoverProvider           any `json:"hoverProvider"`
			CodeActionProvider      any `json:"codeActionProvider"`
			CallHierarchyProvider   any `json:"callHierarchyProvider"`
		} `json:"capabilities"`
	}
	_ = json.Unmarshal(raw, &msg)
	return serverCapabilities{
		WorkspaceSymbol: capabilityEnabled(msg.Capabilities.WorkspaceSymbolProvider),
		Definition:      capabilityEnabled(msg.Capabilities.DefinitionProvider),
		References:      capabilityEnabled(msg.Capabilities.ReferencesProvider),
		Hover:           capabilityEnabled(msg.Capabilities.HoverProvider),
		CodeAction:      capabilityEnabled(msg.Capabilities.CodeActionProvider),
		CallHierarchy:   capabilityEnabled(msg.Capabilities.CallHierarchyProvider),
	}
}

func capabilityEnabled(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case map[string]any:
		return true
	default:
		return true
	}
}
