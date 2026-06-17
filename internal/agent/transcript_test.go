package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/subagents"
)

// newTestTranscript opens a transcript file in the test's temp dir
// and returns the writer + the path. Caller is responsible for
// closing it (typically via t.Cleanup).
func newTestTranscript(t *testing.T, agentName string) (*transcriptFile, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), agentName+"-transcript.md")
	cfg := &subagents.AgentConfig{Name: agentName, Source: "test"}
	a := agentArgs{Prompt: "do the thing"}
	tr := openTranscript(path, cfg, a)
	t.Cleanup(func() { tr.close() })
	return tr, path
}

// TestOpenTranscript_CreatesMissingParentDir pins the lazy-creation the
// startup wiring now relies on: TranscriptDir is resolved (not created)
// at session start, so the first dispatch's openTranscript must MkdirAll
// the missing parent itself. If this regressed, a subagent run would
// silently lose its transcript (openTranscript swallows the open error
// and returns a no-op writer).
func TestOpenTranscript_CreatesMissingParentDir(t *testing.T) {
	// Path two levels below a dir that does not exist yet — mirrors a
	// fresh project whose memory/projects/<slug>/subagents/ tree was
	// never created at startup.
	parent := filepath.Join(t.TempDir(), "projects", "demo", "subagents")
	path := filepath.Join(parent, "Explore-abc123.md")
	cfg := &subagents.AgentConfig{Name: "Explore", Source: "test"}
	tr := openTranscript(path, cfg, agentArgs{Prompt: "do the thing"})
	t.Cleanup(func() { tr.close() })

	if tr.f == nil {
		t.Fatal("openTranscript returned a no-op writer; parent dir was not created")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("transcript file not created at %q: %v", path, err)
	}
	if !strings.Contains(readTranscript(t, path), "do the thing") {
		t.Errorf("transcript missing the task prompt; header not written")
	}
}

func readTranscript(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	return string(b)
}

// Streamed ContentToken events should accumulate into one paragraph,
// not produce one line per token. This is the single biggest source
// of transcript noise — every model output character used to be its
// own line, burying the rest of the run under a tower of one-char
// "content: x" entries.
func TestTranscript_AccumulatesContentTokens(t *testing.T) {
	tr, path := newTestTranscript(t, "Explore")
	tr.writeEvent(ContentToken{Text: "I'll "})
	tr.writeEvent(ContentToken{Text: "search "})
	tr.writeEvent(ContentToken{Text: "for "})
	tr.writeEvent(ContentToken{Text: "auth"})
	tr.writeEvent(TurnDone{})
	tr.close()
	got := readTranscript(t, path)
	if !strings.Contains(got, "I'll search for auth\n") {
		t.Errorf("content tokens should land as one paragraph; got:\n%s", got)
	}
	if strings.Count(got, "content:") != 0 {
		t.Errorf("transcript should not contain raw `content:` event prefix; got:\n%s", got)
	}
}

// A ToolStart + ToolResult pair should render as a single markdown
// section: `### <preview>` heading + fenced code body. The original
// preview from ToolStart drives the heading so the transcript matches
// what the user saw live in the TUI.
func TestTranscript_RendersToolBlockAsMarkdown(t *testing.T) {
	tr, path := newTestTranscript(t, "Explore")
	tr.writeEvent(ToolStart{
		ToolName: "grep",
		Preview:  `Grep("AuthMiddleware")`,
	})
	tr.writeEvent(ToolResult{
		ToolName: "grep",
		Output:   "internal/auth/middleware.go:23:func AuthMiddleware(...)\n",
	})
	tr.close()
	got := readTranscript(t, path)
	if !strings.Contains(got, `### Grep("AuthMiddleware")`) {
		t.Errorf("missing markdown heading with preview; got:\n%s", got)
	}
	if !strings.Contains(got, "```\ninternal/auth/middleware.go:23:func AuthMiddleware(...)\n```") {
		t.Errorf("tool output should be inside a fenced code block; got:\n%s", got)
	}
}

// run_bash output should get the `bash` language hint on its fenced
// block so markdown renderers can syntax-highlight shell.
func TestTranscript_FencedLangHintForBash(t *testing.T) {
	tr, path := newTestTranscript(t, "verification")
	tr.writeEvent(ToolStart{ToolName: "run_bash", Preview: "Bash(go test)"})
	tr.writeEvent(ToolResult{ToolName: "run_bash", Output: "ok\n"})
	tr.close()
	got := readTranscript(t, path)
	if !strings.Contains(got, "```bash\nok\n```") {
		t.Errorf("run_bash output should be fenced with the `bash` lang hint; got:\n%s", got)
	}
}

// Errored tool calls should surface the `errored` tag in the meta
// footer so the user can scan a long transcript for failures.
func TestTranscript_ErroredFooterTag(t *testing.T) {
	tr, path := newTestTranscript(t, "Explore")
	tr.writeEvent(ToolStart{ToolName: "read_file", Preview: "Read(missing.go)"})
	tr.writeEvent(ToolResult{
		ToolName: "read_file",
		Output:   "open missing.go: no such file",
		Errored:  true,
	})
	tr.close()
	got := readTranscript(t, path)
	if !strings.Contains(got, "_errored") {
		t.Errorf("errored tool result should produce an `errored` meta footer; got:\n%s", got)
	}
}

// IterCap and TurnDone should compress into horizontal rules rather
// than each producing their own timestamped event line.
func TestTranscript_TurnDoneAndIterCapBecomeRules(t *testing.T) {
	tr, path := newTestTranscript(t, "Explore")
	tr.writeEvent(TurnDone{})
	tr.writeEvent(IterCap{Max: 40})
	tr.close()
	got := readTranscript(t, path)
	if !strings.Contains(got, "---") {
		t.Errorf("TurnDone / IterCap should emit a horizontal rule; got:\n%s", got)
	}
	if !strings.Contains(got, "iteration cap hit (max 40)") {
		t.Errorf("IterCap should mention the cap value; got:\n%s", got)
	}
	if strings.Contains(got, "turn_done") {
		t.Errorf("turn_done literal should not appear; got:\n%s", got)
	}
}

// writeOutcome must flush a pending content buffer and any in-flight
// tool call so the transcript ends consistently even when a run was
// canceled mid-iteration.
func TestTranscript_OutcomeFlushesPendingState(t *testing.T) {
	tr, path := newTestTranscript(t, "Explore")
	tr.writeEvent(ContentToken{Text: "still thinking"})
	tr.writeEvent(ToolStart{ToolName: "grep", Preview: "Grep(x)"})
	// No ToolResult — simulates a cancellation mid-call.
	tr.writeOutcome("runner_canceled", "")
	tr.close()
	got := readTranscript(t, path)
	if !strings.Contains(got, "still thinking") {
		t.Errorf("pending content should flush before outcome; got:\n%s", got)
	}
	if !strings.Contains(got, "grep call was in flight when the run ended") {
		t.Errorf("pending tool should produce an in-flight notice; got:\n%s", got)
	}
	if !strings.Contains(got, "**Outcome:** runner_canceled") {
		t.Errorf("outcome label missing; got:\n%s", got)
	}
}

// Full output is preserved in the transcript even when very large —
// the transcript's job is to be the faithful record. The live TUI
// truncates tool cards; the transcript is where the user goes for the
// complete output.
func TestTranscript_DoesNotTruncateLargeOutput(t *testing.T) {
	tr, path := newTestTranscript(t, "Explore")
	huge := strings.Repeat("x", 20000)
	tr.writeEvent(ToolStart{ToolName: "grep", Preview: "Grep(x)"})
	tr.writeEvent(ToolResult{ToolName: "grep", Output: huge})
	tr.close()
	got := readTranscript(t, path)
	if !strings.Contains(got, huge) {
		t.Errorf("transcript should preserve the full %d-byte output (no truncation); got %d bytes", len(huge), len(got))
	}
	if strings.Contains(got, "truncated") {
		t.Errorf("transcript should not mention truncation; got:\n%s", got)
	}
}

// ErrorEvent should render as bold markdown rather than a raw
// `[ts] error: ...` line.
func TestTranscript_ErrorEventAsBold(t *testing.T) {
	tr, path := newTestTranscript(t, "Explore")
	tr.writeEvent(ErrorEvent{Err: errors.New("provider rate-limited")})
	tr.close()
	got := readTranscript(t, path)
	if !strings.Contains(got, "**Error:** provider rate-limited") {
		t.Errorf("error event should render as bold markdown; got:\n%s", got)
	}
}
