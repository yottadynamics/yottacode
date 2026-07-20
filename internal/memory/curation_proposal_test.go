package memory

import (
	"strings"
	"testing"
)

func TestProposeCuration_DuplicateDescriptionDraftsMerge(t *testing.T) {
	loaded := Loaded{UserMemories: []MemoryEntry{
		{Scope: "user", Name: "dupe-a", Type: "user", Description: "Prefers concise answers", Body: "The user prefers concise answers with direct tradeoffs."},
		{Scope: "user", Name: "dupe-b", Type: "feedback", Description: "Prefers concise answers", Body: "Avoid long recaps unless the user asks for detail."},
	}}
	report := Audit(loaded)

	proposals := ProposeCuration(loaded, report)
	if len(proposals) != 1 {
		t.Fatalf("len(proposals) = %d, want 1: %+v", len(proposals), proposals)
	}
	p := proposals[0]
	if p.Action != "merge-candidate" || p.ProposedMemory == nil {
		t.Fatalf("proposal = %+v, want merge candidate with proposed memory", p)
	}
	for _, want := range []string{"direct tradeoffs", "Avoid long recaps"} {
		if !strings.Contains(p.ProposedMemory.Content, want) {
			t.Errorf("merged content missing %q: %q", want, p.ProposedMemory.Content)
		}
	}
	if len(p.Forget) != 1 {
		t.Errorf("len(Forget) = %d, want 1", len(p.Forget))
	}
}

func TestProposeCuration_BodyEchoRefusesToInventContext(t *testing.T) {
	loaded := Loaded{UserMemories: []MemoryEntry{
		{Scope: "user", Name: "echo", Type: "user", Description: "Prefers concise answers", Body: "Prefers concise answers"},
	}}
	report := Audit(loaded)

	proposals := ProposeCuration(loaded, report)
	var found bool
	for _, p := range proposals {
		if p.Problem == "body-echoes-description" {
			found = true
			if p.Action != "rewrite-needs-context" {
				t.Errorf("Action = %q, want rewrite-needs-context", p.Action)
			}
			if p.ProposedMemory != nil {
				t.Errorf("body echo proposal must not invent a replacement: %+v", p.ProposedMemory)
			}
			if !strings.Contains(p.Uncertainty, "do not invent") {
				t.Errorf("uncertainty should warn against invention: %q", p.Uncertainty)
			}
		}
	}
	if !found {
		t.Fatalf("missing body-echoes-description proposal: %+v", proposals)
	}
}

func TestProposeCuration_QuickNoteClassifiesPromoteMergeForget(t *testing.T) {
	loaded := Loaded{
		UserMemories: []MemoryEntry{
			{Scope: "user", Name: "promote-note", Type: "note", Description: "Debugging preference", Body: "The user prefers root-cause summaries instead of pasted stack traces."},
			{Scope: "user", Name: "merge-note", Type: "note", Description: "Table-driven tests", Body: "Prefers table-driven tests."},
			{Scope: "user", Name: "durable", Type: "user", Description: "Table-driven tests", Body: "The user prefers table-driven Go tests where they fit."},
			{Scope: "user", Name: "forget-note", Type: "note", Description: "Remember thing", Body: "Remember thing"},
		},
	}
	report := Audit(loaded)
	proposals := ProposeCuration(loaded, report)
	actions := map[string]string{}
	for _, p := range proposals {
		if p.Problem != "quick-note" || len(p.Sources) == 0 {
			continue
		}
		actions[p.Sources[0].Name] = p.Action
	}
	want := map[string]string{
		"promote-note": "promote-candidate",
		"merge-note":   "merge-candidate",
		"forget-note":  "forget-candidate",
	}
	for name, action := range want {
		if actions[name] != action {
			t.Errorf("quick note %s action = %q, want %q (all actions: %+v)", name, actions[name], action, actions)
		}
	}
}
