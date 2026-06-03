package agent

import (
	"strings"
	"testing"
)

// TestDispatchPromptAddendum_Steers pins the steering the model needs to
// actually reach for dispatch/integrate, and verifies it stays OUT of the
// default prompt (it's only appended when the experimental gate is on, so
// the prompt never advertises tools that aren't registered).
func TestDispatchPromptAddendum_Steers(t *testing.T) {
	a := DispatchPromptAddendum
	for _, want := range []string{
		"dispatch", "integrate", "in parallel", "DISJOINT", "files", "BACKGROUND",
	} {
		if !strings.Contains(a, want) {
			t.Errorf("DispatchPromptAddendum should mention %q", want)
		}
	}
	if strings.Contains(DefaultSystemPrompt, "dispatch + integrate") {
		t.Error("dispatch steering must NOT be in DefaultSystemPrompt — it is gated and appended only when the feature is enabled")
	}
}
