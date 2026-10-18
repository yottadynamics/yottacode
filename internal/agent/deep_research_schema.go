package agent

import (
	"encoding/json"
	"strings"
)

// The JSON shapes the deep_research sub-agents must return, defined once.
// schemaPrompt renders them into every prompt the tool sends, so the shape a
// model is asked for cannot drift from the one the validators in
// deep_research.go decode (a test also checks the examples in the builtin
// agent definitions against these). Subagents have no provider-level schema
// enforcement; these are instructions plus Go-side validation, not a
// constrained-decoding contract.

func jsonStr(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func jsonEnum(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func jsonArray(items map[string]any, max int) map[string]any {
	return map[string]any{"type": "array", "maxItems": max, "items": items}
}

func jsonObject(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}

var (
	researchPlanSchema = jsonObject(map[string]any{
		"questions": jsonArray(jsonStr("one self-contained research question"), deepResearchMaxBreadth),
	}, "questions")

	researchResultSchema = jsonObject(map[string]any{
		"claims": jsonArray(jsonObject(map[string]any{
			"claim":          jsonStr("one atomic factual statement"),
			"evidence":       jsonStr("quoted or closely paraphrased supporting text"),
			"source_title":   jsonStr("page or document title"),
			"source_locator": jsonStr("precise URL of the page you opened"),
			"source_type":    jsonEnum("primary", "secondary", "repository", "other"),
			"confidence":     jsonEnum("high", "medium", "low"),
		}, "claim", "evidence", "source_title", "source_locator", "source_type", "confidence"), deepResearchMaxClaimsPerQuestion),
		"uncertainties": jsonArray(jsonStr("a gap, conflict, or unconfirmed point"), 6),
	}, "claims", "uncertainties")

	researchVerdictsSchema = jsonObject(map[string]any{
		"verdicts": jsonArray(jsonObject(map[string]any{
			"claim_id":       jsonStr("a claim_id from the packet, used exactly once"),
			"supported":      map[string]any{"type": "boolean"},
			"reason":         jsonStr("why the claim is or is not supported"),
			"evidence":       jsonStr("independent evidence you accessed"),
			"source_title":   jsonStr("title of the source you checked"),
			"source_locator": jsonStr("URL of the source you checked"),
		}, "claim_id", "supported", "reason"), deepResearchCandidateCap),
	}, "verdicts")
)

// schemaPrompt renders a schema as a trailing prompt section.
func schemaPrompt(schema map[string]any) string {
	b, _ := json.MarshalIndent(schema, "", "  ")
	return "\n\nReply with ONE JSON object matching this JSON Schema and nothing else (no prose, no code fence):\n" +
		"<json-schema>\n" + string(b) + "\n</json-schema>"
}

// schemaPaths lists every property path in a schema, e.g. "claims[].claim".
func schemaPaths(schema map[string]any, prefix string) []string {
	var out []string
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, sub := range props {
			p := prefix + name
			out = append(out, p)
			if m, ok := sub.(map[string]any); ok {
				out = append(out, schemaPaths(m, p+".")...)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		out = append(out, schemaPaths(items, strings.TrimSuffix(prefix, ".")+"[].")...)
	}
	return out
}

// examplePaths lists the property paths of an example JSON value in the same
// notation as schemaPaths (arrays are walked through their first element).
func examplePaths(v any, prefix string) []string {
	var out []string
	switch x := v.(type) {
	case map[string]any:
		for name, sub := range x {
			p := prefix + name
			out = append(out, p)
			out = append(out, examplePaths(sub, p+".")...)
		}
	case []any:
		if len(x) > 0 {
			out = append(out, examplePaths(x[0], strings.TrimSuffix(prefix, ".")+"[].")...)
		}
	}
	return out
}

// clip truncates s to at most n runes. Applied to untrusted model output so a
// runaway claim cannot bloat the verifier packet or the report.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Per-field caps for untrusted sub-agent output.
const (
	clipClaim    = 600
	clipEvidence = 1500
	clipTitle    = 200
	clipLocator  = 500
	clipReason   = 600
)
