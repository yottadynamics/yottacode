package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// fieldRaw returns the raw JSON bytes of a top-level key in js.
func fieldRaw(t *testing.T, js, key string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatalf("output not a JSON object: %v (%s)", err, js)
	}
	return strings.TrimSpace(string(m[key]))
}

func objSchema(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

func TestCoerceArgs_StringScalarsToTypes(t *testing.T) {
	schema := objSchema(map[string]any{
		"query":       map[string]any{"type": "string"},
		"max_results": map[string]any{"type": "integer"},
		"ratio":       map[string]any{"type": "number"},
		"regex":       map[string]any{"type": "boolean"},
		"ignore_case": map[string]any{"type": "boolean"},
	})
	in := `{"query":"joke","max_results":"5","ratio":"3.5","regex":"true","ignore_case":"False"}`
	out := coerceArgsToSchema(in, schema)

	if got := fieldRaw(t, out, "max_results"); got != "5" {
		t.Errorf("max_results = %s, want 5", got)
	}
	if got := fieldRaw(t, out, "ratio"); got != "3.5" {
		t.Errorf("ratio = %s, want 3.5", got)
	}
	if got := fieldRaw(t, out, "regex"); got != "true" {
		t.Errorf("regex = %s, want true", got)
	}
	if got := fieldRaw(t, out, "ignore_case"); got != "false" {
		t.Errorf("ignore_case = %s, want false", got)
	}
	if got := fieldRaw(t, out, "query"); got != `"joke"` {
		t.Errorf("query = %s, want \"joke\" (string left untouched)", got)
	}
}

func TestCoerceArgs_PassthroughUnchanged(t *testing.T) {
	schema := objSchema(map[string]any{
		"query":       map[string]any{"type": "string"},
		"max_results": map[string]any{"type": "integer"},
		"regex":       map[string]any{"type": "boolean"},
	})
	cases := []struct {
		name string
		in   string
	}{
		// Already-typed args must come back byte-for-byte (no re-marshal).
		{"already typed", `{"query":"joke","max_results":5,"regex":true}`},
		// A non-parseable string is left for the tool's own validation.
		{"non-parseable int", `{"max_results":"abc"}`},
		// Unrecognized boolean spelling is not coerced.
		{"loose bool", `{"regex":"yes"}`},
		// null is preserved.
		{"null value", `{"max_results":null}`},
		// String field that happens to look numeric stays a string.
		{"numeric-looking string field", `{"query":"5"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if out := coerceArgsToSchema(c.in, schema); out != c.in {
				t.Errorf("coerce changed input\n in:  %s\n out: %s", c.in, out)
			}
		})
	}
}

func TestCoerceArgs_FastPathNoCoercibleField(t *testing.T) {
	// Schema with only string fields → fast path returns input unchanged
	// even when a value is a numeric-looking string.
	schema := objSchema(map[string]any{"query": map[string]any{"type": "string"}})
	in := `{"query":"7"}`
	if out := coerceArgsToSchema(in, schema); out != in {
		t.Errorf("fast path mutated input: %s", out)
	}
	// nil schema → unchanged.
	if out := coerceArgsToSchema(in, nil); out != in {
		t.Errorf("nil schema mutated input: %s", out)
	}
}

func TestCoerceArgs_EmptyBecomesObject(t *testing.T) {
	// Empty / whitespace args normalize to "{}" so a tool decodes a valid
	// object — and a replayed call stays valid JSON — instead of failing with
	// "unexpected end of JSON input". This runs before the schema fast path,
	// so it also covers no-argument tools whose schema has no coercible field.
	schema := objSchema(map[string]any{
		"max_results": map[string]any{"type": "integer"},
	})
	for _, tc := range []struct{ name, in string }{
		{"empty", ""},
		{"spaces", "   "},
		{"newline tab", "\n\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out := coerceArgsToSchema(tc.in, schema); out != "{}" {
				t.Errorf("coerceArgsToSchema(%q) = %q, want {}", tc.in, out)
			}
		})
	}
	// nil schema (a no-argument tool) also normalizes empty → "{}".
	if out := coerceArgsToSchema("", nil); out != "{}" {
		t.Errorf("empty args, nil schema = %q, want {}", out)
	}
}

func TestCoerceArgs_MalformedBecomesObject(t *testing.T) {
	// A partial tool-argument fragment must not be replayed into history as-is.
	// Tools will report their ordinary missing-field error from {}, and the next
	// Responses/openai-auth request remains valid instead of failing provider-side
	// on malformed function_call arguments.
	schema := objSchema(map[string]any{"path": map[string]any{"type": "string"}})
	for _, tc := range []struct{ name, in string }{
		{"partial object", `{"path":`},
		{"scalar", `"path"`},
		{"array", `["path"]`},
		{"null", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out := coerceArgsToSchema(tc.in, schema); out != "{}" {
				t.Errorf("coerceArgsToSchema(%q) = %q, want {}", tc.in, out)
			}
		})
	}
}

func TestCoerceArgs_UnknownFieldUntouched(t *testing.T) {
	schema := objSchema(map[string]any{
		"max_results": map[string]any{"type": "integer"},
	})
	out := coerceArgsToSchema(`{"max_results":"5","foo":"bar"}`, schema)
	if got := fieldRaw(t, out, "max_results"); got != "5" {
		t.Errorf("max_results = %s, want 5", got)
	}
	if got := fieldRaw(t, out, "foo"); got != `"bar"` {
		t.Errorf("foo = %s, want \"bar\" (unknown field preserved)", got)
	}
}

func TestCoerceArgs_NestedObject(t *testing.T) {
	schema := objSchema(map[string]any{
		"opts": objSchema(map[string]any{
			"limit": map[string]any{"type": "integer"},
		}),
	})
	out := coerceArgsToSchema(`{"opts":{"limit":"3"}}`, schema)
	var m struct {
		Opts struct {
			Limit json.Number `json:"limit"`
		} `json:"opts"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if m.Opts.Limit.String() != "3" {
		t.Errorf("opts.limit = %s, want 3", m.Opts.Limit)
	}
}

func TestCoerceArgs_ArrayItems(t *testing.T) {
	schema := objSchema(map[string]any{
		"ids": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "integer"},
		},
	})
	out := coerceArgsToSchema(`{"ids":["1","2","3"]}`, schema)
	var m struct {
		IDs []json.Number `json:"ids"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if len(m.IDs) != 3 || m.IDs[0] != "1" || m.IDs[2] != "3" {
		t.Errorf("ids = %v, want [1 2 3]", m.IDs)
	}
}

func TestCoerceArgs_UnionTypeWithNull(t *testing.T) {
	schema := objSchema(map[string]any{
		"max_results": map[string]any{"type": []any{"integer", "null"}},
	})
	out := coerceArgsToSchema(`{"max_results":"5"}`, schema)
	if got := fieldRaw(t, out, "max_results"); got != "5" {
		t.Errorf("max_results = %s, want 5 (union [integer,null])", got)
	}
}

// TestLoop_CoercesStringEncodedArgs proves the dispatch layer normalizes
// string-encoded scalar args before the tool's Execute runs — the end-to-end
// guard against the NVIDIA-NIM Llama {"n":"5"} failure.
func TestLoop_CoercesStringEncodedArgs(t *testing.T) {
	rec := &recordingTool{schema: objSchema(map[string]any{
		"n": map[string]any{"type": "integer"},
	})}
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "rec", ArgsJSON: `{"n":"5"}`})},
		{sseToken("done"), sseDone("done")},
	}}
	reg := NewRegistry()
	reg.Register(rec)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if rec.gotArgs != `{"n":5}` {
		t.Errorf("Execute received %q, want %q (coercion before dispatch)", rec.gotArgs, `{"n":5}`)
	}
}

func TestLoop_MalformedToolArgsNormalizedBeforeHistory(t *testing.T) {
	// Regression for provider freezes after a tool arg stream produced malformed
	// JSON: the tool may still fail validation, but the assistant tool_call saved
	// in history must carry valid object JSON so the follow-up provider request can
	// continue instead of rejecting the conversation.
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "rec", ArgsJSON: `{"path":`})},
		{sseToken("done"), sseDone("done")},
	}}
	rec := &recordingTool{schema: objSchema(map[string]any{"path": map[string]any{"type": "string"}})}
	reg := NewRegistry()
	reg.Register(rec)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	if _, err := runTurnSync(t, context.Background(), cfg, &hist, nil); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if rec.gotArgs != "{}" {
		t.Fatalf("Execute received %q, want {}", rec.gotArgs)
	}
	if len(hist) < 2 || len(hist[1].ToolCalls) != 1 {
		t.Fatalf("history missing assistant tool call: %+v", hist)
	}
	if got := hist[1].ToolCalls[0].ArgsJSON; got != "{}" {
		t.Fatalf("history tool args = %q, want {}", got)
	}
}

// recordingTool captures the exact argsJSON its Execute receives.
type recordingTool struct {
	schema  map[string]any
	gotArgs string
}

func (r *recordingTool) Name() string                 { return "rec" }
func (r *recordingTool) Description() string          { return "records args" }
func (r *recordingTool) Schema() map[string]any       { return r.schema }
func (r *recordingTool) RequiresApproval(string) bool { return false }
func (r *recordingTool) ParallelSafe(string) bool     { return false }
func (r *recordingTool) PreviewCall(string) string    { return "rec()" }
func (r *recordingTool) Execute(_ context.Context, args string) (string, error) {
	r.gotArgs = args
	return "ok", nil
}
