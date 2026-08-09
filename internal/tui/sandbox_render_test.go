package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestRenderToolStartLine_NonRunBashUnchanged(t *testing.T) {
	got := renderToolStartLine("read_file", "read_file(x.go)")
	if !strings.Contains(got, "▸ read_file(x.go)") {
		t.Errorf("non-run_bash tool should render plainly: %q", got)
	}
}

// The run_bash preview renders as plain pass-through — there's no
// per-call backend tag today.
func TestRenderToolStartLine_RunBashPlainPassthrough(t *testing.T) {
	got := stripANSI(renderToolStartLine("run_bash", "run_bash: ls -la"))
	if strings.Contains(got, "[") || strings.Contains(got, "]") {
		t.Errorf("run_bash output should not carry a bracket tag: %q", got)
	}
	if !strings.Contains(got, "ls -la") {
		t.Errorf("output should contain command: %q", got)
	}
	if !strings.Contains(got, "run_bash") {
		t.Errorf("output should still identify the tool: %q", got)
	}
}

// The startup card stays slim: no sandbox row and no checks row
// (provider issues/warnings live inside the provider segment as a
// `!`/`?` suffix and in the live status bar). This guards against
// regressions that would re-add either row.
func TestRenderStartupBox_OmitsSandboxAndChecks(t *testing.T) {
	got := stripANSI(renderStartupBox("0.4.0", "abc1234", false, "gpt-4o", "/repo", "20260721-000000.000000", "main", "USER", adapter.ProviderProfile{
		Provider:                adapter.ProviderOpenAI,
		UsesResponsesAPI:        true,
		EnabledBuiltinTools:     []adapter.BuiltinToolKind{adapter.BuiltinToolWebSearch},
		SupportsWebSearch:       true,
		SupportsReasoning:       true,
		SupportsCodeInterpreter: true,
		Warnings:                []string{"API key is empty for a remote provider"},
	}, "", 0))
	if strings.Contains(got, "sandbox") {
		t.Errorf("startup box should not include sandbox row: %q", got)
	}
	if strings.Contains(got, "checks") {
		t.Errorf("startup box should not include checks row: %q", got)
	}
	if !strings.Contains(got, "v0.4.0") || !strings.Contains(got, "abc1234") {
		t.Errorf("startup box title should show version + commit: %q", got)
	}
	if !strings.Contains(got, "provider") || !strings.Contains(got, "openai") {
		t.Errorf("startup box should show provider: %q", got)
	}
	if !strings.Contains(got, "tools") || !strings.Contains(got, "web_search") {
		t.Errorf("startup box should show enabled built-in tools: %q", got)
	}
}

func TestRenderProviderToolCard(t *testing.T) {
	got := stripANSI(renderProviderToolCard("web_search", "searching", "", 80))
	for _, want := range []string{"┌ xAI Web Search", "│ searching the web…", "└ hosted web search"} {
		if !strings.Contains(got, want) {
			t.Errorf("provider tool card missing %q:\n%s", want, got)
		}
	}

	got = stripANSI(renderProviderToolCard("x_search", "in_progress", "", 80))
	for _, want := range []string{"┌ xAI X Search", "│ searching X…", "└ hosted X search"} {
		if !strings.Contains(got, want) {
			t.Errorf("provider tool card missing %q:\n%s", want, got)
		}
	}

	got = stripANSI(renderProviderToolCard("web_search", "completed", "7 results from allowed domains with enough detail to wrap on a narrow terminal", 48))
	for _, want := range []string{"┌ xAI Web Search", "│ web search complete", "│ 7 results", "└ hosted web search"} {
		if !strings.Contains(got, want) {
			t.Errorf("provider tool card missing %q:\n%s", want, got)
		}
	}
	rows := strings.Split(got, "\n")
	for i, row := range rows {
		if len([]rune(row)) > 48 {
			t.Fatalf("provider card row %d exceeded width 48: %q\n%s", i, row, got)
		}
	}
}
