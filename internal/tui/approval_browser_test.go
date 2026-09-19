package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// openBrowserApproval puts a fresh model into the state the real loop leaves
// it in when a browser_* tool asks for approval.
func openBrowserApproval(t *testing.T, tool, preview, args string) Model {
	t.Helper()
	m := newTestModel(t)
	m.decisions = make(chan agent.Decision, 1)
	m, _ = applyMsg(m, agentEventMsg{ev: agent.ApprovalNeeded{ToolName: tool, Preview: preview, ArgsJSON: args}})
	return m
}

// The prompt in the bug report: browser_inspect showed only [Y]/[N]. It must
// now offer session / always / never, with the exact rule spelled out.
func TestApprovalModal_BrowserInspectOffersSessionAlwaysNever(t *testing.T) {
	m := openBrowserApproval(t, "browser_inspect", "browser_inspect(main li:nth-of-type(1))", `{"selector":"main li:nth-of-type(1)"}`)
	if !m.approvalAllowAlwaysOK || m.approvalDerivedRule != "Browser(inspect)" {
		t.Fatalf("allow side = (%t, %q), want (true, Browser(inspect))", m.approvalAllowAlwaysOK, m.approvalDerivedRule)
	}
	if !m.approvalDenyAlwaysOK || m.approvalDerivedDenyRule != "Browser(inspect)" {
		t.Fatalf("deny side = (%t, %q), want (true, Browser(inspect))", m.approvalDenyAlwaysOK, m.approvalDerivedDenyRule)
	}
	grid := ansi.Strip(approvalHotkeyGrid(m.approvalAllowAlwaysOK, m.approvalDerivedRule, m.approvalDenyAlwaysOK, m.approvalDerivedDenyRule))
	for _, want := range []string{
		"[Y]", "yes — allow this call once",
		"[N]", "no — reject this call",
		"[S]", "session — allow Browser(inspect) for this session",
		"[A]", "always — adds Browser(inspect)",
		"[D]", "never — adds Browser(inspect)",
	} {
		if !strings.Contains(grid, want) {
			t.Errorf("modal missing %q; got:\n%s", want, grid)
		}
	}
}

func TestApprovalModal_BrowserKeysEmitDecisions(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want agent.Decision
	}{
		{"s", agent.AllowSession},
		{"a", agent.AllowAlways},
		{"d", agent.DenyAlways},
	} {
		m := openBrowserApproval(t, "browser_click", "browser_click(#go)", `{"selector":"#go"}`)
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: tc.key})
		select {
		case d := <-m.decisions:
			if d != tc.want {
				t.Errorf("key %q sent %v, want %v", tc.key, d, tc.want)
			}
		default:
			t.Errorf("key %q sent no decision", tc.key)
		}
	}
}

// browser_navigate's rule names the site, so the user sees exactly which site
// [S]/[A] would cover before committing.
func TestApprovalModal_BrowserNavigateShowsSite(t *testing.T) {
	m := openBrowserApproval(t, "browser_navigate", "browser_navigate(https://streeteasy.com/for-rent/long-island-city)", `{"url":"https://streeteasy.com/for-rent/long-island-city"}`)
	if m.approvalDerivedRule != "Browser(navigate streeteasy.com)" {
		t.Errorf("derived allow rule = %q, want Browser(navigate streeteasy.com)", m.approvalDerivedRule)
	}
}

// Uploads never get an allow shortcut (they send local file bytes to a site),
// but can still be blocked outright. [S]/[A] must be inert, [D] must work.
func TestApprovalModal_BrowserUploadOffersOnlyNever(t *testing.T) {
	m := openBrowserApproval(t, "browser_upload", "browser_upload(#f, a.txt)", `{"selector":"#f","paths":["a.txt"]}`)
	if m.approvalAllowAlwaysOK || m.approvalDerivedRule != "" {
		t.Fatalf("allow side = (%t, %q), want suppressed", m.approvalAllowAlwaysOK, m.approvalDerivedRule)
	}
	if !m.approvalDenyAlwaysOK || m.approvalDerivedDenyRule != "Browser(upload)" {
		t.Fatalf("deny side = (%t, %q), want (true, Browser(upload))", m.approvalDenyAlwaysOK, m.approvalDerivedDenyRule)
	}
	grid := ansi.Strip(approvalHotkeyGrid(m.approvalAllowAlwaysOK, m.approvalDerivedRule, m.approvalDenyAlwaysOK, m.approvalDerivedDenyRule))
	if strings.Contains(grid, "[S]") || strings.Contains(grid, "[A]") {
		t.Errorf("upload modal must not advertise [S]/[A]; got:\n%s", grid)
	}
	if !strings.Contains(grid, "never — adds Browser(upload)") {
		t.Errorf("upload modal should still offer [D]; got:\n%s", grid)
	}

	for _, key := range []string{"s", "a"} {
		m2 := openBrowserApproval(t, "browser_upload", "browser_upload(#f, a.txt)", `{"selector":"#f","paths":["a.txt"]}`)
		m2, _ = applyMsg(m2, tea.KeyPressMsg{Text: key})
		select {
		case d := <-m2.decisions:
			t.Errorf("key %q should be inert for upload; sent %v", key, d)
		default:
		}
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "d"})
	select {
	case d := <-m.decisions:
		if d != agent.DenyAlways {
			t.Errorf("d sent %v, want DenyAlways", d)
		}
	default:
		t.Error("d sent no decision")
	}
}
