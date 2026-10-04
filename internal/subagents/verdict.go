package subagents

import "strings"

// Verdicts a verification-style subagent ends its reply with.
const (
	VerdictPass    = "PASS"
	VerdictFail    = "FAIL"
	VerdictPartial = "PARTIAL"
)

// ParseVerdict returns the verdict from a subagent result whose last non-empty
// line is exactly `VERDICT: PASS|FAIL|PARTIAL` (the contract the verification and
// code-verifier prompts require), or "" when there is no such line. It is
// deliberately strict: markdown around the line or a verdict mid-reply does not
// count, so a caller gating on PASS errs toward refusing.
func ParseVerdict(result string) string {
	lines := strings.Split(strings.TrimRight(result, " \t\r\n"), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	const prefix = "VERDICT: "
	if !strings.HasPrefix(last, prefix) {
		return ""
	}
	switch v := strings.TrimPrefix(last, prefix); v {
	case VerdictPass, VerdictFail, VerdictPartial:
		return v
	}
	return ""
}
