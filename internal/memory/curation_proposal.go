package memory

import (
	"sort"
	"strings"
)

const proposalQuickCaptureType = "note"

// CurationProposal is a read-only suggestion for subjective memory curation.
// It never applies changes; callers must route accepted proposals through
// memory_get, memory_save, memory_forget, or memory_curate_apply explicitly.
type CurationProposal struct {
	Problem        string
	Action         string
	Rationale      string
	Uncertainty    string
	Sources        []ProposalSource
	ProposedMemory *ProposedMemory
	Forget         []ProposalSource
}

// ProposalSource identifies existing memory text that justifies a proposal.
// Proposal rendering should quote these snippets so a curator can verify the
// suggestion without trusting invented context.
type ProposalSource struct {
	Scope         string
	Name          string
	Type          string
	Description   string
	SourceSession string
	SourceTurn    string
	Excerpt       string
}

// ProposedMemory is a candidate memory_save payload. It is intentionally only
// a draft: proposal generation never writes it to disk.
type ProposedMemory struct {
	Scope       string
	Name        string
	Type        string
	Description string
	Content     string
}

// ProposeCuration builds conservative, read-only proposals for subjective
// audit issues. Mechanical fixes remain handled by memory_curate_apply; this
// function covers the cases that require judgment and should be reviewed first.
func ProposeCuration(l Loaded, report AuditReport) []CurationProposal {
	entries := append([]MemoryEntry{}, l.UserMemories...)
	entries = append(entries, l.ProjectMemories...)
	byID := map[string]MemoryEntry{}
	byDesc := map[string][]MemoryEntry{}
	for _, e := range entries {
		byID[memoryID(e.Scope, e.Name)] = e
		key := normalizeAuditText(e.Description)
		if key != "" {
			byDesc[key] = append(byDesc[key], e)
		}
	}

	seenDuplicates := map[string]bool{}
	var proposals []CurationProposal
	for _, issue := range report.Issues {
		e, ok := byID[memoryID(issue.Scope, issue.Name)]
		if !ok {
			continue
		}
		switch issue.Problem {
		case "duplicate-description":
			key := normalizeAuditText(e.Description)
			if seenDuplicates[key] {
				continue
			}
			seenDuplicates[key] = true
			if p, ok := proposeDuplicateMerge(byDesc[key]); ok {
				proposals = append(proposals, p)
			}
		case "body-echoes-description":
			proposals = append(proposals, proposeBodyRewrite(e))
		case "quick-note":
			proposals = append(proposals, proposeQuickNote(e, byDesc[normalizeAuditText(e.Description)], issue.AgeDays))
		}
	}
	return proposals
}

func proposeDuplicateMerge(entries []MemoryEntry) (CurationProposal, bool) {
	if len(entries) < 2 {
		return CurationProposal{}, false
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Scope != entries[j].Scope {
			return entries[i].Scope < entries[j].Scope
		}
		return entries[i].Name < entries[j].Name
	})
	primary := choosePrimaryMemory(entries)
	content := mergeUniqueBodies(entries)
	p := CurationProposal{
		Problem:   "duplicate-description",
		Action:    "merge-candidate",
		Rationale: "These memories share the same normalized description; consolidate only the quoted source content below and forget redundant copies after review.",
		Sources:   proposalSources(entries),
		ProposedMemory: &ProposedMemory{
			Scope:       primary.Scope,
			Name:        primary.Name,
			Type:        primary.Type,
			Description: primary.Description,
			Content:     content,
		},
	}
	for _, e := range entries {
		if e.Scope == primary.Scope && e.Name == primary.Name {
			continue
		}
		p.Forget = append(p.Forget, proposalSource(e))
	}
	return p, true
}

func proposeBodyRewrite(e MemoryEntry) CurationProposal {
	p := CurationProposal{
		Problem:     "body-echoes-description",
		Action:      "rewrite-needs-context",
		Rationale:   "The body repeats the description, so there is not enough source material in the memory itself to draft a trustworthy richer body.",
		Uncertainty: "Use session_recall or other source context before rewriting; do not invent details from the description alone.",
		Sources:     []ProposalSource{proposalSource(e)},
	}
	return p
}

func proposeQuickNote(e MemoryEntry, sameDescription []MemoryEntry, ageDays int) CurationProposal {
	p := CurationProposal{
		Problem: "quick-note",
		Sources: []ProposalSource{proposalSource(e)},
	}
	for _, other := range sameDescription {
		if other.Scope == e.Scope && other.Name == e.Name {
			continue
		}
		if other.Type != proposalQuickCaptureType {
			p.Action = "merge-candidate"
			p.Rationale = "This quick note shares a description with an existing durable memory; review both bodies and merge only source-backed details."
			p.Sources = append(p.Sources, proposalSource(other))
			return p
		}
	}
	if strings.TrimSpace(e.Body) == "" || sameText(e.Body, e.Description) {
		p.Action = "forget-candidate"
		p.Rationale = "This quick note has no detail beyond its index line; it is likely stale capture noise unless source context can be recovered."
		if ageDays >= oldNoteDays {
			p.Rationale += " It is also older than the quick-note priority threshold."
		}
		p.Uncertainty = "If this note matters, recover context with session_recall before deleting."
		return p
	}
	p.Action = "promote-candidate"
	p.Rationale = "This quick note contains body detail that may be durable; promote only if the quoted text is still useful across future sessions."
	p.ProposedMemory = &ProposedMemory{
		Scope:       e.Scope,
		Name:        strings.TrimSuffix(e.Name, "-note"),
		Type:        "reference",
		Description: e.Description,
		Content:     strings.TrimSpace(e.Body),
	}
	return p
}

func choosePrimaryMemory(entries []MemoryEntry) MemoryEntry {
	best := entries[0]
	for _, e := range entries[1:] {
		if best.Type == proposalQuickCaptureType && e.Type != proposalQuickCaptureType {
			best = e
			continue
		}
		if best.Scope == "project" && e.Scope == "user" {
			best = e
		}
	}
	return best
}

func mergeUniqueBodies(entries []MemoryEntry) string {
	seen := map[string]bool{}
	var parts []string
	for _, e := range entries {
		body := strings.TrimSpace(e.Body)
		if body == "" || seen[normalizeAuditText(body)] {
			continue
		}
		seen[normalizeAuditText(body)] = true
		parts = append(parts, body)
	}
	return strings.Join(parts, "\n\n")
}

func proposalSources(entries []MemoryEntry) []ProposalSource {
	out := make([]ProposalSource, 0, len(entries))
	for _, e := range entries {
		out = append(out, proposalSource(e))
	}
	return out
}

func proposalSource(e MemoryEntry) ProposalSource {
	return ProposalSource{
		Scope:         e.Scope,
		Name:          e.Name,
		Type:          e.Type,
		Description:   e.Description,
		SourceSession: e.SourceSession,
		SourceTurn:    e.SourceTurn,
		Excerpt:       excerptMemoryBody(e),
	}
}

func excerptMemoryBody(e MemoryEntry) string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return "(empty body)"
	}
	body = strings.Join(strings.Fields(body), " ")
	const max = 220
	if len([]rune(body)) <= max {
		return body
	}
	r := []rune(body)
	return strings.TrimSpace(string(r[:max])) + "…"
}

func memoryID(scope, name string) string { return scope + "/" + name }
