package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/permissions"
	"github.com/yottadynamics/yottacode/internal/recall"
	"github.com/yottadynamics/yottacode/internal/session"
)

// TestRenderHelpPanel_LongSkillDescriptionWrapsWithinWidth is a
// regression guard: a skill's Help text can be arbitrarily long
// (author-written prose), and renderMenuItem's Desc column used to
// render it unbounded — overflowing the popup's right border on a
// narrow terminal instead of truncating in place like every other
// picker row.
func TestRenderHelpPanel_LongSkillDescriptionWrapsWithinWidth(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 60, Height: 24})
	m.skillSlash = []slashCommand{
		{Name: "my-skill", Help: "this is a deliberately long skill description that would overflow a narrow popup's right border if it were never truncated to fit the row"},
	}

	got := renderHelpPanel(m)
	width := m.popupWidth()
	for _, line := range strings.Split(got, "\n") {
		if w := runeLen(stripANSI(line)); w > width {
			t.Errorf("line exceeds popup width %d (got %d): %q", width, w, line)
		}
	}
}

// TestRenderHelpPanel_DividerMatchesBoxWidth mirrors the same
// divider/box-width consistency guard as skills_menu_test.go's
// TestSkillsMenu_DividerMatchesBoxWidth — renderMenuHeader now always
// receives the same width the row content is bounded to.
func TestRenderHelpPanel_DividerMatchesBoxWidth(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 60, Height: 24})
	m.skillSlash = []slashCommand{
		{Name: "my-skill", Help: "this is a deliberately long skill description that would overflow a narrow popup's right border if it were never truncated to fit the row"},
	}

	rendered := renderHelpPanel(m)
	box := popupBox(rendered)
	boxLines := strings.Split(box, "\n")
	if len(boxLines) < 2 {
		t.Fatalf("popup box has too few lines: %d", len(boxLines))
	}
	boxWidth := runeLen(stripANSI(boxLines[0]))

	for _, line := range strings.Split(rendered, "\n") {
		if !strings.Contains(stripANSI(line), "─") {
			continue
		}
		if got := runeLen(stripANSI(line)) + 4; got != boxWidth {
			t.Errorf("divider width (%d, +border/padding) does not match the box's own width (%d): %q", got, boxWidth, line)
		}
	}
}

// permissionsLoadHelper seeds <cwd>/.yottacode/permissions.json with
// the given allow/ask/deny lists and returns a fresh Permissions
// pointing at it. Used by /permissions slash-command tests so they
// don't have to hand-build the on-disk shape.
func permissionsLoadHelper(t *testing.T, cwd string, allow, ask, deny []string) (*permissions.Permissions, error) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(cwd, ".yottacode"), 0o755); err != nil {
		return nil, err
	}
	shape := map[string]map[string][]string{
		"permissions": {"allow": allow, "ask": ask, "deny": deny},
	}
	b, _ := json.MarshalIndent(shape, "", "  ")
	if err := os.WriteFile(filepath.Join(cwd, ".yottacode", "permissions.json"), b, 0o644); err != nil {
		return nil, err
	}
	return permissions.Load(cwd)
}

// typeAndEnter types each rune of input then sends Enter, returning the
// resulting Model and Cmd. Used by command tests that exercise the user-
// facing path (text → Update → dispatch).
func typeAndEnter(t *testing.T, m Model, input string) (Model, tea.Cmd) {
	t.Helper()
	for _, r := range input {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	return applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// allSlash must not register the same name twice — /help and the slash
// palette both iterate the registry, so a duplicate renders the command
// twice. Regression guard for the duplicate /plan and /subagents entries.
func TestAllSlash_NoDuplicateNames(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range allSlash {
		if seen[c.Name] {
			t.Errorf("duplicate slash command registered: /%s", c.Name)
		}
		seen[c.Name] = true
	}
}

func TestSlash_HelpListsAllCommands(t *testing.T) {
	m, _ := typeAndEnter(t, newTestModel(t), "/help")
	content := m.helpPanel
	if !m.helpOpen {
		t.Fatal("/help should open the inline help overlay")
	}
	for _, c := range allSlash {
		if !strings.Contains(content, "/"+c.Name) {
			t.Errorf("/help output missing /%s", c.Name)
		}
	}
	if !strings.Contains(content, "──") || !strings.Contains(content, "Common") || !strings.Contains(content, "Workflow") || !strings.Contains(content, "Git") {
		t.Errorf("/help should use grouped submenu chrome: %q", content)
	}
}

// /help renders as a transient overlay so the dense command catalog stays out
// of transcript history while remaining visible above the cmdline.
func TestSlash_HelpOpensOverlay(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
	)
	m.transcript.Reset()
	m, _ = typeAndEnter(t, m, "/help")
	content := m.helpPanel
	if !m.helpOpen {
		t.Fatal("/help should open the help overlay")
	}
	if strings.Contains(m.transcript.String(), "Workflow") {
		t.Errorf("/help should not dump the command catalog into transcript: %q", m.transcript.String())
	}
	if !strings.Contains(content, "Help") || !strings.Contains(content, "/help") {
		t.Errorf("/help should still list commands: %q", content)
	}
}

func TestSlash_QuitProducesQuitMsg(t *testing.T) {
	_, cmd := typeAndEnter(t, newTestModel(t), "/quit")
	if cmd == nil {
		t.Fatalf("/quit should produce a Cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("/quit should QuitMsg")
	}
}

func TestSlash_PermissionsOpensPickerWithBothRows(t *testing.T) {
	m := newTestModel(t)
	// Tall enough that the centered popup and the top-anchored launch
	// hero (/permissions no longer exits it — see enteredConversation's
	// field doc) don't overlap; the default 24-row test height puts
	// both in the same few rows.
	m.height = 60
	// Stand up a permissions store so SharedPath/LocalPath resolve.
	p, err := permissionsLoadHelper(t, m.cwd, []string{"Bash(go *)"}, nil, []string{"Bash(rm *)"})
	if err != nil {
		t.Fatalf("permissionsLoadHelper: %v", err)
	}
	m.perms = p
	m, _ = typeAndEnter(t, m, "/permissions")
	if !m.permissionsOpen {
		t.Fatalf("/permissions should open the inline picker (permissionsOpen=true)")
	}
	if m.permissionsCursor != 0 {
		t.Errorf("fresh picker should start with cursor on the shared row, got %d", m.permissionsCursor)
	}
	// Picker chrome lives in View() — the cmd no longer dumps to
	// scrollback, so assert against the rendered view. Match the
	// /model + /provider visual language: title, both row labels,
	// path strings, picker-style hotkey footer.
	v := m.View().Content
	for _, want := range []string{
		"Permissions",
		"shared",
		"local",
		"permissions.json",
		"permissions.local.json",
		"↵ open in vim",
		"esc back",
		"↑↓ navigate",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("/permissions picker missing %q; got %q", want, v)
		}
	}
	// Popups float over a persistent background (composePopup, popup.go)
	// rather than replacing the footer, so the cmdline border and status
	// bar text stay visible in the same frame as the picker content —
	// this pins that "float over, don't replace" invariant. Their
	// relative text position no longer carries meaning once the popup is
	// Z-composited on top (it can land at any row depending on centering
	// math), so this only checks presence, not order.
	plainView := stripANSI(v)
	if !strings.Contains(plainView, "Permissions") {
		t.Fatalf("view missing popup content:\n%s", plainView)
	}
	if !strings.Contains(plainView, "┌") {
		t.Fatalf("view missing cmdline chrome (popup should float over it, not replace it):\n%s", plainView)
	}
	if !strings.Contains(plainView, "test-model") || !strings.Contains(plainView, "ctx ") {
		t.Fatalf("view missing status bar (popup should float over it, not replace it):\n%s", plainView)
	}
	// Picker must not regress into a state-dump (no rule listing).
	for _, banned := range []string{"Bash(go *)", "Bash(rm *)"} {
		if strings.Contains(v, banned) {
			t.Errorf("/permissions should no longer list rules; found %q in %q", banned, v)
		}
	}
	// Old "press any key" hint shouldn't survive — Esc is the
	// dismiss now (matching /model and /provider).
	if strings.Contains(v, "press any key") {
		t.Errorf("/permissions should use picker-style esc dismiss, not 'press any key': %q", v)
	}
	// Transcript stays clean of the picker chrome — picker is live
	// footer UI, not scrollback. The startup card may embed the test's
	// temp-dir name (which contains "Permissions"), so assert against the
	// picker's hotkey footer instead, which is unique to the live overlay.
	if got := m.transcript.String(); strings.Contains(got, "↵ open in vim") {
		t.Errorf("/permissions should not write the picker to scrollback; got %q", got)
	}
}

func TestSlash_PermissionsPickerNavigatesAndEscDismisses(t *testing.T) {
	m := newTestModel(t)
	p, err := permissionsLoadHelper(t, m.cwd, nil, nil, nil)
	if err != nil {
		t.Fatalf("permissionsLoadHelper: %v", err)
	}
	m.perms = p
	m, _ = typeAndEnter(t, m, "/permissions")
	if !m.permissionsOpen {
		t.Fatalf("/permissions should open the inline picker")
	}
	// Down moves to the local row.
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.permissionsCursor != 1 {
		t.Errorf("Down should move cursor to row 1 (local); got %d", m.permissionsCursor)
	}
	// Down at the bottom row clamps — no third row.
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.permissionsCursor != 1 {
		t.Errorf("Down at bottom should clamp at 1; got %d", m.permissionsCursor)
	}
	// Up returns to the shared row.
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyUp})
	if m.permissionsCursor != 0 {
		t.Errorf("Up should move cursor back to row 0 (shared); got %d", m.permissionsCursor)
	}
	// Random keystrokes don't dismiss anymore — only Esc does.
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "x"})
	if !m.permissionsOpen {
		t.Errorf("non-picker keys should not dismiss the permissions picker")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.permissionsOpen {
		t.Errorf("Esc should dismiss the permissions picker")
	}
	if m.permissionsCursor != 0 {
		t.Errorf("Esc should reset cursor to 0; got %d", m.permissionsCursor)
	}
	// Esc from a sub-picker pops back to the slash palette so the user
	// can pick a different command without retyping `/`.
	if !m.paletteOpen {
		t.Errorf("Esc should reopen the slash palette; paletteOpen=false")
	}
	if got := m.textInput.Value(); got != "/" {
		t.Errorf("Esc should refill the prompt with `/`; got %q", got)
	}
}

func TestSlash_SystemRendersExistingPrompt(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages, adapter.Message{
		Role:    adapter.RoleSystem,
		Content: "you are a unit test fixture",
	})
	m, _ = typeAndEnter(t, m, "/system")
	v := m.transcript.String()
	if !strings.Contains(v, "unit test fixture") {
		t.Errorf("/system did not render the prompt: %q", v)
	}
}

func TestSlash_ModelSwitches(t *testing.T) {
	m := newTestModel(t)
	m.provider = "openai"
	m.baseURL = "https://api.openai.com/v1"
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleSystem, Content: "old sys"}}
	if m.modelName != "test-model" {
		t.Fatalf("fixture model name = %q", m.modelName)
	}
	m, _ = typeAndEnter(t, m, "/model qwen3.5:latest")
	if m.modelName != "qwen3.5:latest" {
		t.Errorf("/model should set modelName; got %q", m.modelName)
	}
	if m.sess.Model != "qwen3.5:latest" {
		t.Errorf("/model should also update session.Model; got %q", m.sess.Model)
	}
	if !strings.Contains(m.sess.Messages[0].Content, "provider-native web_search") {
		t.Errorf("/model should refresh system prompt for provider tools; got %q", m.sess.Messages[0].Content)
	}
}

func TestComposeSystemPrompt_XAIPrefersXSearch(t *testing.T) {
	got := composeSystemPrompt("base", adapter.ProviderProfile{
		Provider: adapter.ProviderXAI,
		EnabledBuiltinTools: []adapter.BuiltinToolKind{
			adapter.BuiltinToolWebSearch,
			adapter.BuiltinToolXSearch,
		},
	})
	for _, want := range []string{"use x_search, not web_search", "X/Twitter posts", "Use web_search only for general web pages"} {
		if !strings.Contains(got, want) {
			t.Fatalf("xAI prompt guidance missing %q:\n%s", want, got)
		}
	}
}

// Bare /model now opens the picker overlay (Phase 1A default UX).
// The flat-text list is reachable via /model list — covered in
// TestSlash_ModelListAllGroupsByProvider — so we just confirm the
// modal opens here and the transcript stays untouched (the picker
// renders in View() rather than appending lines).
func TestSlash_ModelWithoutArgsOpensPicker(t *testing.T) {
	m, _ := typeAndEnter(t, newTestModel(t), "/model")
	if !m.modelPickerOpen {
		t.Errorf("/model with no args should open the picker; modelPickerOpen = false")
	}
	if m.modelPicker == nil {
		t.Fatalf("/model with no args should attach picker state")
	}
}

// /model list keeps the flat-text dump for users who prefer it.
func TestSlash_ModelListShowsFlatText(t *testing.T) {
	m, _ := typeAndEnter(t, newTestModel(t), "/model list")
	got := m.transcript.String()
	if !strings.Contains(got, "no providers configured") && !strings.Contains(got, "models for") {
		t.Errorf("/model list should print catalog or no-providers hint; got %q", got)
	}
}

// /model list for a free-form provider (NVIDIA NIM, Ollama, custom)
// must surface the configured default_model + API-key status, not
// just say "no models configured" and leave the user wondering if
// their setup is wired up.
func TestSlash_ModelListSurfacesConfiguredDefaultForFreeForm(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[active]
provider      = "nvidia-nim"
default_model = "nvidia/nemotron-3-super-120b-a12b"

[[providers]]
name = "nvidia-nim"
kind = "openai-compatible"
base_url = "https://integrate.api.nvidia.com/v1"
api_key_env = "NVIDIA_API_KEY"
default_model = "nvidia/nemotron-3-super-120b-a12b"
`)
	t.Setenv("NVIDIA_API_KEY", "sk-set")
	m, _ = typeAndEnter(t, m, "/model list")
	got := m.transcript.String()
	for _, want := range []string{
		"nvidia-nim",
		"nvidia/nemotron-3-super-120b-a12b",
		"configured default",
		"NVIDIA_API_KEY set",
		"open /model for the live catalog",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("/model list missing %q in free-form output:\n%s", want, got)
		}
	}
}

// When the API key is missing for a configured provider, the list
// surfaces the missing-key hint so users see exactly what's wrong.
func TestSlash_ModelListFlagsMissingAPIKey(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[active]
provider      = "anthropic"
default_model = "claude-sonnet-4-6"

[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY_TEST_MISSING"
default_model = "claude-sonnet-4-6"
  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"
`)
	t.Setenv("ANTHROPIC_API_KEY_TEST_MISSING", "")
	m, _ = typeAndEnter(t, m, "/model list")
	got := m.transcript.String()
	if !strings.Contains(got, "missing") {
		t.Errorf("expected 'missing' API-key hint when env var is empty; got:\n%s", got)
	}
}

// seedConfigTOML writes config.toml under HOME (which newTestModel
// already isolates to a tempdir) so /provider and /model commands
// have a populated catalog to render.
func seedConfigTOML(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

// `/provider list` now opens the picker into the Use sub-mode (the
// "Switch provider" screen renders the list inline below the
// cmdline + status bar AND lets the user switch in one step). The
// prior scrollback dump retired alongside the standalone "List"
// menu item.
func TestSlash_ProviderListShowsConfiguredProfiles(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"
  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"

[[providers]]
name = "openai"
kind = "openai"
base_url = "https://api.openai.com/v1"
default_model = "gpt-4o"
  [[providers.models]]
  name = "gpt-4o"
  tier = "balanced"
`)
	m, _ = typeAndEnter(t, m, "/provider list")
	if !m.providerPickerOpen || m.providerPicker == nil {
		t.Fatalf("/provider list should open the picker")
	}
	if m.providerPicker.mode != providerUsePickerMode {
		t.Errorf("expected use-picker mode; got %v", m.providerPicker.mode)
	}
	got := stripANSI(m.View().Content)
	for _, want := range []string{"anthropic", "openai", "Switch provider"} {
		if !strings.Contains(got, want) {
			t.Errorf("/provider list view missing %q; got:\n%s", want, got)
		}
	}
}

func TestSlash_ProviderUseSwitchesActiveProfile(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name = "openai-test"
kind = "openai"
base_url = "https://openai.example/v1"
default_model = "gpt-4o"
  [[providers.models]]
  name = "gpt-4o"
  tier = "balanced"
`)
	m, _ = typeAndEnter(t, m, "/provider use openai-test")
	if m.baseURL != "https://openai.example/v1" {
		t.Errorf("baseURL after /provider use = %q", m.baseURL)
	}
	if m.modelName != "gpt-4o" {
		t.Errorf("model after /provider use = %q, want gpt-4o", m.modelName)
	}
	if !strings.Contains(m.transcript.String(), "switched to openai-test") {
		t.Errorf("transcript missing switch confirmation:\n%s", m.transcript.String())
	}
}

// A reasoning effort set on a model that supports it must be re-flagged
// as a no-op the moment /provider use lands the session on a model that
// doesn't — reasoningEffort is a session-global field that /provider
// never clears or re-validates, so without an active re-check the
// setting would silently stop applying with no indication to the user.
func TestSlash_ProviderUseWarnsWhenEffortBecomesNoop(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name = "anthropic-test"
kind = "anthropic"
base_url = "https://anthropic.example/v1"
default_model = "claude-sonnet-4-6"
  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"

[[providers]]
name = "openai-test"
kind = "openai"
base_url = "https://openai.example/v1"
default_model = "gpt-4o"
  [[providers.models]]
  name = "gpt-4o"
  tier = "balanced"
`)
	m, _ = typeAndEnter(t, m, "/provider use anthropic-test")
	m, _ = typeAndEnter(t, m, "/effort high")
	if strings.Contains(m.transcript.String(), "no-op on this model") {
		t.Fatalf("effort should apply cleanly on claude-sonnet-4-6; transcript:\n%s", m.transcript.String())
	}
	pre := m.transcript.String()
	m, _ = typeAndEnter(t, m, "/provider use openai-test")
	post := m.transcript.String()[len(pre):]
	if !strings.Contains(post, "no-op on this model") {
		t.Errorf("switching to gpt-4o (non-reasoning) should re-surface the no-op warning; new transcript:\n%s", post)
	}
}

// /provider use changes the active model exactly like /model and the
// picker do (providerUse adopts the target provider's default_model),
// so a mid-session switch burns the provider prompt cache the same
// way — the earlier cache-reset warning was wired into the /model
// shortcut and the picker but missed this third call site.
func TestSlash_ProviderUseWarnsOnMidSessionCacheReset(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name = "anthropic-test"
kind = "anthropic"
base_url = "https://anthropic.example/v1"
default_model = "claude-sonnet-4-6"
  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"

[[providers]]
name = "openai-test"
kind = "openai"
base_url = "https://openai.example/v1"
default_model = "gpt-4o"
  [[providers.models]]
  name = "gpt-4o"
  tier = "balanced"
`)
	m, _ = typeAndEnter(t, m, "/provider use anthropic-test")
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "hello"},
	)
	pre := m.transcript.String()
	m, _ = typeAndEnter(t, m, "/provider use openai-test")
	post := m.transcript.String()[len(pre):]
	if !strings.Contains(post, "prompt cache resets") {
		t.Errorf("/provider use mid-session should warn about the cache reset; new transcript:\n%s", post)
	}
}

func TestSlash_ModelListAllGroupsByProvider(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
default_model = "claude-sonnet-4-6"

[[providers]]
name = "openai"
kind = "openai"
base_url = "https://api.openai.com/v1"
default_model = "gpt-4o"
`)
	m, _ = typeAndEnter(t, m, "/model list all")
	got := m.transcript.String()
	// Each provider must appear in the grouped output. Per-model rows
	// are sourced from internal/catalog — present when
	// `cmd/yotta-models refresh` has been run, absent otherwise. Assert
	// only on the headers (structural invariant) and require either the
	// catalog-empty hint OR at least one model row to be present, so
	// the test is robust to both states.
	for _, want := range []string{
		"models for anthropic",
		"models for openai",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("/model list all missing %q; transcript:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "catalog empty") && !strings.Contains(got, "ctx=") {
		t.Errorf("/model list all should show either 'catalog empty' or model rows; transcript:\n%s", got)
	}
}

func TestSlash_ModelTagSwitchesProvider(t *testing.T) {
	// /model claude-sonnet-4-6 should auto-switch the active provider
	// when the tag belongs to a different configured profile than the
	// session's current base URL.
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name = "anthropic"
kind = "anthropic"
base_url = "https://api.anthropic.com"
api_key_env = "ANTHROPIC_API_KEY"
default_model = "claude-sonnet-4-6"
  [[providers.models]]
  name = "claude-sonnet-4-6"
  tier = "balanced"
`)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	m, _ = typeAndEnter(t, m, "/model claude-sonnet-4-6")
	if m.baseURL != "https://api.anthropic.com" {
		t.Errorf("baseURL should auto-switch to anthropic, got %q", m.baseURL)
	}
	if m.modelName != "claude-sonnet-4-6" {
		t.Errorf("modelName = %q", m.modelName)
	}
}

func TestSlash_ProviderAddPersistsToConfigToml(t *testing.T) {
	m := newTestModel(t)
	// Start with empty config so /provider add is a clean append.
	seedConfigTOML(t, "")
	m, _ = typeAndEnter(t, m, "/provider add my-openrouter --kind openai-compatible --base-url https://openrouter.ai/api/v1 --key-env OPENROUTER_API_KEY --models gpt-4o,claude-sonnet-4-6")
	body, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	// config.Render aligns key columns with padding (e.g.
	// `name          = "my-openrouter"`), so the substring matches
	// here include the trailing space-padding-`=` rather than
	// pinning an exact whitespace shape. We only care that each
	// field appears with the right value, in any padded form.
	for _, want := range []string{
		`= "my-openrouter"`,
		`= "openai-compatible"`,
		`= "https://openrouter.ai/api/v1"`,
		`= "OPENROUTER_API_KEY"`,
		"gpt-4o",
		"claude-sonnet-4-6",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("config.toml after add missing %q\n%s", want, string(body))
		}
	}
}

// Removing an openai-auth profile also deletes the OAuth token store
// — equivalent to running `yottacode openai-auth logout`. Otherwise
// the bearer/refresh tokens linger on disk after the user thought
// they cleaned up the provider.
func TestSlash_ProviderRemoveOpenAIAuthDeletesTokenStore(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name          = "chatgpt"
kind          = "openai-auth"
base_url      = "https://chatgpt.com/backend-api/codex"
default_model = "gpt-5.5"

  [[providers.models]]
  name = "gpt-5.5"
`)
	// Seed a token store at the default path so the remove path has
	// something concrete to delete.
	tokenPath := filepath.Join(os.Getenv("HOME"), ".yottacode", "auth", "openai-auth.json")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(`{"access_token":"x","refresh_token":"y"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ = typeAndEnter(t, m, "/provider remove chatgpt")

	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Errorf("token store should have been deleted; stat err = %v", err)
	}
	got := stripANSI(m.transcript.String())
	if !strings.Contains(got, "logged out") {
		t.Errorf("transcript should mention auto-logout; got:\n%s", got)
	}
}

// Non-openai-auth removes leave any token store untouched —
// auto-logout is scoped to that one kind.
func TestSlash_ProviderRemoveNonOpenAIAuthLeavesTokenStore(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, `
[[providers]]
name          = "anthropic"
kind          = "anthropic"
base_url      = "https://api.anthropic.com"
default_model = "claude-sonnet-4-6"

  [[providers.models]]
  name = "claude-sonnet-4-6"
`)
	tokenPath := filepath.Join(os.Getenv("HOME"), ".yottacode", "auth", "openai-auth.json")
	_ = os.MkdirAll(filepath.Dir(tokenPath), 0o700)
	_ = os.WriteFile(tokenPath, []byte(`{"access_token":"keep"}`), 0o600)

	m, _ = typeAndEnter(t, m, "/provider remove anthropic")

	if _, err := os.Stat(tokenPath); err != nil {
		t.Errorf("token store should be untouched after non-openai-auth remove: %v", err)
	}
	_ = m
}

// /provider add openai-auth kicks off the inline OAuth flow: writes
// the config, surfaces "starting browser sign-in" in the transcript,
// and returns a tea.Cmd that drives StartLogin. Users no longer have
// to drop to a separate command. The blocking Wait runs as a
// follow-up cmd; this test only asserts the dispatch shape (config
// written + start message + non-nil cmd) since the OAuth flow itself
// requires a real browser + auth server.
func TestSlash_ProviderAddOpenAIAuthStartsInlineLogin(t *testing.T) {
	m := newTestModel(t)
	seedConfigTOML(t, "")
	m, cmd := typeAndEnter(t, m, "/provider add chatgpt --kind openai-auth --base-url https://chatgpt.com/backend-api/codex --models gpt-5.5 --default-model gpt-5.5")
	got := stripANSI(m.transcript.String())
	for _, want := range []string{
		"openai-auth",
		"starting browser sign-in",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("/provider add openai-auth transcript missing %q\n%s", want, got)
		}
	}
	if cmd == nil {
		t.Errorf("expected a tea.Cmd to drive StartLogin; got nil")
	}
}

// Bare /provider now opens the sub-menu picker; the legacy
// diagnostics dump moved to /doctor (and the underlying
// formatProviderProfile is still callable). Verify the picker
// opens and the diagnostics-builder still produces the expected
// fields when called directly — that's what matters for provider
// warning diagnostics.
func TestSlash_ProviderOpensSubMenuPicker(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/provider")
	if !m.providerPickerOpen {
		t.Errorf("/provider should open the sub-menu picker")
	}
	if m.providerPicker == nil {
		t.Fatalf("provider picker state should be attached")
	}
	if m.providerPicker.mode != providerMenuMode {
		t.Errorf("picker should start in menu mode, got %v", m.providerPicker.mode)
	}
}

func TestFormatProviderProfile_RendersDiagnostics(t *testing.T) {
	profile := adapter.ProviderProfile{
		Provider:                adapter.ProviderOpenAI,
		UsesResponsesAPI:        true,
		SupportsReasoning:       true,
		SupportsWebSearch:       true,
		SupportsCodeInterpreter: true,
		EnabledBuiltinTools:     []adapter.BuiltinToolKind{adapter.BuiltinToolWebSearch},
		Warnings:                []string{"API key is empty for a remote provider"},
	}
	got := formatProviderProfile(profile, "https://api.openai.com/v1")
	for _, want := range []string{
		"provider: openai",
		"api-style: responses",
		"enabled tools: web_search",
		"warning: API key is empty for a remote provider",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatProviderProfile output missing %q:\n%s", want, got)
		}
	}
}

func TestSlash_DoctorRunsActiveProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-5"}]}`)
	}))
	t.Cleanup(srv.Close)

	m := newTestModel(t)
	m.baseURL = srv.URL
	m.apiKey = "sk-test"
	m.modelName = "gpt-5"
	m.providerProfile = adapter.ProviderProfile{Provider: adapter.ProviderOpenAI, UsesResponsesAPI: true}
	m, cmd := cmdDoctor(m, nil)
	if cmd == nil {
		t.Fatalf("/doctor should return a probe command")
	}
	if !strings.Contains(m.transcript.String(), SysMsg(SysProgress, "doctor", "probing provider")) {
		t.Fatalf("doctor preflight line missing:\n%s", m.transcript.String())
	}
	m, _ = applyMsg(m, cmd())
	got := m.transcript.String()
	for _, want := range []string{
		"probe: endpoint=yes auth=yes model-visible=yes status=200",
		"models: gpt-5",
		"result: ok",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("/doctor output missing %q:\n%s", want, got)
		}
	}
}

func TestSlash_ClearStartsNewSession(t *testing.T) {
	m := newTestModel(t)
	oldID := m.sess.ID
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "hi"},
	}
	m, _ = typeAndEnter(t, m, "/clear")
	if m.sess.ID == oldID {
		t.Errorf("/clear should create a new session ID")
	}
	// System prompt should be carried over; user message should not.
	hasSystem := false
	for _, msg := range m.sess.Messages {
		if msg.Role == adapter.RoleUser {
			t.Errorf("user message leaked into new session")
		}
		if msg.Role == adapter.RoleSystem {
			hasSystem = true
		}
	}
	if !hasSystem {
		t.Errorf("/clear should preserve system prompt in new session")
	}
}

// TestSlash_SessionsShortcutResumesByName covers the power-user
// path: `/sessions <name>` skips the picker and resumes directly,
// mirroring how /model <name> works alongside the bare /model
// picker. Replaces the retired /resume slash command.
func TestSlash_SessionsShortcutResumesByName(t *testing.T) {
	m := newTestModel(t)
	other, _ := session.New("test-model", "/cwd")
	other.Name = "to-resume"
	other.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "old sys"},
		{Role: adapter.RoleUser, Content: "from old session"},
	}
	if err := other.Save(); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	m, _ = typeAndEnter(t, m, "/sessions to-resume")
	if m.sess.ID != other.ID {
		t.Errorf("/sessions <name> should swap session id; got %q want %q", m.sess.ID, other.ID)
	}
	v := m.transcript.String()
	if !strings.Contains(v, "from old session") {
		t.Errorf("/sessions <name> should rebuild transcript: %q", v)
	}
	if m.contextTokens == 0 {
		t.Errorf("/sessions <name> should reseed contextTokens from loaded session; got 0")
	}
}

// TestSlash_SessionsOpensPicker covers the bare-form entry. With no
// args, /sessions installs the layered Load/Resume/Rename/Export
// picker rather than dumping a flat list to scrollback.
func TestSlash_SessionsOpensPicker(t *testing.T) {
	m := newTestModel(t)
	a, _ := session.New("m", "/x")
	a.Name = "alpha"
	a.Save()

	m, _ = typeAndEnter(t, m, "/sessions")
	if !m.sessionsPickerOpen || m.sessionsPicker == nil {
		t.Fatalf("/sessions should open the picker; open=%v picker=%v",
			m.sessionsPickerOpen, m.sessionsPicker != nil)
	}
	if m.sessionsPicker.mode != sessionsMenuMode {
		t.Errorf("picker should start on the menu; got mode=%d", m.sessionsPicker.mode)
	}
	if got := len(m.sessionsPicker.menuItems); got != 4 {
		t.Errorf("menu should have 4 rows (Load/Resume/Rename/Export); got %d", got)
	}
	wantLabels := []string{"Load", "Resume", "Rename", "Export"}
	for i, want := range wantLabels {
		if m.sessionsPicker.menuItems[i].Label != want {
			t.Errorf("menu row %d = %q, want %q", i, m.sessionsPicker.menuItems[i].Label, want)
		}
	}
}

// TestSessionsPicker_LoadFlow walks the menu → load-list → Enter
// path end-to-end and asserts the active session swap.
func TestSessionsPicker_LoadFlow(t *testing.T) {
	m := newTestModel(t)
	other, _ := session.New("test-model", "/cwd")
	other.Name = "old-session"
	other.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "old sys"},
		{Role: adapter.RoleUser, Content: "earlier turn"},
	}
	if err := other.Save(); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	m, _ = typeAndEnter(t, m, "/sessions")
	// Menu: cursor starts on Load (row 0). Enter to drop into the
	// list. The current session sorts ahead of the seed because List
	// sorts by ID descending — find the seed row by walking the cursor
	// down until we land on it.
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionsPicker == nil || m.sessionsPicker.mode != sessionsLoadListMode {
		t.Fatalf("Enter on Load row should drop into load list; got mode=%v",
			m.sessionsPicker)
	}
	for i, info := range m.sessionsPicker.sessions {
		if info.ID == other.ID {
			m.sessionsPicker.listCursor = i
			break
		}
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sess.ID != other.ID {
		t.Errorf("picker load should swap session; got %q want %q", m.sess.ID, other.ID)
	}
	if m.sessionsPickerOpen {
		t.Errorf("picker should close after load commits")
	}
	if !strings.Contains(m.transcript.String(), "earlier turn") {
		t.Errorf("transcript should be rebuilt from the loaded session")
	}
}

// TestSessionsPicker_ResumeByRefFlow drives Menu → Resume (input) →
// type a name → Enter. Resume skips the recent-N list entirely and
// resumes whichever session matches the typed id-or-name reference.
// The list-pick equivalent is Load (covered separately).
func TestSessionsPicker_ResumeByRefFlow(t *testing.T) {
	m := newTestModel(t)
	other, _ := session.New("resume-model", "/cwd")
	other.Name = "labelled"
	other.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "old sys"},
		{Role: adapter.RoleUser, Content: "resumed turn"},
	}
	if err := other.Save(); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	m, _ = typeAndEnter(t, m, "/sessions")
	// Menu cursor starts on Load (row 0); Down once to land on Resume.
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionsPicker == nil || m.sessionsPicker.mode != sessionsResumeInputMode {
		t.Fatalf("Enter on Resume row should drop into resume input; got mode=%v",
			m.sessionsPicker)
	}
	for _, r := range "labelled" {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sess.ID != other.ID {
		t.Errorf("resume-by-ref should swap session; got %q want %q", m.sess.ID, other.ID)
	}
	if m.sessionsPickerOpen {
		t.Errorf("picker should close after resume commits")
	}
}

// TestSessionsPicker_ResumeByRefRequiresInput asserts the form keeps
// itself open with an error message when the user submits empty —
// rather than dropping back to the menu silently.
func TestSessionsPicker_ResumeByRefRequiresInput(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/sessions")
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown}) // Resume row
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionsPicker == nil || m.sessionsPicker.mode != sessionsResumeInputMode {
		t.Fatalf("expected resume input mode")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionsPicker == nil {
		t.Fatalf("empty submit should keep the form open")
	}
	if m.sessionsPicker.inputErr == "" {
		t.Errorf("empty ref should produce an inline error")
	}
}

// TestSessionsPicker_ResumeByRefCtrlSToggles verifies Ctrl+S in the
// Resume input mode flips the summarized state without typing into
// the input — `s` alone is reserved as a legal id/name character so
// it falls through to the textinput.
func TestSessionsPicker_ResumeByRefCtrlSToggles(t *testing.T) {
	m := newTestModel(t)
	m, _ = typeAndEnter(t, m, "/sessions")
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown}) // Resume row
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionsPicker.summarized {
		t.Fatalf("resume input should default summarized=false")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !m.sessionsPicker.summarized {
		t.Errorf("ctrl+s should toggle summarized on")
	}
	// A bare 's' in the input should NOT toggle the state — it should
	// land in the textinput as a typed character.
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "s"})
	if !m.sessionsPicker.summarized {
		t.Errorf("bare 's' should not flip the toggle (left it %v)", m.sessionsPicker.summarized)
	}
	if !strings.Contains(m.sessionsPicker.input.Value(), "s") {
		t.Errorf("bare 's' should type into the input; got value %q",
			m.sessionsPicker.input.Value())
	}
}

// TestSessionsPicker_RenameFlow drives Menu → Rename → list → input
// → commit, verifying the saved Name is written to disk and the live
// session reflects it when renaming the active session.
func TestSessionsPicker_RenameFlow(t *testing.T) {
	m := newTestModel(t)
	// Make sure the running session is on disk so List finds it; the
	// picker's list is sourced from session.List(), which only offers
	// sessions holding a real exchange — a system-only shell isn't
	// resumable, so it never reaches the picker.
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hello"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "world"},
	)
	if err := m.sess.Save(); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	m, _ = typeAndEnter(t, m, "/sessions")
	// Move cursor to Rename (row 2: Load=0, Resume=1, Rename=2, Export=3).
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionsPicker == nil || m.sessionsPicker.mode != sessionsRenameListMode {
		t.Fatalf("expected rename list mode")
	}
	// Cursor defaulted to the active session — Enter to drop into the
	// textinput.
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionsPicker.mode != sessionsRenameInputMode {
		t.Fatalf("expected rename input mode")
	}
	for _, r := range "my-feature" {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sess.Name != "my-feature" {
		t.Errorf("rename commit should set Name on live session; got %q", m.sess.Name)
	}
	loaded, err := session.Load("my-feature")
	if err != nil {
		t.Errorf("Load by name after rename: %v", err)
	}
	if loaded != nil && loaded.ID != m.sess.ID {
		t.Errorf("loaded ID = %q, want %q", loaded.ID, m.sess.ID)
	}
}

// TestSessionsPicker_ExportFlow drives Menu → Export → list → path
// input → commit, verifying the markdown lands at the chosen path
// with the prior turns rendered.
func TestSessionsPicker_ExportFlow(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hello"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "world"},
	)
	if err := m.sess.Save(); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	tmp := t.TempDir()
	m.cwd = tmp

	m, _ = typeAndEnter(t, m, "/sessions")
	// Menu: down three times to land on Export (Load=0, Resume=1,
	// Rename=2, Export=3), Enter to drop in.
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionsPicker == nil || m.sessionsPicker.mode != sessionsExportListMode {
		t.Fatalf("expected export list mode")
	}
	// Pick the only session in the list.
	for i, info := range m.sessionsPicker.sessions {
		if info.ID == m.sess.ID {
			m.sessionsPicker.listCursor = i
			break
		}
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.sessionsPicker.mode != sessionsExportInputMode {
		t.Fatalf("expected export input mode")
	}
	// Replace the suggested default with our own path so we can read
	// the file off a known location.
	m.sessionsPicker.input.SetValue(filepath.Join(tmp, "out.md"))
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	contents, err := os.ReadFile(filepath.Join(tmp, "out.md"))
	if err != nil {
		t.Fatalf("export file not written: %v", err)
	}
	body := string(contents)
	if !strings.Contains(body, "hello") || !strings.Contains(body, "world") {
		t.Errorf("export missing turns: %q", body)
	}
	if !strings.Contains(m.transcript.String(), "✓ export · wrote transcript") {
		t.Errorf("export should report success: %q", m.transcript.String())
	}
}

// TestSessionsPicker_LoadSummarizedToggle verifies pressing `s`
// while the Load list has focus flips the summarized state.
func TestSessionsPicker_LoadSummarizedToggle(t *testing.T) {
	m := newTestModel(t)
	// Needs a real exchange: session.List skips system-only shells.
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hello"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "world"},
	)
	if err := m.sess.Save(); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	m, _ = typeAndEnter(t, m, "/sessions")
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter}) // into Load list
	if m.sessionsPicker.summarized {
		t.Fatalf("load list should default summarized=false")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "s"})
	if !m.sessionsPicker.summarized {
		t.Errorf("`s` should toggle summarized on")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "s"})
	if m.sessionsPicker.summarized {
		t.Errorf("`s` should toggle summarized off again")
	}
}

func TestSlash_UnknownCommandReports(t *testing.T) {
	m, _ := typeAndEnter(t, newTestModel(t), "/totallyfake")
	if !strings.Contains(m.transcript.String(), "unknown command") {
		t.Errorf("unknown slash should report; got %q", m.transcript.String())
	}
}

func TestSlash_RecallWithoutIndexErrors(t *testing.T) {
	// /recall without an attached index should report a clear error rather
	// than silently doing nothing.
	m, _ := typeAndEnter(t, newTestModel(t), "/recall something")
	if !strings.Contains(m.transcript.String(), "[recall] index unavailable") {
		t.Errorf("expected index-unavailable error; got %q", m.transcript.String())
	}
}

func TestSlash_RecallReturnsHits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel(t)
	// Open an index, seed it with a session, and attach it to the model.
	idx := recall.MustOpenForTest(t)
	t.Cleanup(func() { _ = idx.Close() })
	seed := &session.Session{
		ID:      "seed-1",
		Model:   "qwen3.5",
		Created: m.sess.Created,
		Messages: []adapter.Message{
			{Role: adapter.RoleUser, Content: "I had a question about authentication"},
			{Role: adapter.RoleAssistant, Content: "Authentication via JWT works like this..."},
		},
	}
	if err := idx.IndexSession(seed); err != nil {
		t.Fatalf("IndexSession: %v", err)
	}
	m.recall = idx

	m, _ = typeAndEnter(t, m, "/recall authentication")
	if !m.recallPickerOpen || m.recallPicker == nil {
		t.Fatalf("/recall hits should open the picker; open=%v picker=%v",
			m.recallPickerOpen, m.recallPicker != nil)
	}
	view := stripANSI(m.View().Content)
	for _, want := range []string{"Recall", "1 session(s), 2 hit(s)", "seed-1", "2 hits", "↵ preview"} {
		if !strings.Contains(view, want) {
			t.Errorf("recall picker view missing %q:\n%s", want, view)
		}
	}
	if strings.Count(view, "seed-1") < 1 || strings.Count(view, "seed-1") > 3 {
		t.Errorf("recall picker should group duplicate session hits; got view:\n%s", view)
	}
	transcript := stripANSI(m.transcript.String())
	for _, banned := range []string{"[recall]", "seed-1", "Authentication via JWT"} {
		if strings.Contains(transcript, banned) {
			t.Errorf("recall hits should not be appended to transcript; found %q in:\n%s", banned, transcript)
		}
	}
}

func TestSlash_RecallPickerEnterPreviewsThenResumesSelectedSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel(t)
	idx := recall.MustOpenForTest(t)
	t.Cleanup(func() { _ = idx.Close() })

	seed, err := session.New("qwen3.5", m.cwd)
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	seed.Messages = []adapter.Message{
		{Role: adapter.RoleAssistant, Content: "previous context before auth"},
		{Role: adapter.RoleUser, Content: "authentication picker resume target"},
		{Role: adapter.RoleAssistant, Content: "next context after auth"},
	}
	if err := seed.Save(); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	if err := idx.IndexSession(seed); err != nil {
		t.Fatalf("IndexSession: %v", err)
	}
	m.recall = idx

	m, _ = typeAndEnter(t, m, "/recall authentication")
	if !m.recallPickerOpen || m.recallPicker == nil || len(m.recallPicker.groups) == 0 {
		t.Fatalf("/recall should open picker with grouped hits")
	}
	selectedID := m.recallPicker.groups[m.recallPicker.cursor].SessionID
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.recallPickerOpen || m.recallPicker == nil || !m.recallPicker.preview {
		t.Fatalf("first Enter should preview without closing picker")
	}
	if m.sess.ID == selectedID {
		t.Fatalf("first Enter should not resume the session yet")
	}
	if view := stripANSI(m.View().Content); !strings.Contains(view, "Recall preview") || !strings.Contains(view, "previous context before auth") || !strings.Contains(view, "authentication picker resume target") || !strings.Contains(view, "next context after auth") {
		t.Fatalf("preview should show selected session snippets; got:\n%s", view)
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.recallPickerOpen {
		t.Errorf("second Enter should close recall picker")
	}
	if m.sess.ID != selectedID {
		t.Errorf("second Enter should resume selected hit; got session %q want %q", m.sess.ID, selectedID)
	}
}

func TestSlash_RecallPreviewScrollsAndTogglesSummarized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel(t)
	idx := recall.MustOpenForTest(t)
	t.Cleanup(func() { _ = idx.Close() })

	seed := &session.Session{ID: "seed-scroll", Model: "qwen3.5", Created: m.sess.Created}
	for i := 0; i < recallPreviewPageSize+2; i++ {
		seed.Messages = append(seed.Messages, adapter.Message{Role: adapter.RoleUser, Content: fmt.Sprintf("authentication scroll target %d", i)})
	}
	if err := idx.IndexSession(seed); err != nil {
		t.Fatalf("IndexSession: %v", err)
	}
	m.recall = idx

	m, _ = typeAndEnter(t, m, "/recall authentication")
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.recallPicker.preview {
		t.Fatalf("Enter should open recall preview")
	}
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "showing hits 1-5") || strings.Contains(view, "target 6") {
		t.Fatalf("preview should start on the first bounded page; got:\n%s", view)
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.recallPicker.previewOffset != recallPreviewPageSize {
		t.Fatalf("PgDown previewOffset = %d, want %d", m.recallPicker.previewOffset, recallPreviewPageSize)
	}
	view = stripANSI(m.View().Content)
	if !strings.Contains(view, "showing hits 6-7") || !strings.Contains(view, "target 6") {
		t.Fatalf("PgDown should show the next preview page; got:\n%s", view)
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "s"})
	if !m.recallPicker.summarized {
		t.Fatalf("s should toggle summarized resume on")
	}
	if view := stripANSI(m.View().Content); !strings.Contains(view, "summarized (on)") {
		t.Fatalf("preview footer should show summarized state; got:\n%s", view)
	}
}

func TestSlash_RecallNoMatchesReportsCleanly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newTestModel(t)
	idx := recall.MustOpenForTest(t)
	t.Cleanup(func() { _ = idx.Close() })
	m.recall = idx

	m, _ = typeAndEnter(t, m, "/recall nothing-matches-this")
	if !strings.Contains(m.transcript.String(), "no matches") {
		t.Errorf("expected 'no matches' message; got %q", m.transcript.String())
	}
}

func TestSlash_RedoLoadsPreviousMessageIntoInput(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "explain fibonacci"},
		{Role: adapter.RoleAssistant, Content: "fib is..."},
	}
	m, _ = typeAndEnter(t, m, "/redo")
	if got := m.textInput.Value(); got != "explain fibonacci" {
		t.Errorf("/redo should load last user message; got %q", got)
	}
	// History truncated back to before the user message.
	if len(m.sess.Messages) != 1 {
		t.Errorf("history should have only system message left; got %d", len(m.sess.Messages))
	}
	if m.sess.Messages[0].Role != adapter.RoleSystem {
		t.Errorf("only system message should remain; got %v", m.sess.Messages[0].Role)
	}
}

func TestSlash_RedoErrorsWhenNoUserMessage(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
	}
	m, _ = typeAndEnter(t, m, "/redo")
	if !strings.Contains(m.transcript.String(), "no previous user message") {
		t.Errorf("/redo on empty conversation should error; got %q", m.transcript.String())
	}
}

func TestSlash_RedoDropsEverythingAfterUserMessage(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleSystem, Content: "sys"},
		{Role: adapter.RoleUser, Content: "list files"},
		{Role: adapter.RoleAssistant, ToolCalls: []adapter.ToolCall{
			{ID: "c1", Name: "list_dir", ArgsJSON: `{}`},
		}},
		{Role: adapter.RoleTool, ToolCallID: "c1", Content: "results..."},
		{Role: adapter.RoleAssistant, Content: "you have 5 files"},
	}
	m, _ = typeAndEnter(t, m, "/redo")
	if got := m.textInput.Value(); got != "list files" {
		t.Errorf("input = %q, want 'list files'", got)
	}
	if len(m.sess.Messages) != 1 {
		t.Errorf("expected only system message left; got %d messages", len(m.sess.Messages))
	}
}

func TestPalette_OpensWhenInputStartsWithSlash(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "/"})
	if !m.paletteOpen {
		t.Errorf("palette should open after typing '/'")
	}
}

func TestPalette_TabCompletes(t *testing.T) {
	m := newTestModel(t)
	for _, r := range "/mo" {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	if !m.paletteOpen {
		t.Fatalf("palette should be open after /mo")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.textInput.Value(); !strings.HasPrefix(got, "/model") {
		t.Errorf("Tab should complete to /model; got %q", got)
	}
	if m.paletteOpen {
		t.Errorf("palette should close after Tab")
	}
}

func TestPalette_MouseHoverMovesIndex(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "/"})
	if !m.paletteOpen {
		t.Fatalf("palette should be open after typing '/'")
	}

	// The inline palette starts immediately after the startup card on the hero.
	// Its first selectable command row is below the top border, title, and
	// divider rows.
	paletteTop := m.inlinePaletteTop()
	m, cmd := applyMsg(m, tea.MouseMotionMsg{X: 4, Y: paletteTop + 3})
	if cmd != nil {
		t.Fatalf("hover should not trigger a command, got %T", cmd)
	}
	if got := m.paletteIndex; got != 0 {
		t.Fatalf("hover over first command row set paletteIndex = %d, want 0", got)
	}

	m, _ = applyMsg(m, tea.MouseMotionMsg{X: 4, Y: paletteTop + 5})
	if got := m.paletteIndex; got != 2 {
		t.Fatalf("hover over third command row set paletteIndex = %d, want 2", got)
	}
}

func TestPalette_MouseClickRunsMainSlashCommand(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "/"})
	if !m.paletteOpen {
		t.Fatalf("palette should be open after typing '/'")
	}
	target := 2 // /model is a visible no-args picker command near the top.

	paletteTop := m.inlinePaletteTop()
	m, _ = applyMsg(m, tea.MouseClickMsg{X: 4, Y: paletteTop + 3 + target})
	if !m.modelPickerOpen || m.modelPicker == nil {
		t.Fatalf("click should execute /model and open the model picker")
	}
	if m.paletteOpen {
		t.Fatal("palette should close after click execution")
	}
}

func TestPalette_DownArrowMovesIndex(t *testing.T) {
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "/"})
	startIdx := m.paletteIndex
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.paletteIndex != startIdx+1 {
		t.Errorf("Down arrow should advance idx; got %d", m.paletteIndex)
	}
}

func TestPalette_EscClosesAndClears(t *testing.T) {
	m := newTestModel(t)
	for _, r := range "/mo" {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.paletteOpen {
		t.Errorf("Esc should close palette")
	}
	if m.textInput.Value() != "" {
		t.Errorf("Esc should clear input; got %q", m.textInput.Value())
	}
}

func TestPalette_EnterRunsHighlightedNoArgsCommand(t *testing.T) {
	// User opens palette with "/", arrows down to a no-args command, hits
	// Enter — should execute the highlighted command instead of trying to
	// run "/" (which has historically reported "unknown command").
	m := newTestModel(t)
	m, _ = applyMsg(m, tea.KeyPressMsg{Text: "/"})
	if !m.paletteOpen {
		t.Fatalf("palette should be open")
	}
	// Find /help in the filtered list and arrow down to it. /help is
	// chosen because it has no args and opens visible overlay output, so
	// the post-Enter assertion is unambiguous.
	target := -1
	for i, c := range m.paletteFiltered {
		if c.Name == "help" {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("/help not in palette")
	}
	for i := 0; i < target; i++ {
		m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if m.paletteIndex != target {
		t.Fatalf("paletteIndex = %d, want %d", m.paletteIndex, target)
	}

	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.helpOpen || !strings.Contains(m.helpPanel, "/help") {
		t.Errorf("Enter should have executed /help and opened the help overlay")
	}
	if m.paletteOpen {
		t.Errorf("palette should close after execution")
	}
}

func TestPalette_EnterFillsInForArgsCommand(t *testing.T) {
	// /recall takes a <query>. Enter on the highlighted /recall should
	// fill in "/recall " and leave the palette closed so the user can
	// type the arg — not execute /recall with empty args. (This was
	// originally tested against /save, then /forget, before each got
	// folded into a picker; /recall is the current canonical
	// args-required command.)
	m := newTestModel(t)
	for _, r := range "/recal" {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	if !m.paletteOpen {
		t.Fatalf("palette should be open after /recal")
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.textInput.Value(); got != "/recall " {
		t.Errorf("Enter on /recall should fill in '/recall '; got %q", got)
	}
	if m.paletteOpen {
		t.Errorf("palette should close after fill-in")
	}
}

func TestPalette_EnterPassesThroughTypedArgs(t *testing.T) {
	// "/model qwen3.5" + Enter should execute the typed input verbatim, even
	// though the palette is technically still open.
	m := newTestModel(t)
	for _, r := range "/model qwen3.5" {
		m, _ = applyMsg(m, tea.KeyPressMsg{Text: string(r)})
	}
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.modelName != "qwen3.5" {
		t.Errorf("/model with typed arg should swap modelName; got %q", m.modelName)
	}
}

func TestStatusLine_RendersCompactPrefix(t *testing.T) {
	line := statusLine("provider", "switched to openai")
	if line != "○ provider · switched to openai" {
		t.Fatalf("statusLine rendered %q", line)
	}
}

func TestStatusHelpers_RenderSymbolsAndHints(t *testing.T) {
	cases := map[string]string{
		statusOKLine("provider", "added openai"):       "✓ provider · added openai",
		statusWarnLine("model", "context unknown"):     "⚠ model · context unknown",
		statusActionLine("openai-auth", "signing in"):  "… openai-auth · signing in",
		statusHintLine("set context_window in config"): "○ hint · set context_window in config",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestShortenMiddle(t *testing.T) {
	got := shortenMiddle("nvidia/nemotron-3-ultra-550b-a55b", 24)
	if got != "nvidia/nem...a-550b-a55b" {
		t.Fatalf("shortenMiddle = %q", got)
	}
	if got := shortenMiddle("short", 24); got != "short" {
		t.Fatalf("short input changed to %q", got)
	}
}
