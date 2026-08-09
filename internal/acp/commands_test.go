package acp

import (
	"context"
	"sync"
	"testing"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

func TestAvailableCommands_ReturnsAllMacroNamesAndArgHints(t *testing.T) {
	cmds := availableCommands()
	macros := promptmacros.All()
	if len(cmds) != len(macros) {
		t.Fatalf("availableCommands() returned %d entries, want %d", len(cmds), len(macros))
	}
	for i, m := range macros {
		if cmds[i].Name != m.Name {
			t.Errorf("availableCommands()[%d].Name = %q, want %q", i, cmds[i].Name, m.Name)
		}
		if cmds[i].Description != m.Description {
			t.Errorf("availableCommands()[%d].Description = %q, want %q", i, cmds[i].Description, m.Description)
		}
		if m.ArgHint == "" {
			if cmds[i].Input != nil {
				t.Errorf("%s: expected nil Input (no ArgHint), got %+v", m.Name, cmds[i].Input)
			}
			continue
		}
		if cmds[i].Input == nil || cmds[i].Input.Unstructured == nil || cmds[i].Input.Unstructured.Hint != m.ArgHint {
			t.Errorf("%s: expected Input.Unstructured.Hint = %q, got %+v", m.Name, m.ArgHint, cmds[i].Input)
		}
	}
}

func TestMatchMacroCommand_RecognizesRegisteredNameAndSplitsArgs(t *testing.T) {
	m, args, ok := matchMacroCommand("/git-create-pr develop")
	if !ok {
		t.Fatal("expected /git-create-pr to match")
	}
	if m.Name != "git-create-pr" {
		t.Errorf("matched macro = %q, want git-create-pr", m.Name)
	}
	if len(args) != 1 || args[0] != "develop" {
		t.Errorf("args = %v, want [develop]", args)
	}
}

func TestMatchMacroCommand_FallsThroughOnUnrecognizedOrPlainText(t *testing.T) {
	cases := []string{
		"/does-not-exist",
		"/",
		"not a slash command at all",
		"please read /etc/hosts for me",
	}
	for _, text := range cases {
		if _, _, ok := matchMacroCommand(text); ok {
			t.Errorf("matchMacroCommand(%q) unexpectedly matched", text)
		}
	}
}

func TestNewSession_EmitsAvailableCommandsUpdateWithAllNineNames(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var got []coderacp.AvailableCommand
	for _, u := range h.client.Updates() {
		if u.Update.AvailableCommandsUpdate != nil {
			got = u.Update.AvailableCommandsUpdate.AvailableCommands
		}
	}
	want := promptmacros.All()
	if len(got) != len(want) {
		t.Fatalf("got %d available commands, want %d", len(got), len(want))
	}
	for i, m := range want {
		if got[i].Name != m.Name {
			t.Errorf("available_commands_update[%d].Name = %q, want %q", i, got[i].Name, m.Name)
		}
	}
}

// capturingStreamer is a variant of scriptedStreamer (see prompt_test.go)
// that also records the message slice each ChatStream call received, so
// a test can assert on what text actually reached the adapter as the
// turn's user message — the thing slash-macro substitution changes.
type capturingStreamer struct {
	mu       sync.Mutex
	calls    int
	lastMsgs []adapter.Message
	turns    [][]adapter.StreamEvent
	next     int
}

func (s *capturingStreamer) ChatStream(_ context.Context, msgs []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	s.mu.Lock()
	s.calls++
	s.lastMsgs = append([]adapter.Message(nil), msgs...)
	out := make(chan adapter.StreamEvent, 16)
	if s.next >= len(s.turns) {
		s.mu.Unlock()
		close(out)
		return out
	}
	turn := s.turns[s.next]
	s.next++
	s.mu.Unlock()
	go func() {
		defer close(out)
		for _, ev := range turn {
			out <- ev
		}
	}()
	return out
}

func TestPrompt_SlashGitCommit_SubstitutesBuiltPromptForLiteralText(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	sess, _ := h.srv.session(sessionID)
	streamer := &capturingStreamer{turns: [][]adapter.StreamEvent{{sseDone("done")}}}
	sess.rt.Cfg.Adapter = streamer

	ctx, cancel := withTimeout(t)
	defer cancel()
	resp, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("/git-commit")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if resp.StopReason != coderacp.StopReasonEndTurn {
		t.Errorf("StopReason = %q, want %q", resp.StopReason, coderacp.StopReasonEndTurn)
	}

	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if streamer.calls != 1 {
		t.Fatalf("adapter called %d times, want 1", streamer.calls)
	}
	if len(streamer.lastMsgs) == 0 {
		t.Fatal("adapter received no messages")
	}
	userMsg := streamer.lastMsgs[len(streamer.lastMsgs)-1]
	if userMsg.Content == "/git-commit" {
		t.Error("the literal \"/git-commit\" text reached the adapter — slash dispatch did not substitute the built prompt")
	}
	if userMsg.Content != promptmacros.GitCommitDirective() {
		t.Errorf("user message content = %q, want the git-commit macro's built directive", userMsg.Content)
	}
}

func TestPrompt_UnrecognizedSlashText_FallsThroughUnchanged(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	sess, _ := h.srv.session(sessionID)
	streamer := &capturingStreamer{turns: [][]adapter.StreamEvent{{sseDone("done")}}}
	sess.rt.Cfg.Adapter = streamer

	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("/foo bar baz")},
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	userMsg := streamer.lastMsgs[len(streamer.lastMsgs)-1]
	if userMsg.Content != "/foo bar baz" {
		t.Errorf("unrecognized slash text was rewritten: got %q, want unchanged %q", userMsg.Content, "/foo bar baz")
	}
}

func TestPrompt_GitImplementIssueNoArgs_InvalidParamsWithoutStartingTurn(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	sess, _ := h.srv.session(sessionID)
	streamer := &capturingStreamer{turns: [][]adapter.StreamEvent{{sseDone("done")}}}
	sess.rt.Cfg.Adapter = streamer

	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: coderacp.SessionId(sessionID),
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("/git-implement-issue")},
	}); err == nil {
		t.Fatal("expected an error for /git-implement-issue with no issue number")
	}

	streamer.mu.Lock()
	defer streamer.mu.Unlock()
	if streamer.calls != 0 {
		t.Errorf("adapter was called %d times; a turn must not start on invalid macro args", streamer.calls)
	}
}
