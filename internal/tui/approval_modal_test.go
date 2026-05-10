package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// On a very wide terminal the approval modal caps at 120 columns
// (per Phase 6) instead of stretching across the whole screen.
// Asserted via the rendered top-border line width.
func TestRenderApprovalModal_CapsAt120OnWideTerminal(t *testing.T) {
	m := newTestModel(t)
	m.width = 240
	m.awaitingApproval = true
	m.approvalTool = "run_bash"
	m.approvalPreview = "echo hello"
	m.approvalArgs = `{"command":"echo hello"}`
	m.approvalAllowAlwaysOK = true
	m.approvalDerivedRule = "Bash(echo *)"

	got := renderApprovalModal(m)
	first := strings.SplitN(got, "\n", 2)[0]
	w := ansi.StringWidth(first)
	if w > 124 {
		t.Errorf("approval modal top border width = %d, expected ≤ 124 (120 + 2 corners + 2 outer chars)", w)
	}
	if w < 30 {
		t.Errorf("approval modal too narrow on a 240-col terminal: width=%d", w)
	}
}

// Brackets-first hotkeys, command-only-bright, no permissions.local.json
// inline detail. The toast carries that detail post-decision.
func TestRenderApprovalModal_BracketsFirstNoInlinePermissionsDetail(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.awaitingApproval = true
	m.approvalTool = "run_bash"
	m.approvalPreview = "echo hello"
	m.approvalArgs = `{"command":"echo hello"}`
	m.approvalAllowAlwaysOK = true
	m.approvalDerivedRule = "Bash(echo *)"

	got := stripANSI(renderApprovalModal(m))
	if !strings.Contains(got, "[Y] yes") || !strings.Contains(got, "[N] no") {
		t.Errorf("hotkeys should be brackets-first: %q", got)
	}
	if !strings.Contains(got, "[A] always — adds Bash(echo *)") {
		t.Errorf("always-allow hint missing: %q", got)
	}
	if strings.Contains(got, "permissions.local.json") {
		t.Errorf("permissions.local.json detail should move to the toast: %q", got)
	}
}

// approvalToast formats the post-[A] confirmation that lands in
// scrollback after the user picks "always allow".
func TestApprovalToast_ContainsRule(t *testing.T) {
	got := stripANSI(approvalToast("Bash(go *)"))
	if !strings.Contains(got, "✓ Added Bash(go *) to permissions.local.json") {
		t.Errorf("toast missing expected text: %q", got)
	}
}

// Regression: a body line wider than the box cap must wrap, not
// overflow past the right border. Manifested on write_file approvals
// where a single long markdown line (e.g. a verification note or a
// 200-char URL) blew through `│` into the surrounding scrollback.
func TestRenderApprovalModal_LongLinesWrapInsideBorder(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.awaitingApproval = true
	m.approvalTool = "write_file"
	long := strings.Repeat("x", 300)
	m.approvalArgs = `{"path":"notes.md","content":"` + long + `"}`

	got := renderApprovalModal(m)
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected a multi-line modal, got %d lines", len(lines))
	}
	want := ansi.StringWidth(lines[0])
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != want {
			t.Errorf("line %d width = %d, want %d (right border drift)\n  line: %q",
				i, w, want, stripANSI(line))
		}
	}
}

// Regression: tab-indented body content must not push the right border
// past the rest of the box. ansi.StringWidth treats `\t` as 0-width,
// but the terminal renders it as several columns — without a fixup the
// padding undercounts on tab lines and the right edge drifts. Manifested
// on write_file approvals for Go files (gofmt uses tabs).
func TestRenderApprovalModal_TabIndentedContentAlignsRightBorder(t *testing.T) {
	m := newTestModel(t)
	m.width = 200
	m.awaitingApproval = true
	m.approvalTool = "write_file"
	m.approvalArgs = `{"path":"main.go","content":"package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"}`

	got := renderApprovalModal(m)
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected a multi-line modal, got %d lines", len(lines))
	}
	want := ansi.StringWidth(lines[0])
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != want {
			t.Errorf("line %d width = %d, want %d (right border drift)\n  line: %q",
				i, w, want, stripANSI(line))
		}
	}
}
