package lsp

import "encoding/json"

func parseServerCapabilities(raw json.RawMessage) serverCapabilities {
	// Missing or malformed capabilities are treated as empty only for production
	// clients. Unit tests that construct Client directly leave capOK=false and
	// bypass capability checks so JSON-RPC framing tests stay focused.
	var msg struct {
		Capabilities struct {
			WorkspaceSymbolProvider    any `json:"workspaceSymbolProvider"`
			DocumentSymbolProvider     any `json:"documentSymbolProvider"`
			DefinitionProvider         any `json:"definitionProvider"`
			TypeDefinitionProvider     any `json:"typeDefinitionProvider"`
			ImplementationProvider     any `json:"implementationProvider"`
			ReferencesProvider         any `json:"referencesProvider"`
			HoverProvider              any `json:"hoverProvider"`
			SignatureHelpProvider      any `json:"signatureHelpProvider"`
			CodeActionProvider         any `json:"codeActionProvider"`
			CallHierarchyProvider      any `json:"callHierarchyProvider"`
			RenameProvider             any `json:"renameProvider"`
			DocumentFormattingProvider any `json:"documentFormattingProvider"`
		} `json:"capabilities"`
	}
	_ = json.Unmarshal(raw, &msg)
	codeActionResolve := capabilityResolveEnabled(msg.Capabilities.CodeActionProvider)
	return serverCapabilities{
		WorkspaceSymbol:   capabilityEnabled(msg.Capabilities.WorkspaceSymbolProvider),
		DocumentSymbol:    capabilityEnabled(msg.Capabilities.DocumentSymbolProvider),
		Definition:        capabilityEnabled(msg.Capabilities.DefinitionProvider),
		TypeDefinition:    capabilityEnabled(msg.Capabilities.TypeDefinitionProvider),
		Implementation:    capabilityEnabled(msg.Capabilities.ImplementationProvider),
		References:        capabilityEnabled(msg.Capabilities.ReferencesProvider),
		Hover:             capabilityEnabled(msg.Capabilities.HoverProvider),
		SignatureHelp:     capabilityEnabled(msg.Capabilities.SignatureHelpProvider),
		CodeAction:        capabilityEnabled(msg.Capabilities.CodeActionProvider),
		CodeActionResolve: codeActionResolve,
		CallHierarchy:     capabilityEnabled(msg.Capabilities.CallHierarchyProvider),
		Rename:            capabilityEnabled(msg.Capabilities.RenameProvider),
		Formatting:        capabilityEnabled(msg.Capabilities.DocumentFormattingProvider),
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

func capabilityResolveEnabled(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	resolve, ok := m["resolveProvider"].(bool)
	if !ok {
		return false
	}
	return resolve
}
