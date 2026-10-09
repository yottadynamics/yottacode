package acp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/cli"
	"github.com/yottadynamics/yottacode/internal/config"
)

func TestNewSession_AdvertisesEffortConfigOption(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()

	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	opt := findSelect(resp.ConfigOptions, configIdEffort)
	if opt == nil {
		t.Fatalf("no effort option advertised: %+v", resp.ConfigOptions)
	}
	if len(resp.ConfigOptions) != 2 {
		t.Fatalf("ConfigOptions = %d entries, want 2 (mode + effort — no providers, no router pair)", len(resp.ConfigOptions))
	}
	if opt.CurrentValue != "default" {
		t.Errorf("Select.CurrentValue = %q, want \"default\"", opt.CurrentValue)
	}
	if opt.Options.Ungrouped == nil || len(*opt.Options.Ungrouped) != 4 {
		t.Errorf("expected 4 ungrouped effort options, got %+v", opt.Options)
	}
}

func TestSetSessionConfigOption_Effort_Success(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()

	resp, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdEffort,
			Value:     "high",
		},
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption: %v", err)
	}
	effort := findSelect(resp.ConfigOptions, configIdEffort)
	if effort == nil {
		t.Fatalf("unexpected response shape: %+v", resp.ConfigOptions)
	}
	if effort.CurrentValue != "high" {
		t.Errorf("CurrentValue = %q, want \"high\"", effort.CurrentValue)
	}

	sess, ok := h.srv.session(sessionID)
	if !ok {
		t.Fatal("session not registered")
	}
	if sess.rt.ChatOptions.ReasoningEffort != "high" {
		t.Errorf("rt.ChatOptions.ReasoningEffort = %q, want \"high\"", sess.rt.ChatOptions.ReasoningEffort)
	}
}

func TestSetSessionConfigOption_Effort_UnknownValueErrors(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()

	_, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdEffort,
			Value:     "extreme",
		},
	})
	if err == nil {
		t.Error("expected an error for an unrecognized effort value")
	}
}

func TestSetSessionConfigOption_Effort_WrongVariantErrors(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()

	_, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		Boolean: &coderacp.SetSessionConfigOptionBoolean{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdEffort,
			Value:     true,
		},
	})
	if err == nil {
		t.Error("expected an error sending a boolean value for the effort (select-typed) option")
	}
}

func TestSetSessionConfigOption_UnknownConfigIdErrors(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()

	_, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  "does-not-exist",
			Value:     "high",
		},
	})
	if err == nil {
		t.Error("expected an error for an unknown config option id")
	}
}

func TestSetSessionConfigOption_UnknownSessionErrors(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	_, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: "does-not-exist",
			ConfigId:  configIdEffort,
			Value:     "high",
		},
	})
	if err == nil {
		t.Error("expected an error for an unknown session id")
	}
}

// TestSetSessionConfigOption_Advisor_NoPairErrors and
// TestSetSessionConfigOption_Advisor_TogglesWithPair construct
// sess.rt.RouterAdapters directly (rt.Adapter, already a real client from
// the test harness's stub-backed Build call, satisfies adapter.Client) —
// agentruntime's own tests already cover the harder router-interaction
// correctness (effort rebuild keeping a routed session on the advisor
// model, etc.); these only need to prove the RPC dispatches correctly.

func TestSetSessionConfigOption_Advisor_NoPairErrors(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()

	_, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		Boolean: &coderacp.SetSessionConfigOptionBoolean{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdAdvisor,
			Value:     true,
		},
	})
	if err == nil {
		t.Error("expected an error enabling advisor routing with no configured pair")
	}
}

func TestSetSessionConfigOption_Advisor_TogglesWithPair(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	sess, ok := h.srv.session(sessionID)
	if !ok {
		t.Fatal("session not registered")
	}
	sess.rt.RouterAdapters = &cli.RouterAdapters{
		Advisor:          sess.rt.Adapter,
		Implementer:      sess.rt.Adapter,
		AdvisorModel:     "advisor-model",
		ImplementerModel: "implementer-model",
	}

	ctx, cancel := withTimeout(t)
	defer cancel()

	resp, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		Boolean: &coderacp.SetSessionConfigOptionBoolean{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdAdvisor,
			Value:     true,
		},
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption(advisor=true): %v", err)
	}
	var found bool
	for _, opt := range resp.ConfigOptions {
		if opt.Boolean != nil && opt.Boolean.Id == configIdAdvisor {
			found = true
			if !opt.Boolean.CurrentValue {
				t.Error("advisor CurrentValue should be true after enabling")
			}
		}
	}
	if !found {
		t.Fatal("response ConfigOptions missing the advisor boolean entry")
	}
	if !sess.rt.AgentTool.RouteAuto {
		t.Error("rt.AgentTool.RouteAuto should be true after enabling advisor routing")
	}
}

func TestSetSessionConfigOption_RejectedWhileTurnInFlight(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	sess, _ := h.srv.session(sessionID)
	blocker := &blockingStreamer{started: make(chan struct{})}
	sess.rt.Cfg.Adapter = blocker

	ctx, cancel := withTimeout(t)
	defer cancel()

	promptErr := make(chan error, 1)
	go func() {
		_, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{
			SessionId: coderacp.SessionId(sessionID),
			Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("go")},
		})
		promptErr <- err
	}()

	select {
	case <-blocker.started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the turn to start")
	}

	if _, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: coderacp.SessionId(sessionID),
			ConfigId:  configIdEffort,
			Value:     "high",
		},
	}); err == nil {
		t.Error("expected SetSessionConfigOption to be rejected while a turn is in flight")
	}

	if err := h.clientConn.Cancel(ctx, coderacp.CancelNotification{SessionId: coderacp.SessionId(sessionID)}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case err := <-promptErr:
		if err != nil {
			t.Fatalf("Prompt returned an error after cancel: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the Prompt to return after Cancel")
	}
}

// writeModelTestConfig installs a two-provider config.toml under an isolated
// HOME (newTestHarness sets it; call this after) so sessions built by the harness see selectable models.
func writeModelTestConfig(t *testing.T) {
	t.Helper()
	cfg := config.Default()
	cfg.Providers = []config.Provider{
		{Name: "alpha", Kind: "openai", BaseURL: "http://127.0.0.1:0/v1", DefaultModel: "alpha-1"},
		{Name: "beta", Kind: "openai-compatible", BaseURL: "http://127.0.0.1:1/v1", DefaultModel: "beta-1"},
	}
	cfg.Active.Provider = "alpha"
	cfg.Active.Model = "alpha-1"
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func findSelect(opts []coderacp.SessionConfigOption, id coderacp.SessionConfigId) *coderacp.SessionConfigOptionSelect {
	for _, o := range opts {
		if o.Select != nil && o.Select.Id == id {
			return o.Select
		}
	}
	return nil
}

func TestNewSession_AdvertisesModelConfigOption(t *testing.T) {
	h := newTestHarness(t)
	writeModelTestConfig(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sel := findSelect(resp.ConfigOptions, configIdModel)
	if sel == nil {
		t.Fatalf("no model option advertised: %+v", resp.ConfigOptions)
	}
	if sel.Category == nil || *sel.Category != coderacp.SessionConfigOptionCategoryModel {
		t.Errorf("model option category = %v, want \"model\" (buzz-acp discovers models by category)", sel.Category)
	}
	if sel.Options.Ungrouped == nil {
		t.Fatalf("no options: %+v", sel.Options)
	}
	have := map[string]bool{}
	for _, o := range *sel.Options.Ungrouped {
		have[string(o.Value)] = true
	}
	for _, m := range []string{"alpha-1", "beta-1"} {
		if !have[m] {
			t.Errorf("model %q not offered: %v", m, have)
		}
	}
}

func TestSetSessionConfigOption_Model(t *testing.T) {
	h := newTestHarness(t)
	writeModelTestConfig(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	newResp, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	set := func(v string) (coderacp.SetSessionConfigOptionResponse, error) {
		return h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
			ValueId: &coderacp.SetSessionConfigOptionValueId{SessionId: newResp.SessionId, ConfigId: configIdModel, Value: coderacp.SessionConfigValueId(v)},
		})
	}

	resp, err := set("beta-1")
	if err != nil {
		t.Fatalf("set model: %v", err)
	}
	if sel := findSelect(resp.ConfigOptions, configIdModel); sel == nil || sel.CurrentValue != "beta-1" {
		t.Errorf("response model current value = %+v, want beta-1", sel)
	}
	sess, _ := h.srv.session(string(newResp.SessionId))
	if sess.rt.ChatOptions.Provider != "beta" || sess.rt.Session.Model != "beta-1" {
		t.Errorf("runtime not switched: provider=%q session model=%q", sess.rt.ChatOptions.Provider, sess.rt.Session.Model)
	}

	if _, err := set("no-such-model"); err == nil {
		t.Error("unknown model accepted")
	}
}

// TestLoadSession_RestoresSessionModel: a Buzz channel resumes via
// session/load; it must come back on the model it last ran, not the
// process default.
func TestLoadSession_RestoresSessionModel(t *testing.T) {
	h := newTestHarness(t)
	writeModelTestConfig(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	newResp, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{SessionId: newResp.SessionId, ConfigId: configIdModel, Value: "beta-1"},
	}); err != nil {
		t.Fatalf("set model: %v", err)
	}
	sess, _ := h.srv.session(string(newResp.SessionId))
	sess.rt.Cfg.Adapter = &scriptedStreamer{turns: [][]adapter.StreamEvent{{sseDone("hi")}}}
	if _, err := h.clientConn.Prompt(ctx, coderacp.PromptRequest{SessionId: newResp.SessionId, Prompt: []coderacp.ContentBlock{coderacp.TextBlock("hi")}}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if _, err := h.clientConn.CloseSession(ctx, coderacp.CloseSessionRequest{SessionId: newResp.SessionId}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	if _, err := h.clientConn.LoadSession(ctx, coderacp.LoadSessionRequest{SessionId: newResp.SessionId, Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}}); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	loaded, _ := h.srv.session(string(newResp.SessionId))
	if loaded.rt.ChatOptions.Model != "beta-1" || loaded.rt.ChatOptions.Provider != "beta" {
		t.Errorf("loaded session on %q/%q, want beta/beta-1", loaded.rt.ChatOptions.Provider, loaded.rt.ChatOptions.Model)
	}
}

func TestSetSessionConfigOption_Mode(t *testing.T) {
	h, sessionID := newPromptHarness(t, nil)
	ctx, cancel := withTimeout(t)
	defer cancel()
	sess, _ := h.srv.session(sessionID)

	adv, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sel := findSelect(adv.ConfigOptions, configIdMode); sel == nil || sel.Category == nil || *sel.Category != coderacp.SessionConfigOptionCategoryMode || sel.CurrentValue != "default" {
		t.Fatalf("mode option missing/wrong: %+v", sel)
	}

	set := func(v string) (coderacp.SetSessionConfigOptionResponse, error) {
		return h.clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
			ValueId: &coderacp.SetSessionConfigOptionValueId{SessionId: coderacp.SessionId(sessionID), ConfigId: configIdMode, Value: coderacp.SessionConfigValueId(v)},
		})
	}
	// Buzz's kebab-case spelling and Claude-style camelCase both work.
	for _, tc := range []struct {
		value string
		want  coderacp.SessionModeId
		cur   string
	}{
		{"bypass-permissions", modeYolo, "bypassPermissions"},
		{"bypassPermissions", modeYolo, "bypassPermissions"},
		{"plan", modeArchitect, "plan"},
		{"auto", modeCode, "auto"},
		{"accept-edits", modeCode, "auto"},
		{"default", modeAsk, "default"},
	} {
		resp, err := set(tc.value)
		if err != nil {
			t.Fatalf("set %q: %v", tc.value, err)
		}
		if got := currentModeID(sess.rt); got != tc.want {
			t.Errorf("after %q mode = %q, want %q", tc.value, got, tc.want)
		}
		if sel := findSelect(resp.ConfigOptions, configIdMode); sel == nil || string(sel.CurrentValue) != tc.cur {
			t.Errorf("after %q response current = %+v, want %q", tc.value, sel, tc.cur)
		}
	}
	if _, err := set("dont-ask"); err == nil {
		t.Error("dont-ask has no yottacode equivalent and must be rejected")
	}
}

func TestNewSession_MetaSessionTitleNamesSession(t *testing.T) {
	h := newTestHarness(t)
	ctx, cancel := withTimeout(t)
	defer cancel()
	if _, err := h.clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resp, err := h.clientConn.NewSession(ctx, coderacp.NewSessionRequest{
		Cwd: t.TempDir(), McpServers: []coderacp.McpServer{}, Meta: map[string]any{"sessionTitle": "  jpmc-agent "},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess, _ := h.srv.session(string(resp.SessionId))
	want := "jpmc-agent-" + sess.rt.Session.Created.UTC().Format("20060102-150405")
	if sess.rt.Session.Name != want {
		t.Errorf("Session.Name = %q, want %q", sess.rt.Session.Name, want)
	}
}
