package trust

import "testing"

// TestResolveTrustResult_FailsClosed is a regression for the release
// audit's trust-prompt-interactive-fail-open finding: the consent gate
// must not grant trust unless the user explicitly answered. PromptResult's
// zero value is PromptYes, so an un-answered model must map to PromptNo.
func TestResolveTrustResult_FailsClosed(t *testing.T) {
	if got := resolveTrustResult(trustPickerModel{}); got != PromptNo {
		t.Errorf("unanswered model = %v, want PromptNo (fail closed)", got)
	}
	if got := resolveTrustResult(trustPickerModel{answered: true, result: PromptYes}); got != PromptYes {
		t.Errorf("explicit yes = %v, want PromptYes", got)
	}
	if got := resolveTrustResult(trustPickerModel{answered: true, result: PromptNo}); got != PromptNo {
		t.Errorf("explicit no = %v, want PromptNo", got)
	}
}
