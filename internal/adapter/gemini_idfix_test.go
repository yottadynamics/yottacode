package adapter

import (
	"context"
	"testing"
)

// TestGemini_SynthesizedToolCallIDsAreUniqueAcrossTurns is a regression
// for the release audit's Gemini tool-call ID collision: per-turn `call_0`
// IDs recurred every turn, so lookupToolName (first-ID-match across all
// history) bound a later turn's function response to an earlier turn's
// call. Seeding the counter from the history makes IDs unique.
func TestGemini_SynthesizedToolCallIDsAreUniqueAcrossTurns(t *testing.T) {
	body := geminiSSEBody(
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"path":"main.go"}}}],"role":"model"},"finishReason":"STOP"}]}`,
	)
	srv, _ := geminiCapturingMockServer(t, body)
	ad := newGeminiAdapter(Config{BaseURL: srv.URL, APIKey: "test", Model: "gemini-2.5-flash"})
	tools := []Tool{{Name: "read_file", Description: "read", Schema: map[string]any{"type": "object"}}}

	// Turn 1: no prior history.
	_, _, final1, errs := drainEvents(ad.ChatStream(context.Background(),
		[]Message{{Role: RoleUser, Content: "read main.go"}}, tools))
	if len(errs) > 0 || final1 == nil || len(final1.ToolCalls) != 1 {
		t.Fatalf("turn 1: errs=%v final=%+v", errs, final1)
	}
	id1 := final1.ToolCalls[0].ID

	// Turn 2: history carries turn 1's assistant tool call + its result.
	history := []Message{
		{Role: RoleUser, Content: "read main.go"},
		{Role: RoleAssistant, ToolCalls: final1.ToolCalls},
		{Role: RoleTool, ToolCallID: id1, Content: "package main"},
		{Role: RoleUser, Content: "now read util.go"},
	}
	_, _, final2, errs := drainEvents(ad.ChatStream(context.Background(), history, tools))
	if len(errs) > 0 || final2 == nil || len(final2.ToolCalls) != 1 {
		t.Fatalf("turn 2: errs=%v final=%+v", errs, final2)
	}
	id2 := final2.ToolCalls[0].ID

	if id1 == id2 {
		t.Errorf("tool-call IDs collide across turns (both %q) — lookupToolName would mis-bind", id1)
	}
	if got := lookupToolName(history, id1); got != "read_file" {
		t.Errorf("lookupToolName(%q) = %q, want read_file", id1, got)
	}
}

func TestMaxGeminiToolCallSeq(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1"}, {ID: "call_5"}}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_3"}, {ID: "toolu_other"}}},
	}
	if got := maxGeminiToolCallSeq(msgs); got != 5 {
		t.Errorf("maxGeminiToolCallSeq = %d, want 5", got)
	}
	if got := maxGeminiToolCallSeq(nil); got != 0 {
		t.Errorf("maxGeminiToolCallSeq(nil) = %d, want 0", got)
	}
}
