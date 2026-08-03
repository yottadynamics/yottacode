package lsp

import "encoding/json"

// Capabilities is the stable, printable subset of initialized server
// capabilities that yottacode exposes through status and doctor output.
// It intentionally mirrors only methods used by the agent tool surface.
type Capabilities struct {
	WorkspaceSymbol   bool `json:"workspace_symbol"`
	DocumentSymbol    bool `json:"document_symbol"`
	DocumentHighlight bool `json:"document_highlight"`
	SelectionRange    bool `json:"selection_range"`
	Definition        bool `json:"definition"`
	TypeDefinition    bool `json:"type_definition"`
	Implementation    bool `json:"implementation"`
	References        bool `json:"references"`
	Hover             bool `json:"hover"`
	SignatureHelp     bool `json:"signature_help"`
	CodeAction        bool `json:"code_action"`
	CodeActionResolve bool `json:"code_action_resolve"`
	CallHierarchy     bool `json:"call_hierarchy"`
	Rename            bool `json:"rename"`
	RenamePrepare     bool `json:"rename_prepare"`
	Formatting        bool `json:"formatting"`
}

func parseServerCapabilities(raw json.RawMessage) serverCapabilities {
	// Missing or malformed capabilities are treated as empty only for production
	// clients. Unit tests that construct Client directly leave capOK=false and
	// bypass capability checks so JSON-RPC framing tests stay focused.
	var msg struct {
		Capabilities struct {
			WorkspaceSymbolProvider    any `json:"workspaceSymbolProvider"`
			DocumentSymbolProvider     any `json:"documentSymbolProvider"`
			DocumentHighlightProvider  any `json:"documentHighlightProvider"`
			SelectionRangeProvider     any `json:"selectionRangeProvider"`
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
	renamePrepare := capabilityPrepareEnabled(msg.Capabilities.RenameProvider)
	return serverCapabilities{
		WorkspaceSymbol:   capabilityEnabled(msg.Capabilities.WorkspaceSymbolProvider),
		DocumentSymbol:    capabilityEnabled(msg.Capabilities.DocumentSymbolProvider),
		DocumentHighlight: capabilityEnabled(msg.Capabilities.DocumentHighlightProvider),
		SelectionRange:    capabilityEnabled(msg.Capabilities.SelectionRangeProvider),
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
		RenamePrepare:     renamePrepare,
		Formatting:        capabilityEnabled(msg.Capabilities.DocumentFormattingProvider),
	}
}

func (c serverCapabilities) exported() Capabilities {
	return Capabilities{
		WorkspaceSymbol:   c.WorkspaceSymbol,
		DocumentSymbol:    c.DocumentSymbol,
		DocumentHighlight: c.DocumentHighlight,
		SelectionRange:    c.SelectionRange,
		Definition:        c.Definition,
		TypeDefinition:    c.TypeDefinition,
		Implementation:    c.Implementation,
		References:        c.References,
		Hover:             c.Hover,
		SignatureHelp:     c.SignatureHelp,
		CodeAction:        c.CodeAction,
		CodeActionResolve: c.CodeActionResolve,
		CallHierarchy:     c.CallHierarchy,
		Rename:            c.Rename,
		RenamePrepare:     c.RenamePrepare,
		Formatting:        c.Formatting,
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

func capabilityPrepareEnabled(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	prepare, ok := m["prepareProvider"].(bool)
	if !ok {
		return false
	}
	return prepare
}
