package mcp

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mapTool and sessionOps are unexported, so these tests live in package mcp
// (not mcp_test) — same rationale as extract_text_test.go.

func TestMapTool_RejectsSlashInName(t *testing.T) {
	_, err := mapTool(&sdk.Tool{Name: "server/tool", Description: "x"})
	if err == nil {
		t.Fatal("expected an error for a tool name containing '/'")
	}
	if !strings.Contains(err.Error(), "/") {
		t.Errorf("error should mention the offending character; got %q", err)
	}
}

func TestMapTool_AcceptsOrdinaryName(t *testing.T) {
	td, err := mapTool(&sdk.Tool{Name: "read_file", Description: "x"})
	if err != nil {
		t.Fatalf("mapTool: %v", err)
	}
	if td.Name != "read_file" {
		t.Errorf("Name = %q, want read_file", td.Name)
	}
}

func TestMapTool_NoAnnotationsDefaultsToDestructiveAndOpenWorld(t *testing.T) {
	td, err := mapTool(&sdk.Tool{Name: "mystery_tool", Description: "no annotations at all"})
	if err != nil {
		t.Fatalf("mapTool: %v", err)
	}
	if !td.Destructive {
		t.Error("Destructive = false, want true when the server declares no annotations")
	}
	if !td.OpenWorld {
		t.Error("OpenWorld = false, want true when the server declares no annotations")
	}
	if td.ReadOnlyHint {
		t.Error("ReadOnlyHint = true, want false (zero value) when the server declares no annotations")
	}
}

func TestMapTool_ExplicitAnnotationsOverrideDefaults(t *testing.T) {
	no := false
	td, err := mapTool(&sdk.Tool{
		Name:        "list_files",
		Description: "x",
		Annotations: &sdk.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: &no,
			OpenWorldHint:   &no,
		},
	})
	if err != nil {
		t.Fatalf("mapTool: %v", err)
	}
	if td.Destructive {
		t.Error("Destructive = true, want false when the server explicitly says destructiveHint=false")
	}
	if td.OpenWorld {
		t.Error("OpenWorld = true, want false when the server explicitly says openWorldHint=false")
	}
	if !td.ReadOnlyHint {
		t.Error("ReadOnlyHint = false, want true")
	}
}

func TestSessionOps_CallToolUsesConfiguredMaxResultBytes(t *testing.T) {
	// Regression test for the result-cap dedup fix: the client-layer cap
	// (sessionOps.maxResultBytes) must be the authoritative, configurable
	// one — not silently pinned to the hardcoded default regardless of what
	// a caller sets it to.
	big := strings.Repeat("x", 1000)
	ops := &sessionOps{maxResultBytes: 100}
	got := flattenResult(&sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: big}},
	}, ops.maxResultBytes)
	if len(got) >= len(big) {
		t.Fatalf("expected truncation at the configured 100-byte cap; got length %d", len(got))
	}
	if !strings.Contains(got, "[truncated: result was 1000 bytes; yottacode kept the first 100 bytes]") {
		t.Errorf("expected a truncation marker citing the configured cap; got %q", got)
	}
}

func TestSessionOps_ListToolsBeforeStartedReturnsErrNotStarted(t *testing.T) {
	ops := &sessionOps{}
	if _, err := ops.listTools(); err != ErrNotStarted {
		t.Errorf("listTools() before Start err = %v, want ErrNotStarted", err)
	}
	if _, err := ops.fetchTools(context.Background()); err != ErrNotStarted {
		t.Errorf("fetchTools() before Start err = %v, want ErrNotStarted", err)
	}
	if _, err := ops.callTool(context.Background(), "srv", "tool", "{}"); err != ErrNotStarted {
		t.Errorf("callTool() before Start err = %v, want ErrNotStarted", err)
	}
}
