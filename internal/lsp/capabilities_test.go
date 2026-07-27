package lsp

import (
	"encoding/json"
	"testing"
)

func TestParseServerCapabilitiesProductionReadinessProviders(t *testing.T) {
	raw := json.RawMessage(`{
		"capabilities": {
			"documentHighlightProvider": true,
			"selectionRangeProvider": {},
			"renameProvider": {"prepareProvider": true}
		}
	}`)
	caps := parseServerCapabilities(raw)
	if !caps.DocumentHighlight {
		t.Fatal("documentHighlightProvider should enable DocumentHighlight")
	}
	if !caps.SelectionRange {
		t.Fatal("selectionRangeProvider object should enable SelectionRange")
	}
	if !caps.Rename || !caps.RenamePrepare {
		t.Fatalf("rename provider should enable rename and prepare support: %+v", caps)
	}
}

func TestParseServerCapabilitiesMissingAndFalseProviders(t *testing.T) {
	raw := json.RawMessage(`{
		"capabilities": {
			"documentHighlightProvider": false,
			"renameProvider": {"prepareProvider": false}
		}
	}`)
	caps := parseServerCapabilities(raw)
	if caps.DocumentHighlight {
		t.Fatal("false documentHighlightProvider should be disabled")
	}
	if caps.SelectionRange {
		t.Fatal("missing selectionRangeProvider should be disabled")
	}
	if !caps.Rename || caps.RenamePrepare {
		t.Fatalf("rename object should enable rename but not prepare support: %+v", caps)
	}
}
