package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Pure logic for the deep_research workflow (Plan → Research → Verify →
// Report). Everything the model could get wrong — JSON shape, claim IDs,
// citation markers — is validated here in Go rather than trusted to a
// prompt, so a sloppy sub-agent degrades the report to "Partial" instead
// of corrupting it. deep_research_tool.go drives the sub-agents; this file
// has no agent dependency and is unit-tested directly.

const (
	// deepResearchDefaultBreadth is the default number of parallel
	// researchers (and so the max number of planned questions). The caller
	// may ask for 2..deepResearchMaxBreadth.
	deepResearchDefaultBreadth = 4
	deepResearchMinBreadth     = 2
	deepResearchMaxBreadth     = 6

	// deepResearchMaxClaimsPerQuestion bounds one researcher's output.
	deepResearchMaxClaimsPerQuestion = 6
	// deepResearchCandidateCap bounds the claims sent to verification, so
	// verification cost does not scale with how chatty the researchers are.
	deepResearchCandidateCap = 24
	// deepResearchVerifierShards is how many verifiers split the claims.
	deepResearchVerifierShards = 2
)

// researchClaim is one candidate claim that survived structural checks.
type researchClaim struct {
	ID            string `json:"id"`
	Claim         string `json:"claim"`
	Evidence      string `json:"evidence"`
	SourceTitle   string `json:"source_title"`
	SourceLocator string `json:"source_locator"`
	SourceType    string `json:"source_type"`
	Confidence    string `json:"confidence"`
}

// verifiedClaim is a candidate claim a verifier independently confirmed.
type verifiedClaim struct {
	researchClaim
	VerifierEvidence      string
	VerifierSourceTitle   string
	VerifierSourceLocator string
	VerifierNote          string
}

type rawResearch struct {
	Claims        []researchClaim `json:"claims"`
	Uncertainties []string        `json:"uncertainties"`
}

type rawVerdict struct {
	ClaimID       string `json:"claim_id"`
	Supported     bool   `json:"supported"`
	Reason        string `json:"reason"`
	Evidence      string `json:"evidence"`
	SourceTitle   string `json:"source_title"`
	SourceLocator string `json:"source_locator"`
}

type rawVerdicts struct {
	Verdicts []rawVerdict `json:"verdicts"`
}

// decodeReplyJSON decodes the first JSON object found in a sub-agent
// reply into v. Models routinely wrap the object in a code fence or add a
// sentence around it; both are tolerated, but the object itself must be
// valid JSON.
func decodeReplyJSON(reply string, v any) error {
	i := strings.Index(reply, "{")
	if i < 0 {
		return errors.New("reply contains no JSON object")
	}
	if err := json.NewDecoder(strings.NewReader(reply[i:])).Decode(v); err != nil {
		return fmt.Errorf("reply is not valid JSON: %w", err)
	}
	return nil
}

// clampBreadth folds a caller-supplied breadth into [min,max], defaulting
// when unset.
func clampBreadth(n int) int {
	if n == 0 {
		return deepResearchDefaultBreadth
	}
	if n < deepResearchMinBreadth {
		return deepResearchMinBreadth
	}
	if n > deepResearchMaxBreadth {
		return deepResearchMaxBreadth
	}
	return n
}

// parsePlanReply extracts up to breadth non-empty, distinct questions.
func parsePlanReply(reply string, breadth int) ([]string, error) {
	var p struct {
		Questions []string `json:"questions"`
	}
	if err := decodeReplyJSON(reply, &p); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, q := range p.Questions {
		q = strings.TrimSpace(q)
		key := strings.ToLower(q)
		if q == "" || seen[key] || len(out) >= breadth {
			continue
		}
		seen[key] = true
		out = append(out, q)
	}
	if len(out) == 0 {
		return nil, errors.New("plan contains no questions")
	}
	return out, nil
}

// parseResearchReply decodes a researcher reply. A reply with the right
// shape but zero usable claims is valid (the question may simply have no
// sourced answer); a reply that does not decode is an error.
func parseResearchReply(reply string) (rawResearch, error) {
	var r rawResearch
	if err := decodeReplyJSON(reply, &r); err != nil {
		return r, err
	}
	if r.Claims == nil {
		return r, errors.New(`reply has no "claims" array`)
	}
	return r, nil
}

func nonBlank(s string) bool { return strings.TrimSpace(s) != "" }

func normalizeEnum(v string, allowed []string, fallback string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if slices.Contains(allowed, v) {
		return v
	}
	return fallback
}

// acceptClaim reports whether c has everything a citation needs, and
// returns it normalized.
func acceptClaim(c researchClaim) (researchClaim, bool) {
	c.Claim = clip(strings.TrimSpace(stripControl(c.Claim)), clipClaim)
	c.Evidence = clip(strings.TrimSpace(stripControl(c.Evidence)), clipEvidence)
	c.SourceTitle = clip(oneLine(c.SourceTitle), clipTitle)
	c.SourceLocator = clip(oneLine(c.SourceLocator), clipLocator)
	if !nonBlank(c.Claim) || !nonBlank(c.Evidence) || !nonBlank(c.SourceTitle) || !nonBlank(c.SourceLocator) {
		return c, false
	}
	c.SourceType = normalizeEnum(c.SourceType, []string{"primary", "secondary", "repository", "other"}, "other")
	c.Confidence = normalizeEnum(c.Confidence, []string{"high", "medium", "low"}, "low")
	return c, true
}

// shardClaims splits claims round-robin across n shards.
func shardClaims(claims []researchClaim, n int) [][]researchClaim {
	if n > len(claims) {
		n = len(claims)
	}
	if n <= 0 {
		return nil
	}
	shards := make([][]researchClaim, n)
	for i, c := range claims {
		shards[i%n] = append(shards[i%n], c)
	}
	return shards
}

// validateVerdictReply requires exactly one verdict per expected claim ID:
// no missing, duplicate, or foreign IDs. A partially-valid verdict set is
// rejected whole — a verifier that cannot keep its IDs straight cannot be
// trusted on the ones it did get right.
func validateVerdictReply(reply string, expected []string) (map[string]rawVerdict, error) {
	var v rawVerdicts
	if err := decodeReplyJSON(reply, &v); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range expected {
		want[id] = true
	}
	got := map[string]rawVerdict{}
	for _, vd := range v.Verdicts {
		id := strings.TrimSpace(vd.ClaimID)
		if !want[id] {
			return nil, fmt.Errorf("verdict for unknown claim_id %q", id)
		}
		if _, dup := got[id]; dup {
			return nil, fmt.Errorf("duplicate verdict for claim_id %q", id)
		}
		got[id] = vd
	}
	if len(got) != len(expected) {
		return nil, fmt.Errorf("got %d verdicts for %d claims", len(got), len(expected))
	}
	return got, nil
}

// acceptVerdict returns the verified claim when the verdict is a
// well-formed "supported".
func acceptVerdict(c researchClaim, v rawVerdict) (verifiedClaim, bool) {
	v.Evidence = clip(strings.TrimSpace(stripControl(v.Evidence)), clipEvidence)
	v.SourceTitle = clip(oneLine(v.SourceTitle), clipTitle)
	v.SourceLocator = clip(oneLine(v.SourceLocator), clipLocator)
	if !v.Supported || !nonBlank(v.Evidence) || !nonBlank(v.SourceTitle) || !nonBlank(v.SourceLocator) {
		return verifiedClaim{}, false
	}
	return verifiedClaim{
		researchClaim:         c,
		VerifierEvidence:      v.Evidence,
		VerifierSourceTitle:   v.SourceTitle,
		VerifierSourceLocator: v.SourceLocator,
		VerifierNote:          clip(strings.TrimSpace(stripControl(v.Reason)), clipReason),
	}, true
}

var (
	citationRe      = regexp.MustCompile(`\[S(\d+)\]`)
	sourcesHeadRe   = regexp.MustCompile(`(?mi)^#{1,6}\s*(sources|references)\b`)
	reportBodyOpen  = "<report-body>"
	reportBodyClose = "</report-body>"
)

// extractReportBody pulls the text between <report-body> tags.
func extractReportBody(reply string) (string, bool) {
	_, rest, ok := strings.Cut(reply, reportBodyOpen)
	if !ok {
		return "", false
	}
	body, _, ok := strings.Cut(rest, reportBodyClose)
	body = strings.TrimSpace(stripControl(body))
	return body, ok && body != ""
}

// validateReportBody enforces the citation contract: every packet marker
// [S1]..[Sn] appears, no marker outside that range appears, and the model
// did not write its own Sources section (the caller appends the real one).
func validateReportBody(body string, n int) error {
	for i := 1; i <= n; i++ {
		if !strings.Contains(body, fmt.Sprintf("[S%d]", i)) {
			return fmt.Errorf("citation [S%d] is never used", i)
		}
	}
	for _, m := range citationRe.FindAllStringSubmatch(body, -1) {
		var k int
		fmt.Sscanf(m[1], "%d", &k)
		if k < 1 || k > n {
			return fmt.Errorf("citation [S%d] is not in the findings packet", k)
		}
	}
	if sourcesHeadRe.MatchString(body) {
		return errors.New("body contains its own Sources/References section")
	}
	return nil
}

// deepResearchChatLeadRunes caps the chat summary.
const deepResearchChatLeadRunes = 1500

// leadSummary returns the part of a report body worth showing in chat: the
// opening answer, up to the first heading or horizontal rule, cut at a
// paragraph boundary near max runes. A body that opens with a heading (the
// plain finding-list fallback) has no lead, so its beginning is used.
func leadSummary(body string, max int) string {
	body = strings.TrimSpace(body)
	lead := body
	if !strings.HasPrefix(body, "#") {
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			if t := strings.TrimSpace(line); i > 0 && (strings.HasPrefix(t, "#") || t == "---") {
				lead = strings.TrimSpace(strings.Join(lines[:i], "\n"))
				break
			}
		}
	}
	if r := []rune(lead); len(r) > max {
		cut := string(r[:max])
		if i := strings.LastIndex(cut, "\n\n"); i > max/2 {
			cut = cut[:i]
		}
		lead = strings.TrimSpace(cut) + "…"
	}
	return lead + "\n\n(All findings, sources and coverage notes are in the saved report.)"
}

// findingsFallback is the deterministic body used when synthesis fails
// validation: one bullet per verified claim, each carrying its marker.
func findingsFallback(vs []verifiedClaim) string {
	var b strings.Builder
	b.WriteString("## Findings\n")
	for i, v := range vs {
		fmt.Fprintf(&b, "- %s [S%d]\n", oneLine(v.Claim), i+1)
	}
	return b.String()
}

// stripControl removes control characters other than newline and tab. Text
// from sub-agents is derived from arbitrary web pages and ends up in the saved
// report and printed in the terminal, so an escape sequence smuggled through a
// claim (cursor movement, OSC title or clipboard writes) must not survive.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// oneLine strips control characters and collapses all whitespace runs
// (including newlines) to a single space so untrusted titles/locators cannot
// break the report's structure.
func oneLine(s string) string {
	return strings.Join(strings.Fields(stripControl(s)), " ")
}

// renderSources builds the "## Sources" section. Claims that cite the same
// source (and were checked against the same verifier source) share a row.
func renderSources(vs []verifiedClaim) string {
	type row struct {
		cites []int
		claim verifiedClaim
	}
	var rows []*row
	index := map[string]*row{}
	for i, v := range vs {
		key := v.SourceTitle + "\x00" + v.SourceLocator + "\x00" + v.VerifierSourceTitle + "\x00" + v.VerifierSourceLocator
		if r, ok := index[key]; ok {
			r.cites = append(r.cites, i+1)
			continue
		}
		r := &row{cites: []int{i + 1}, claim: v}
		index[key] = r
		rows = append(rows, r)
	}
	var b strings.Builder
	b.WriteString("## Sources\n")
	for _, r := range rows {
		for _, n := range r.cites {
			fmt.Fprintf(&b, "[S%d] ", n)
		}
		fmt.Fprintf(&b, "%s — %s", r.claim.SourceTitle, r.claim.SourceLocator)
		c := r.claim
		if c.VerifierSourceLocator != c.SourceLocator || c.VerifierSourceTitle != c.SourceTitle {
			fmt.Fprintf(&b, " (independently checked against %s — %s)", c.VerifierSourceTitle, c.VerifierSourceLocator)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// bulletList renders notes as a markdown list, one line each.
func bulletList(notes []string) string {
	var b strings.Builder
	for _, n := range notes {
		fmt.Fprintf(&b, "- %s\n", oneLine(n))
	}
	return b.String()
}

// researchSlug turns a query into a short filename-safe slug.
func researchSlug(query string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(query) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 50 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "report"
	}
	return s
}
