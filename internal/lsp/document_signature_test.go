package lsp

import (
	"encoding/json"
	"testing"
)

func TestDocumentSymbolsFlattensHierarchy(t *testing.T) {
	raw := json.RawMessage(`[
		{"name":"Server","kind":5,"selectionRange":{"start":{"line":3,"character":5}},"children":[
			{"name":"Serve","kind":6,"selectionRange":{"start":{"line":8,"character":9}}}
		]}
	]`)

	got, err := documentSymbols("/tmp/server.go", raw)
	if err != nil {
		t.Fatalf("documentSymbols: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("symbols = %d, want 2: %+v", len(got), got)
	}
	if got[0].Name != "Server" || got[0].Kind != "class" || got[0].Container != "" || got[0].Location.Line != 3 {
		t.Errorf("parent symbol mismatch: %+v", got[0])
	}
	if got[1].Name != "Serve" || got[1].Kind != "method" || got[1].Container != "Server" || got[1].Location.Character != 9 {
		t.Errorf("child symbol mismatch: %+v", got[1])
	}
}

func TestDocumentSymbolsAcceptsFlatSymbolInformation(t *testing.T) {
	raw := json.RawMessage(`[
		{"name":"NewServer","kind":12,"containerName":"server","location":{"uri":"file:///tmp/server.go","range":{"start":{"line":12,"character":1}}}}
	]`)

	got, err := documentSymbols("/ignored.go", raw)
	if err != nil {
		t.Fatalf("documentSymbols: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("symbols = %d, want 1: %+v", len(got), got)
	}
	if got[0].Name != "NewServer" || got[0].Kind != "function" || got[0].Container != "server" || got[0].Location.Path != "/tmp/server.go" {
		t.Errorf("flat symbol mismatch: %+v", got[0])
	}
}

func TestSignatureHelpNormalizesMarkupAndParameterOffsets(t *testing.T) {
	raw := json.RawMessage(`{
		"activeSignature":0,
		"activeParameter":1,
		"signatures":[{
			"label":"fmt.Printf(format string, a ...any)",
			"documentation":{"kind":"markdown","value":"formats output"},
			"parameters":[
				{"label":"format string"},
				{"label":[26,34],"documentation":"values"}
			]
		}]
	}`)

	got, err := signatureHelp(raw)
	if err != nil {
		t.Fatalf("signatureHelp: %v", err)
	}
	if got.ActiveSignature != 0 || got.ActiveParameter != 1 || len(got.Signatures) != 1 {
		t.Fatalf("unexpected signature metadata: %+v", got)
	}
	sig := got.Signatures[0]
	if sig.Documentation != "formats output" || len(sig.Parameters) != 2 {
		t.Fatalf("unexpected signature: %+v", sig)
	}
	if sig.Parameters[0].Label != "format string" {
		t.Errorf("first param label = %q", sig.Parameters[0].Label)
	}
	if sig.Parameters[1].Label != "a ...any" || sig.Parameters[1].Documentation != "values" {
		t.Errorf("second param mismatch: %+v", sig.Parameters[1])
	}
}
