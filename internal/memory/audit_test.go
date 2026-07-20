package memory

import (
	"strings"
	"testing"
	"time"
)

func TestAudit_FlagsCurationIssues(t *testing.T) {
	now := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	loaded := Loaded{
		UserMemories: []MemoryEntry{
			{Scope: "user", Name: "raw-capture", Type: "note", Description: "User likes concise answers", Created: now.AddDate(0, 0, -45), Body: "User likes concise answers"},
			{Scope: "user", Name: "dupe-a", Type: "user", Description: "Prefer table-driven tests", Created: now.AddDate(0, 0, -2), Body: "The user prefers table-driven tests in Go when they fit."},
		},
		ProjectMemories: []MemoryEntry{
			{Scope: "project", Name: "portable", Type: "feedback", Description: "Prefer table-driven tests", Created: now.AddDate(0, 0, -1), Body: "The user prefers table-driven tests in Go when they fit."},
			{Scope: "project", Name: "empty", Type: "project", Description: "Architecture fact", Body: ""},
		},
	}

	report := AuditAt(loaded, now)
	if report.Total != 4 {
		t.Fatalf("Total = %d, want 4", report.Total)
	}
	if report.QuickNotes != 1 {
		t.Fatalf("QuickNotes = %d, want 1", report.QuickNotes)
	}
	want := map[string]bool{
		"quick-note/raw-capture":              false,
		"body-echoes-description/raw-capture": false,
		"portable-in-project/portable":        false,
		"duplicate-description/portable":      false,
		"empty-body/empty":                    false,
	}
	for _, issue := range report.Issues {
		key := issue.Problem + "/" + issue.Name
		if _, ok := want[key]; ok {
			want[key] = true
		}
		if issue.Name == "raw-capture" && issue.Problem == "quick-note" {
			if issue.AgeDays != 45 {
				t.Errorf("raw-capture AgeDays = %d, want 45", issue.AgeDays)
			}
			if !strings.Contains(issue.Detail, "old note") {
				t.Errorf("old quick note detail should prioritize curation: %q", issue.Detail)
			}
		}
	}
	for key, got := range want {
		if !got {
			t.Errorf("missing audit issue %s in %+v", key, report.Issues)
		}
	}
}

func TestPlanCuration_GroupsIssuesIntoBatches(t *testing.T) {
	report := AuditReport{Issues: []AuditIssue{
		{Scope: "user", Name: "a", Type: "user", Problem: "duplicate-description"},
		{Scope: "user", Name: "b", Type: "note", Problem: "quick-note"},
		{Scope: "project", Name: "c", Type: "feedback", Problem: "portable-in-project"},
	}}

	plan := PlanCuration(report)
	if plan.TotalIssues != 3 {
		t.Fatalf("TotalIssues = %d, want 3", plan.TotalIssues)
	}
	if len(plan.Batches) != 3 {
		t.Fatalf("len(Batches) = %d, want 3: %+v", len(plan.Batches), plan.Batches)
	}
	if plan.Batches[0].Title != "Merge duplicate memories" {
		t.Errorf("first batch = %q, want duplicate merge first", plan.Batches[0].Title)
	}
	for _, batch := range plan.Batches {
		if batch.Action == "" || len(batch.Issues) == 0 {
			t.Errorf("batch missing action or issues: %+v", batch)
		}
	}
}

func TestNormalizeAuditText_CollapsesWhitespaceAndMarkdown(t *testing.T) {
	if !sameText("## Durable fact\n", "durable   fact") {
		t.Error("sameText should ignore markdown heading markers and repeated whitespace")
	}
}
