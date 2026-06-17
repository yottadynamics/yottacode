package adapter

import "testing"

func TestSafeToolArgsJSON_NormalizesUnsafeReplayPayloads(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "{}"},
		{"partial", `{"path":`, "{}"},
		{"scalar", `"x"`, "{}"},
		{"array", `["x"]`, "{}"},
		{"null", `null`, "{}"},
		{"object", `{"path":"x"}`, `{"path":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeToolArgsJSON(tc.in); got != tc.want {
				t.Fatalf("safeToolArgsJSON(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateToolCallForHistory_RepairsEmptyButRejectsMalformed(t *testing.T) {
	empty := ToolCall{Name: "read_file"}
	if err := validateToolCallForHistory(&empty); err != nil {
		t.Fatalf("empty args should be repairable: %v", err)
	}
	if empty.ArgsJSON != "{}" {
		t.Fatalf("empty args = %q, want {}", empty.ArgsJSON)
	}

	bad := ToolCall{Name: "read_file", ArgsJSON: `{"path":`}
	if err := validateToolCallForHistory(&bad); err == nil {
		t.Fatal("malformed non-empty args should be rejected")
	}
}
