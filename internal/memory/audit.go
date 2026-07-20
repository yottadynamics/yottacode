package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// AuditIssue is one memory-curation finding. The audit is intentionally
// read-only: it gives humans and future curator agents a queue of memories to
// merge, promote, or delete without changing long-term context silently.
type AuditIssue struct {
	Scope         string
	Name          string
	Type          string
	Created       time.Time
	AgeDays       int
	SourceSession string
	SourceTurn    string
	Problem       string
	Detail        string
	Action        string
}

// AuditReport summarizes memory-store quality. QuickNotes are type=note
// captures: useful raw material, but not the curated facts that should dominate
// long-lived memory.
type AuditReport struct {
	Total      int
	QuickNotes int
	Issues     []AuditIssue
	Health     AuditHealth
}

// AuditHealth is a compact read-only summary of memory-store quality. It is
// intentionally aggregate-only so status surfaces can show memory health without
// dumping every curation issue into context.
type AuditHealth struct {
	TotalMemories         int
	TotalIssues           int
	QuickNotes            int
	OldQuickNotes         int
	DuplicateDescriptions int
	VagueBodies           int
	EmptyBodies           int
	PortableScopeMistakes int
}

// CurationBatch is one read-only group of related audit issues. It gives the
// agent a safe checklist for a later explicit curation turn without mutating
// memory automatically.
type CurationBatch struct {
	Title  string
	Action string
	Issues []AuditIssue
}

// CurationPlan groups audit issues into work batches ordered by the safest
// curation sequence: fix duplicates/weak bodies, promote notes, then clean up
// scope and empty entries.
type CurationPlan struct {
	TotalIssues int
	Batches     []CurationBatch
}

// Audit scans loaded memories for the first curation problems that make an
// agent less human-like over time: raw note buildup, duplicate index lines,
// vague bodies, and portable preferences filed under project scope.
func Audit(l Loaded) AuditReport {
	return AuditAt(l, time.Now())
}

// AuditAt is Audit with an injected clock for deterministic tests.
func AuditAt(l Loaded, now time.Time) AuditReport {
	entries := append([]MemoryEntry{}, l.UserMemories...)
	entries = append(entries, l.ProjectMemories...)
	report := AuditReport{Total: len(entries)}
	seenDescriptions := map[string]MemoryEntry{}
	for _, e := range entries {
		ageDays := memoryAgeDays(e.Created, now)
		if e.Type == "note" {
			report.QuickNotes++
			detail := "quick capture should be reviewed, merged, promoted to a durable type, or deleted"
			if ageDays >= oldNoteDays {
				detail += "; old note should be prioritized"
			}
			report.Issues = append(report.Issues, AuditIssue{
				Scope: e.Scope, Name: e.Name, Type: e.Type, Created: e.Created, AgeDays: ageDays, SourceSession: e.SourceSession, SourceTurn: e.SourceTurn,
				Problem: "quick-note",
				Detail:  detail,
				Action:  "memory_get this note; if durable, memory_save a consolidated user/project memory with a specific type and memory_forget the note",
			})
		}
		if e.Scope == "project" && (e.Type == "user" || e.Type == "feedback") {
			report.Issues = append(report.Issues, AuditIssue{
				Scope: e.Scope, Name: e.Name, Type: e.Type, Created: e.Created, AgeDays: ageDays, SourceSession: e.SourceSession, SourceTurn: e.SourceTurn,
				Problem: "portable-in-project",
				Detail:  "user/feedback memories are usually portable; move to user scope unless this is repo-specific",
				Action:  "if the fact is about the person, memory_save it to user scope and memory_forget the project copy",
			})
		}
		if strings.TrimSpace(e.Body) == "" {
			report.Issues = append(report.Issues, AuditIssue{
				Scope: e.Scope, Name: e.Name, Type: e.Type, Created: e.Created, AgeDays: ageDays, SourceSession: e.SourceSession, SourceTurn: e.SourceTurn,
				Problem: "empty-body",
				Detail:  "memory has no body for future agents to act on",
				Action:  "memory_get any archived/source context if available; otherwise memory_forget this empty entry",
			})
		}
		if sameText(e.Body, e.Description) && strings.TrimSpace(e.Description) != "" {
			report.Issues = append(report.Issues, AuditIssue{
				Scope: e.Scope, Name: e.Name, Type: e.Type, Created: e.Created, AgeDays: ageDays, SourceSession: e.SourceSession, SourceTurn: e.SourceTurn,
				Problem: "body-echoes-description",
				Detail:  "body repeats the index line instead of adding concrete context",
				Action:  "rewrite with memory_save so the body adds concrete context, rationale, paths, or constraints beyond the description",
			})
		}
		key := normalizeAuditText(e.Description)
		if key != "" {
			if prior, ok := seenDescriptions[key]; ok {
				report.Issues = append(report.Issues, AuditIssue{
					Scope: e.Scope, Name: e.Name, Type: e.Type, Created: e.Created, AgeDays: ageDays, SourceSession: e.SourceSession, SourceTurn: e.SourceTurn,
					Problem: "duplicate-description",
					Detail:  "same description as " + prior.Scope + "/" + prior.Name + "; consider consolidating",
					Action:  "memory_get both entries; merge the durable parts into one memory_save call, then memory_forget the duplicate",
				})
			} else {
				seenDescriptions[key] = e
			}
		}
	}
	sort.SliceStable(report.Issues, func(i, j int) bool {
		if report.Issues[i].Problem != report.Issues[j].Problem {
			return report.Issues[i].Problem < report.Issues[j].Problem
		}
		if report.Issues[i].AgeDays != report.Issues[j].AgeDays {
			return report.Issues[i].AgeDays > report.Issues[j].AgeDays
		}
		if report.Issues[i].Scope != report.Issues[j].Scope {
			return report.Issues[i].Scope < report.Issues[j].Scope
		}
		return report.Issues[i].Name < report.Issues[j].Name
	})
	report.Health = SummarizeAuditHealth(report)
	return report
}

// SummarizeAuditHealth derives aggregate issue counts for compact status views.
func SummarizeAuditHealth(report AuditReport) AuditHealth {
	health := AuditHealth{
		TotalMemories: report.Total,
		TotalIssues:   len(report.Issues),
		QuickNotes:    report.QuickNotes,
	}
	for _, issue := range report.Issues {
		switch issue.Problem {
		case "quick-note":
			if issue.AgeDays >= oldNoteDays {
				health.OldQuickNotes++
			}
		case "duplicate-description":
			health.DuplicateDescriptions++
		case "body-echoes-description":
			health.VagueBodies++
		case "empty-body":
			health.EmptyBodies++
		case "portable-in-project":
			health.PortableScopeMistakes++
		}
	}
	return health
}

// PlanCuration groups a report's issues into read-only batches. The plan is
// intentionally high-level: it tells a curator what to work on together, while
// preserving the requirement that every actual memory change happens through
// explicit memory_get / memory_save / memory_forget calls.
func PlanCuration(report AuditReport) CurationPlan {
	plan := CurationPlan{TotalIssues: len(report.Issues)}
	if len(report.Issues) == 0 {
		return plan
	}
	groups := map[string][]AuditIssue{}
	for _, issue := range report.Issues {
		groups[issue.Problem] = append(groups[issue.Problem], issue)
	}
	add := func(problem, title, action string) {
		issues := groups[problem]
		if len(issues) == 0 {
			return
		}
		plan.Batches = append(plan.Batches, CurationBatch{Title: title, Action: action, Issues: issues})
	}
	add("duplicate-description", "Merge duplicate memories", "memory_get each duplicate pair, memory_save one consolidated entry, then memory_forget redundant copies")
	add("body-echoes-description", "Rewrite vague memories", "memory_get each entry and memory_save a body with concrete context beyond the index line")
	add("quick-note", "Promote or delete quick notes", "memory_get each note; promote durable facts with memory_save and memory_forget the raw note, or forget stale notes")
	add("portable-in-project", "Move portable facts to user scope", "memory_get project entries about the person, memory_save them to user scope, then memory_forget the project copies")
	add("empty-body", "Delete or reconstruct empty memories", "recover missing context if available; otherwise memory_forget entries with no useful body")
	return plan
}

// oldNoteDays is the age at which a raw quick-capture note becomes priority
// curation work. If it survived this long as type=note, it is either valuable
// enough to promote or stale enough to delete.
const oldNoteDays = 30

// sameText compares prose after the markdown/frontmatter surfaces have already
// done their job. It catches the common low-value memory shape where the body is
// just the one-line description repeated verbatim.
func sameText(a, b string) bool {
	return normalizeAuditText(a) == normalizeAuditText(b)
}

func memoryAgeDays(created, now time.Time) int {
	if created.IsZero() || now.IsZero() || created.After(now) {
		return -1
	}
	return int(now.Sub(created).Hours() / 24)
}

func FormatAuditCreated(created time.Time) string {
	if created.IsZero() {
		return "unknown"
	}
	return created.UTC().Format(time.DateOnly)
}

func FormatAuditAge(ageDays int) string {
	if ageDays < 0 {
		return "unknown"
	}
	return fmt.Sprintf("%dd", ageDays)
}

func normalizeAuditText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Trim(s, "#>-*+ \t\r\n")
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
