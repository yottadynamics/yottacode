package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

type stubStreamer struct {
	events []adapter.StreamEvent
	got    []adapter.Message
}

func (s *stubStreamer) ChatStream(_ context.Context, msgs []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	s.got = msgs
	ch := make(chan adapter.StreamEvent, len(s.events))
	for _, e := range s.events {
		ch <- e
	}
	close(ch)
	return ch
}

func reply(text string) []adapter.StreamEvent {
	return []adapter.StreamEvent{
		{Kind: adapter.EventTokenDelta, Token: text},
		{Kind: adapter.EventDone, Final: &adapter.Message{}},
	}
}

func editHistory() []adapter.Message {
	return []adapter.Message{
		{Role: adapter.RoleUser, Content: "fix it"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{{ID: "1", Name: "edit_file", ArgsJSON: `{"path":"a.go"}`}}},
		{Role: adapter.RoleTool, ToolCallID: "1", Content: "ok"},
	}
}

func TestSummarize_FirstCompactionUsesPromptSectionsAndFiles(t *testing.T) {
	s := &stubStreamer{events: reply("## Decisions made\nship it")}
	out, err := Summarize(context.Background(), s, Input{Middle: editHistory(), Window: 32_000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.got[0].Content != Prompt {
		t.Errorf("first compaction must use Prompt")
	}
	for _, h := range SectionHeaders {
		if !strings.Contains(out, h) {
			t.Errorf("missing section %q", h)
		}
	}
	if !strings.HasSuffix(out, "<files>\n(Write) a.go\n</files>") {
		t.Errorf("file list not appended: %q", out)
	}
	if !strings.Contains(s.got[1].Content, "a.go") {
		t.Errorf("tool args missing from input: %q", s.got[1].Content)
	}
}

func TestSummarize_UpdateMergesPreviousFiles(t *testing.T) {
	s := &stubStreamer{events: reply("x")}
	out, err := Summarize(context.Background(), s, Input{
		Middle: editHistory(), Window: 32_000, Prev: "PREV",
		PrevFiles: map[string]FileOp{"old.go": OpRead},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.got[0].Content != UpdatePrompt || !strings.Contains(s.got[1].Content, "<previous-summary>\nPREV\n</previous-summary>") {
		t.Errorf("update prompt / previous summary not used")
	}
	if !strings.Contains(out, "(Read) old.go") || !strings.Contains(out, "(Write) a.go") {
		t.Errorf("files not merged: %q", out)
	}
}

func TestSummarize_ErrorsAndFallbacks(t *testing.T) {
	if _, err := Summarize(context.Background(), &stubStreamer{events: reply("")}, Input{Middle: editHistory()}, nil); !errors.Is(err, ErrEmptySummary) {
		t.Errorf("empty reply: %v", err)
	}
	if _, err := Summarize(context.Background(), &stubStreamer{}, Input{}, nil); !errors.Is(err, ErrEmptyBody) {
		t.Errorf("empty input: %v", err)
	}
	boom := errors.New("boom")
	if _, err := Summarize(context.Background(), &stubStreamer{events: []adapter.StreamEvent{{Kind: adapter.EventErr, Err: boom}}}, Input{Middle: editHistory()}, nil); !errors.Is(err, boom) {
		t.Errorf("stream error: %v", err)
	}
	var seen int
	evs := append([]adapter.StreamEvent{{Kind: adapter.EventFallback, FallbackFrom: "a", FallbackTo: "b"}}, reply("ok")...)
	if _, err := Summarize(context.Background(), &stubStreamer{events: evs}, Input{Middle: editHistory()}, func(adapter.StreamEvent) { seen++ }); err != nil || seen != 1 {
		t.Errorf("fallback callback: err=%v seen=%d", err, seen)
	}
}

func TestSplitSessionSummary(t *testing.T) {
	body, files, ok := SplitSessionSummary("[Session summary — compressed at turn 3]\n\nPROSE\n\n<files>\n(RW) a.go\n</files>")
	if !ok || body != "PROSE" || files["a.go"] != OpRead|OpWrite {
		t.Fatalf("ok=%v body=%q files=%v", ok, body, files)
	}
	if _, _, ok := SplitSessionSummary("plain assistant text"); ok {
		t.Fatal("non-summary must not parse")
	}
}
