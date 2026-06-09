package adapter

import (
	"encoding/json"
	"fmt"
	"strings"
)

// safeToolArgsJSON returns a JSON object string suitable for replaying as a
// provider function-call argument payload. Providers expect arguments to be a
// JSON object encoded as a string; older transcripts or interrupted streams can
// contain blanks, scalars, arrays, null, or malformed fragments. Replaying those
// verbatim can make every future request fail before the model can recover.
func safeToolArgsJSON(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return "{}"
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &obj); err != nil || obj == nil {
		return "{}"
	}
	return args
}

// validateToolCallForHistory checks provider-emitted tool calls before they are
// committed as a successful assistant message. Empty args are repairable, but a
// malformed non-empty fragment means the stream was cut mid-call and must be a
// hard stream error instead of a poisoned history item.
func validateToolCallForHistory(tc *ToolCall) error {
	if strings.TrimSpace(tc.Name) == "" {
		return fmt.Errorf("adapter: invalid tool call: function name is required")
	}
	args := strings.TrimSpace(tc.ArgsJSON)
	if args == "" {
		tc.ArgsJSON = "{}"
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &obj); err != nil {
		return fmt.Errorf("adapter: invalid tool call %s arguments: %w", tc.Name, err)
	}
	if obj == nil {
		return fmt.Errorf("adapter: invalid tool call %s arguments: expected JSON object", tc.Name)
	}
	return nil
}

func validateToolCallsForHistory(calls []ToolCall) error {
	for i := range calls {
		if err := validateToolCallForHistory(&calls[i]); err != nil {
			return err
		}
	}
	return nil
}
