package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/compaction"
)

func TestSplitHistoryForSummary_ExtractsPriorSummaryAndNote(t *testing.T) {
	history := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "SYS"},
		{Role: adapter.RoleUser, Content: summaryUserPreamble},
		{Role: adapter.RoleAssistant, Content: "[Session summary — compressed at turn 4]\n\nOLD SUMMARY\n\n<files>\n(RW) a.go\n</files>"},
		{Role: adapter.RoleUser, Content: "next request\n\n" + compaction.ProgressMarker + "\n\nMID NOTE\n\n<files>\n(Read) b.go\n</files>"},
		{Role: adapter.RoleAssistant, Content: "working"},
	}
	middle, prev, files := splitHistoryForSummary(history)
	if len(middle) != 2 || middle[0].Content != "next request" || middle[1].Content != "working" {
		t.Fatalf("middle = %+v", middle)
	}
	if !strings.Contains(prev, "OLD SUMMARY") || !strings.Contains(prev, "MID NOTE") || strings.Contains(prev, "<files>") {
		t.Fatalf("prev = %q", prev)
	}
	if files["a.go"] != compaction.OpRead|compaction.OpWrite || files["b.go"] != compaction.OpRead {
		t.Fatalf("files = %v", files)
	}
}

func TestRunSummarization_SecondRunUpdatesPreviousSummary(t *testing.T) {
	captured := make(chan []adapter.Message, 1)
	stub := &capturingAdapter{captured: captured, out: []adapter.StreamEvent{
		{Kind: adapter.EventTokenDelta, Token: "## Goal and progress\nnew"},
		{Kind: adapter.EventDone, Final: &adapter.Message{}},
	}}
	history := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "SYS"},
		{Role: adapter.RoleUser, Content: summaryUserPreamble},
		{Role: adapter.RoleAssistant, Content: "[Session summary — compressed at turn 2]\n\nOLD SUMMARY"},
		{Role: adapter.RoleUser, Content: "fresh question"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: "1", Name: "edit_file", ArgsJSON: `{"path":"x.go"}`}}},
		{Role: adapter.RoleTool, ToolCallID: "1", Content: "ok"},
	}
	out, _, err := runSummarization(context.Background(), stub, history, 32_000)
	if err != nil {
		t.Fatal(err)
	}
	msgs := <-captured
	if msgs[0].Content != compaction.UpdatePrompt {
		t.Errorf("expected the update prompt on a second compaction")
	}
	body := msgs[1].Content
	if !strings.Contains(body, "<previous-summary>\nOLD SUMMARY\n</previous-summary>") || !strings.Contains(body, "fresh question") {
		t.Errorf("body = %q", body)
	}
	if strings.Count(body, "OLD SUMMARY") != 1 {
		t.Errorf("old summary must appear only as <previous-summary>, not also as conversation")
	}
	if !strings.Contains(out, "(Write) x.go") {
		t.Errorf("summary missing deterministic file list: %q", out)
	}
	for _, h := range compaction.SectionHeaders {
		if !strings.Contains(out, h) {
			t.Errorf("missing section %q", h)
		}
	}
}

func TestComposeSummarizedHistory_DropsOldSyntheticPair(t *testing.T) {
	history := []adapter.Message{
		{Role: adapter.RoleSystem, Content: "SYS"},
		{Role: adapter.RoleUser, Content: summaryUserPreamble},
		{Role: adapter.RoleAssistant, Content: "[Session summary — compressed at turn 2]\n\nOLD"},
		{Role: adapter.RoleUser, Content: "q1"},
		{Role: adapter.RoleAssistant, Content: "a1"},
	}
	out := composeSummarizedHistory(history, "NEW", 32_000)
	n := 0
	for _, m := range out {
		if strings.HasPrefix(m.Content, compaction.SessionSummaryPrefix) {
			n++
			if !strings.Contains(m.Content, "NEW") {
				t.Errorf("surviving summary is not the new one: %q", m.Content)
			}
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one summary message, got %d: %+v", n, out)
	}
}

type failIfCalled struct{ t *testing.T }

func (f failIfCalled) ChatStream(context.Context, []adapter.Message, []adapter.Tool) <-chan adapter.StreamEvent {
	f.t.Error("summarizer must not be called when pruning is enough")
	ch := make(chan adapter.StreamEvent)
	close(ch)
	return ch
}

func TestSummarizeCmd_AutoPruneAloneSkipsSummary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel(t)
	m.fileCfg.Context.DefaultWindow = 200_000
	m.fileCfg.Context.AutoThreshold = 0.85
	m.summarizerAdapter = failIfCalled{t}
	msgs := []adapter.Message{{Role: adapter.RoleSystem, Content: "SYS"}, {Role: adapter.RoleUser, Content: "task"}}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			adapter.Message{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: id, Name: "run_bash", ArgsJSON: `{}`}}},
			adapter.Message{Role: adapter.RoleTool, ToolCallID: id, Content: strings.Repeat("x", 40_000)})
	}
	m.sess.Messages = msgs

	done, ok := m.summarizeCmd(true)().(summaryDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("msg = %#v", done)
	}
	if !done.pruned || len(done.newMessages) != len(msgs) {
		t.Fatalf("expected a prune-only result keeping every message, pruned=%v len=%d", done.pruned, len(done.newMessages))
	}

	// An explicit /summarize never takes the prune-only shortcut.
	m.summarizerAdapter = &capturingAdapter{captured: make(chan []adapter.Message, 1), out: []adapter.StreamEvent{
		{Kind: adapter.EventTokenDelta, Token: "## Goal and progress\nx"},
		{Kind: adapter.EventDone, Final: &adapter.Message{}},
	}}
	manual, _ := m.summarizeCmd(false)().(summaryDoneMsg)
	if manual.pruned {
		t.Fatal("manual /summarize must always summarize")
	}
}

// resume --summarized must read snapshots written before the unified format
// (four sections, no <files> block) as well as new ones.
func TestReadNewestSnapshot_OldAndNewFormats(t *testing.T) {
	cases := map[string]string{
		"old four-section": "[Session summary — compressed at turn 3]\n\n## Decisions made\nA\n\n## Code changes\nB\n\n## Open questions\n(none)\n\n## Preferences expressed\n(none)",
		"new six-section":  "[Session summary — compressed at turn 9]\n\n## Goal and progress\nG\n\n## Next step\nN\n\n<files>\n(Write) a.go\n</files>",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			msgs := []adapter.Message{
				{Role: adapter.RoleUser, Content: summaryUserPreamble},
				{Role: adapter.RoleAssistant, Content: content},
				{Role: adapter.RoleUser, Content: "q"},
			}
			history := append([]adapter.Message{{Role: adapter.RoleSystem, Content: "S"}}, msgs...)
			path, err := writePreSummarySnapshot("sess1", history)
			if err != nil {
				t.Fatal(err)
			}
			_ = path
			got, err := readNewestSnapshot("sess1")
			if err != nil {
				t.Fatal(err)
			}
			want := strings.TrimSpace(content[strings.Index(content, "\n\n")+2:])
			if got != want {
				t.Fatalf("got %q want %q", got, want)
			}
			// The same body must round-trip into the system prompt on resume.
			sys := injectSummaryIntoSystemPrompt(history, got)
			if len(sys) != 1 || !strings.Contains(sys[0].Content, "## Prior session context (summarized)") {
				t.Fatalf("not injected: %+v", sys)
			}
		})
	}
}
