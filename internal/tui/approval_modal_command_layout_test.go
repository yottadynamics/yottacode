package tui

import (
	"strings"
	"testing"
)

// Approval previews keep a clear section label above the exact operation so
// the user can distinguish the command from the decision controls.
func TestRenderApprovalModal_LabelsCommandPreview(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.awaitingApproval = true
	m.approvalTool = "run_bash"
	m.approvalArgs = `{"command":"go test ./..."}`

	plain := stripANSI(renderApprovalModal(m))
	if !strings.Contains(plain, "Command") {
		t.Fatalf("approval modal should label the approved operation: %q", plain)
	}
	if !strings.Contains(plain, "$ go test ./...") {
		t.Fatalf("approval modal should show the exact shell command: %q", plain)
	}
}

// The primary decision stays compact even when persistent permission choices
// are available, so users can scan the safety action without reading policy text.
func TestRenderApprovalModal_UsesCompactDecisionLabels(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.awaitingApproval = true
	m.approvalTool = "run_bash"
	m.approvalArgs = `{"command":"go test ./..."}`
	m.approvalAllowAlwaysOK = true
	m.approvalDerivedRule = "Bash(go *)"
	m.approvalDenyAlwaysOK = true
	m.approvalDerivedDenyRule = "Bash(go *)"

	plain := stripANSI(renderApprovalModal(m))
	for _, want := range []string{"[Y]  allow once", "[N]  reject", "[S]  session", "[A]  always", "[D]  never"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("approval modal missing compact action %q: %s", want, plain)
		}
	}
	for _, noisy := range []string{"yes — allow this call once", "no — reject this call", "never — adds"} {
		if strings.Contains(plain, noisy) {
			t.Fatalf("approval modal retained verbose action %q: %s", noisy, plain)
		}
	}
}
