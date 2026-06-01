package agent

import (
	"encoding/json"
	"strconv"
	"strings"
)

// coerceArgsToSchema rewrites string-encoded scalar tool arguments to the JSON
// types the tool's schema declares. Some OpenAI-compatible models — notably
// Meta Llama 3.1/3.3 served by NVIDIA NIM, Ollama, vLLM, etc. — emit numeric
// and boolean arguments as JSON strings ({"max_results":"5"} instead of
// {"max_results":5}). Go's strict encoding/json then rejects the string in an
// int/bool field, the tool call errors, and the model loops retrying the same
// malformed call.
//
// The coercion is conservative and fail-open. It rewrites a value only when
// (a) the schema declares that field integer/number/boolean and (b) the model
// sent a JSON string that cleanly parses to that type. Anything else — args
// that aren't a JSON object, an absent/empty schema, values already the right
// type, or strings that don't parse — is returned byte-for-byte unchanged. A
// compliant provider therefore sees no behavior change, and a genuinely
// malformed call still reaches the tool's own validation with a real error.
//
// Coercion is driven by the local tool Schema(), not by anything the model
// sends, so it is provider- and model-agnostic. It is the type-normalization
// step that first-party stacks (e.g. OpenAI's schema-constrained decoding)
// perform internally but the NIM+Llama path skips.
func coerceArgsToSchema(argsJSON string, schema map[string]any) string {
	// Fast path: if the schema declares no coercible scalar anywhere there is
	// nothing to do. This covers every compliant provider and most calls
	// without parsing the args at all.
	if !schemaHasCoercibleField(schema) {
		return argsJSON
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &obj); err != nil {
		return argsJSON // not a JSON object we understand — leave it untouched
	}
	if coerceObject(obj, schema) == 0 {
		return argsJSON // nothing changed — avoid re-marshal churn
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return argsJSON
	}
	return string(out)
}

// schemaType returns the declared type of a schema node, resolving a union
// like ["integer","null"] to its first non-null member. Returns "" when no
// type is declared.
func schemaType(node map[string]any) string {
	switch t := node["type"].(type) {
	case string:
		return t
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok && s != "null" {
				return s
			}
		}
	}
	return ""
}

// schemaHasCoercibleField reports whether the schema (or any nested object
// property / array item) declares an integer, number, or boolean field.
func schemaHasCoercibleField(node map[string]any) bool {
	if node == nil {
		return false
	}
	switch schemaType(node) {
	case "integer", "number", "boolean":
		return true
	}
	if props, ok := node["properties"].(map[string]any); ok {
		for _, p := range props {
			if pm, ok := p.(map[string]any); ok && schemaHasCoercibleField(pm) {
				return true
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok && schemaHasCoercibleField(items) {
		return true
	}
	return false
}

// coerceObject walks obj against the object schema's properties, coercing
// scalars and recursing into nested objects/arrays. Returns the number of
// values rewritten.
func coerceObject(obj map[string]json.RawMessage, schema map[string]any) int {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return 0
	}
	changed := 0
	for name, raw := range obj {
		prop, ok := props[name].(map[string]any)
		if !ok {
			continue // field not in schema — leave untouched
		}
		if nr, n := coerceValue(raw, prop); n > 0 {
			obj[name] = nr
			changed += n
		}
	}
	return changed
}

// coerceValue dispatches on the declared type of a single value. Returns the
// (possibly rewritten) raw value and the number of coercions applied.
func coerceValue(raw json.RawMessage, prop map[string]any) (json.RawMessage, int) {
	switch schemaType(prop) {
	case "integer", "number", "boolean":
		return coerceScalar(raw, schemaType(prop))
	case "object":
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) != nil {
			return raw, 0
		}
		if n := coerceObject(nested, prop); n > 0 {
			if b, err := json.Marshal(nested); err == nil {
				return b, n
			}
		}
	case "array":
		items, ok := prop["items"].(map[string]any)
		if !ok {
			return raw, 0
		}
		var elems []json.RawMessage
		if json.Unmarshal(raw, &elems) != nil {
			return raw, 0
		}
		changed := 0
		for i, e := range elems {
			if ne, n := coerceValue(e, items); n > 0 {
				elems[i] = ne
				changed += n
			}
		}
		if changed > 0 {
			if b, err := json.Marshal(elems); err == nil {
				return b, changed
			}
		}
	}
	return raw, 0
}

// coerceScalar rewrites a JSON string into the declared scalar type when it
// parses cleanly. A value that is not a JSON string (already typed, null, an
// object, etc.) or a string that does not parse is left unchanged.
func coerceScalar(raw json.RawMessage, typ string) (json.RawMessage, int) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return raw, 0 // not a JSON string — already typed or non-scalar
	}
	t := strings.TrimSpace(s)
	switch typ {
	case "integer":
		if n, err := strconv.ParseInt(t, 10, 64); err == nil {
			return json.RawMessage(strconv.FormatInt(n, 10)), 1
		}
	case "number":
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			if b, err := json.Marshal(f); err == nil {
				return b, 1
			}
		}
	case "boolean":
		switch t {
		case "true", "True", "TRUE":
			return json.RawMessage("true"), 1
		case "false", "False", "FALSE":
			return json.RawMessage("false"), 1
		}
	}
	return raw, 0
}
