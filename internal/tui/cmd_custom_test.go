package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agent"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/session"
	"github.com/yottadynamics/yottacode/internal/usercmd"
)

// newCustomCmdModel builds a Model with the given custom commands
// pre-installed. Mirrors newTestModel but exposes the CustomCommands
// hook so the test doesn't have to write to disk and re-Load. The
// adapter remains nil — the tests below verify registry/dispatch
// shape, not full turn execution.
func newCustomCmdModel(t *testing.T, cmds []usercmd.Command) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	sess, err := session.New("test-model", "/cwd")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	cfg := agent.LoopConfig{Registry: agent.NewRegistry(), MaxIterations: 5}
	cwd := t.TempDir()
	perms := permissions.LoadEmpty(cwd)
	m := New(context.Background(), Config{
		Cfg:            cfg,
		Session:        sess,
		Permissions:    perms,
		ModelName:      "test-model",
		BaseURL:        "http://test/v1",
		Cwd:            cwd,
		CustomCommands: cmds,
	})
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

func TestCustom_RegistryLookup(t *testing.T) {
	m := newCustomCmdModel(t, []usercmd.Command{{
		Name:        "review",
		Description: "Review a PR",
		ArgHint:     "<pr>",
		Body:        "review PR $1",
		Scope:       usercmd.ScopeUser,
	}})
	c := m.findSlash("review")
	if c == nil {
		t.Fatal("findSlash(\"review\") returned nil for a registered custom command")
	}
	if c.Args != "<pr>" {
		t.Errorf("Args = %q, want %q", c.Args, "<pr>")
	}
	if c.Help != "Review a PR" {
		t.Errorf("Help = %q, want %q", c.Help, "Review a PR")
	}
	if !c.IsCustom {
		t.Error("IsCustom should be true for a loaded custom command")
	}
}

func TestCustom_BuiltinShadowsCustom(t *testing.T) {
	// Even if a custom command sneaks past the loader's reserved-name
	// gate (defense-in-depth check on the dispatcher), built-ins still
	// win. We can construct a Model that has a custom "help" entry to
	// verify findSlash returns the built-in.
	m := newCustomCmdModel(t, []usercmd.Command{{
		Name: "help",
		Body: "custom help body",
	}})
	c := m.findSlash("help")
	if c == nil {
		t.Fatal("findSlash(\"help\") returned nil")
	}
	if c.IsCustom {
		t.Errorf("built-in /help should win over a custom entry; got IsCustom=true (Source=%q)", c.Source)
	}
}

func TestCustom_HelpListsCustomSection(t *testing.T) {
	m := newCustomCmdModel(t, []usercmd.Command{{
		Name:        "deploy",
		Description: "Trigger a deploy",
		Body:        "deploy now",
		SourceFile:  "/tmp/commands/deploy.md",
	}})
	m, _ = typeAndEnter(t, m, "/help")
	content := m.helpPanel
	if !strings.Contains(content, "Custom commands") {
		t.Errorf("/help should include 'Custom commands:' section: %q", content)
	}
	if !strings.Contains(content, "/deploy") {
		t.Errorf("/help should list /deploy: %q", content)
	}
	if !strings.Contains(content, "Trigger a deploy") {
		t.Errorf("/help should show the description: %q", content)
	}
}

func TestCustom_PaletteIncludesCustom(t *testing.T) {
	m := newCustomCmdModel(t, []usercmd.Command{{
		Name:        "deploy",
		Description: "Trigger a deploy",
		Body:        "deploy now",
	}})
	// "/dep" should filter down to the custom /deploy.
	matches := m.filterPaletteAll("/dep")
	if len(matches) != 1 || matches[0].Name != "deploy" {
		t.Errorf("expected /dep to match custom /deploy; got %+v", matches)
	}
}

func TestCustom_DispatchSubstitutesArgsIntoSession(t *testing.T) {
	// Wire an adapter so startTurn actually appends the user message
	// (it bails early when m.cfg.Adapter is nil). A stub adapter that
	// never streams events keeps the test deterministic — the turn
	// will hang waiting on a never-arriving channel close, but we
	// only need to assert the substituted message landed in session
	// state, which startTurn does before it kicks off the turn.
	m := newCustomCmdModel(t, []usercmd.Command{{
		Name:    "review",
		ArgHint: "<pr>",
		Body:    "Review PR #$1 against @docs/style.md",
	}})
	m.cfg.Adapter = stubAdapterNoStream{}

	preMsgCount := len(m.sess.Messages)
	out, _ := typeAndEnter(t, m, "/review 123")

	if len(out.sess.Messages) <= preMsgCount {
		t.Fatalf("expected a new user message after /review; sess.Messages len went %d → %d",
			preMsgCount, len(out.sess.Messages))
	}
	last := out.sess.Messages[len(out.sess.Messages)-1]
	if !strings.Contains(last.Content, "Review PR #123") {
		t.Errorf("expected substituted '$1' → '123' in user message; got %q", last.Content)
	}
	if !strings.Contains(last.Content, "@docs/style.md") {
		t.Errorf("expected @docs/style.md to be preserved verbatim (filerefs runs inside startTurn); got %q", last.Content)
	}
}

// TestCustom_TranscriptShowsCompactInvocation locks the
// scrollback-compression behavior: the agent still gets the full
// substituted body (asserted by the test above), but the *visible*
// user block in scrollback shows only the slash invocation, not the
// directive prose. Stops the "why is my screen flooded with the
// prompt body every time I run /git:commit-message" regression.
func TestCustom_TranscriptShowsCompactInvocation(t *testing.T) {
	m := newCustomCmdModel(t, []usercmd.Command{{
		Name:    "review",
		ArgHint: "<pr>",
		Body:    "Review PR #$1 against the long detailed checklist that nobody wants to see echoed every invocation",
	}})
	m.cfg.Adapter = stubAdapterNoStream{}

	out, _ := typeAndEnter(t, m, "/review 123")
	transcript := out.transcript.String()

	if !strings.Contains(transcript, "/review 123") {
		t.Errorf("transcript should show the compact '/review 123' invocation; got:\n%s", transcript)
	}
	if strings.Contains(transcript, "long detailed checklist") {
		t.Errorf("transcript should NOT echo the full body — only the agent should see it. Got:\n%s", transcript)
	}
}

func TestCustom_DispatchBailsWhenTurnActive(t *testing.T) {
	m := newCustomCmdModel(t, []usercmd.Command{{
		Name: "deploy",
		Body: "ship it",
	}})
	m.turnActive = true
	preMsgCount := len(m.sess.Messages)
	out, _ := typeAndEnter(t, m, "/deploy")
	if len(out.sess.Messages) != preMsgCount {
		t.Errorf("turn-active guard should prevent a new user message")
	}
	if !strings.Contains(out.transcript.String(), "a turn is already running") {
		t.Errorf("expected 'a turn is already running' notice; got: %q", out.transcript.String())
	}
}

func TestCustom_EndToEndFromDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\ndescription: Review a PR\nargument-hint: <pr>\n---\nReview PR $1"
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cmds, errs := usercmd.Load("")
	if len(errs) > 0 {
		t.Fatalf("unexpected load errors: %v", errs)
	}
	m := newCustomCmdModel(t, cmds)
	c := m.findSlash("review")
	if c == nil || c.Help != "Review a PR" {
		t.Errorf("expected /review with description loaded from disk; got %+v", c)
	}
}

// stubAdapterNoStream satisfies adapter.Client just enough for
// startTurn to pass its nil-Adapter early bail-out. ChatStream returns
// an immediately-closed channel; tests using this stub inspect session
// state synchronously after the dispatch returns, before any agent
// event loop has a chance to react to the closed stream.
type stubAdapterNoStream struct{}

func (stubAdapterNoStream) ChatStream(ctx context.Context, _ []adapter.Message, _ []adapter.Tool) <-chan adapter.StreamEvent {
	ch := make(chan adapter.StreamEvent)
	close(ch)
	return ch
}

func (stubAdapterNoStream) Profile() adapter.ProviderProfile {
	return adapter.ProviderProfile{Provider: adapter.ProviderOpenAICompatible}
}
