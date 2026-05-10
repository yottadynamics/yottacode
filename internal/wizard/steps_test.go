package wizard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/catalog"
)

// TestView_NarrowTerminalNoLineWrap renders every step at several
// terminal widths and asserts that no single output line exceeds
// the budget. The bug class this guards against is "long styled
// segments wrap at terminal width, stacking the rest of the screen
// vertically" — what the user reported as 'all the rows expand in
// one column' on resize.
//
// We measure visible width by stripping ANSI escapes; a 100-char
// styled string with color codes can have ~15 visible chars but
// len() == 100, so the naive byte/rune comparison would be a
// false-positive minefield.
func TestView_NarrowTerminalNoLineWrap(t *testing.T) {
	// Test a representative band of widths. 40 = the lower bound we
	// can support cleanly; 60 = common laptop split-screen; 80 =
	// canonical terminal; 120 = wide.
	widths := []int{40, 60, 80, 120}
	const tolerance = 2 // small slack for trailing whitespace etc.

	steps := []struct {
		name string
		step stepID
	}{
		{"welcome", stepWelcome},
		{"providers", stepProviders},
		{"configure", stepConfigure},
		{"active", stepActive},
		{"router", stepRouter},
		{"review", stepReview},
	}

	for _, width := range widths {
		for _, s := range steps {
			t.Run(fmt.Sprintf("w%d/%s", width, s.name), func(t *testing.T) {
				m0 := newWizardModel(context.Background(), Options{ConfigPath: "/tmp/x.toml", EnvPath: "/tmp/x.env"})
				// Dispatch WindowSizeMsg through Update so the
				// per-provider textinput Widths resize. Setting
				// m.width directly leaves the textinputs at their
				// 48-col construction default which overflows narrow
				// terminals.
				updated, _ := m0.Update(tea.WindowSizeMsg{Width: width, Height: 24})
				m := updated.(wizardModel)
				// inputs are pre-built for every catalog entry by the
				// constructor; the test just marks a couple as
				// configured-with-model so activeChoices / Review have
				// content to render.
				anthIdx := catalogIndex("anthropic")
				customIdx := catalogIndex("custom")
				m.inputs[anthIdx].chosenModel = "claude-sonnet-4-6"
				m.inputs[anthIdx].configured = true
				m.inputs[customIdx].chosenModel = "test-model"
				m.inputs[customIdx].configured = true
				m.configIdx = customIdx
				m.inputs[customIdx].field = 2
				m.finalPlan = m.assemblePlan()
				m.reviewBody = m.finalPlan.RenderTOML()
				m.step = s.step
				m.err = nil

				out := m.View()
				for i, line := range splitLines(out) {
					if vw := lipglossVisibleWidth(line); vw > width+tolerance {
						t.Errorf("w=%d step=%s line %d width=%d exceeds budget+tol=%d\n  line: %q",
							width, s.name, i, vw, width+tolerance, line)
					}
				}
			})
		}
	}
}

// catalogIndex looks up the index of a catalog entry by name. Tests
// pre-fill m.inputs slots by index (the constructor builds one slot
// per entry, in Catalog order), so this helper keeps tests resilient
// to catalog reordering.
func catalogIndex(name string) int {
	for i, e := range Catalog {
		if e.Name == name {
			return i
		}
	}
	return -1
}

// splitLines splits a multi-line string on '\n' without dropping
// the trailing empty record (ranged loops in tests appreciate the
// one-line-per-iteration shape).
func splitLines(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// lipglossVisibleWidth measures rendered width skipping ANSI
// escapes — same job lipgloss.Width does, but avoiding a direct
// dependency at test scope (the import is already at file scope
// in steps.go).
func lipglossVisibleWidth(s string) int {
	// We import lipgloss in steps.go; the test file shares the
	// package, so we can reach it via the package binding indirectly
	// through any symbol that uses it. Simpler: re-implement the
	// strip ourselves — ANSI escape sequences are ESC[...m. Strip
	// those, count runes.
	var b []rune
	in := []rune(s)
	for i := 0; i < len(in); i++ {
		if in[i] == 0x1b && i+1 < len(in) && in[i+1] == '[' {
			// Skip until 'm' or other letter terminator.
			for i+1 < len(in) {
				i++
				r := in[i]
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
					break
				}
			}
			continue
		}
		b = append(b, in[i])
	}
	return len(b)
}

// TestUpdate_WindowSizeMsg_ResizesInputs verifies the per-provider
// textinputs get their Width updated when WindowSizeMsg arrives.
// Without this, a resize from narrow → wide leaves stale tiny
// input boxes; wide → narrow leaves boxes that overflow the
// terminal. Either looks broken.
func TestUpdate_WindowSizeMsg_ResizesInputs(t *testing.T) {
	custom := *FindCatalogEntry("custom")
	m := newWizardModel(context.Background(), Options{})
	m.inputs = []providerInputs{m.newProviderInputs(custom)}
	// Initial width before the first WindowSizeMsg is the historic
	// default (m.width is 0 at construction).
	if m.inputs[0].customModel.Width != 48 {
		t.Fatalf("pre-resize Width = %d, want 48 (default for unset terminal)",
			m.inputs[0].customModel.Width)
	}

	// Wide terminal: 120 cols → input ceilinged at 80.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	wm := updated.(wizardModel)
	if wm.inputs[0].customModel.Width != 80 {
		t.Errorf("after wide resize, customModel.Width = %d, want 80",
			wm.inputs[0].customModel.Width)
	}
	if wm.inputs[0].key.Width != 80 || wm.inputs[0].baseURL.Width != 80 {
		t.Errorf("all three inputs should resize uniformly; got key=%d baseURL=%d",
			wm.inputs[0].key.Width, wm.inputs[0].baseURL.Width)
	}

	// Narrow terminal: 32 cols → input floored at 24 (32-8).
	updated, _ = wm.Update(tea.WindowSizeMsg{Width: 32, Height: 20})
	wm = updated.(wizardModel)
	if wm.inputs[0].customModel.Width != 24 {
		t.Errorf("after narrow resize, customModel.Width = %d, want 24",
			wm.inputs[0].customModel.Width)
	}
}

// TestFocusActiveConfigField_FreeFormModel guards against the bug
// where the model textinput on a free-form provider (custom,
// nvidia-nim, ollama) was never Focus()'d, so bubbles/textinput's
// Update silently dropped every keystroke. The screenshot fixture
// for that bug showed "Configure custom (2/2)" with the user unable
// to type a model tag.
func TestFocusActiveConfigField_FreeFormModel(t *testing.T) {
	custom := *FindCatalogEntry("custom")
	m := newWizardModel(context.Background(), Options{})
	m.inputs = []providerInputs{m.newProviderInputs(custom)}
	m.configIdx = 0
	m.inputs[0].field = 2 // model field

	// Custom needs its base URL set before model becomes the active
	// field — otherwise fieldKind for field=2 still returns "model"
	// but our regression target is the focus call. Set it directly.
	m.inputs[0].baseURL.SetValue("https://example.test/v1")

	cmd := m.focusActiveConfigField()
	if cmd == nil {
		t.Fatalf("expected focus cmd (cursor blink) for free-form model field, got nil")
	}
	if !m.inputs[0].customModel.Focused() {
		t.Errorf("customModel should be focused for free-form provider; was not")
	}
	if m.inputs[0].key.Focused() || m.inputs[0].baseURL.Focused() {
		t.Errorf("only customModel should be focused; got key=%v baseURL=%v",
			m.inputs[0].key.Focused(), m.inputs[0].baseURL.Focused())
	}
}

// TestFocusActiveConfigField_CuratedPicker verifies the inverse of
// the free-form case: when the wizard is rendering a curated picker
// (anthropic/openai/gemini with a populated catalog), no textinput
// should be focused on field=2 — selection is via arrow keys.
// Returning nil here is correct: there's no input to blink.
//
// Synthesizes a populated curatedModels slice directly rather than
// depending on the embedded catalog (which is empty by default in
// CI; refresh requires API keys).
func TestFocusActiveConfigField_CuratedPicker(t *testing.T) {
	anthropic := *FindCatalogEntry("anthropic")
	m := newWizardModel(context.Background(), Options{})
	in := m.newProviderInputs(anthropic)
	// Inject a synthetic curated list so the picker code path is
	// exercised even when catalog.gen.json hasn't been refreshed.
	in.curatedModels = []catalog.Model{
		{ID: "model-a", DisplayName: "Model A", Provider: "anthropic"},
		{ID: "model-b", DisplayName: "Model B", Provider: "anthropic"},
	}
	m.inputs = []providerInputs{in}
	m.configIdx = 0
	m.inputs[0].field = 2 // model field

	cmd := m.focusActiveConfigField()
	if cmd != nil {
		t.Errorf("curated picker should not return a focus cmd on model field; got %v", cmd)
	}
	if m.inputs[0].customModel.Focused() {
		t.Errorf("customModel must NOT be focused for curated picker; selection is via arrow keys")
	}
}

// TestUpdateConfigure_DownArrowDrivesCuratedPicker is a regression
// test for the scroll bug where Down/Up keystrokes were intercepted
// at the top of updateConfigure for field navigation (key → baseURL
// → model) and never reached the curated picker — leaving the user
// unable to scroll past the first model. The fix routes Down/Up to
// the modelCursor when curatedPickerActive; Tab/Shift+Tab keep the
// field-switching role so the user can still jump back to key.
func TestUpdateConfigure_DownArrowDrivesCuratedPicker(t *testing.T) {
	anthropic := *FindCatalogEntry("anthropic")
	m := newWizardModel(context.Background(), Options{})
	in := m.newProviderInputs(anthropic)
	in.curatedModels = []catalog.Model{
		{ID: "a", Provider: "anthropic"},
		{ID: "b", Provider: "anthropic"},
		{ID: "c", Provider: "anthropic"},
	}
	in.envHas = true // skip key field so we land on model directly
	in.field = 2
	m.inputs = []providerInputs{in}
	m.configIdx = 0
	m.step = stepConfigure

	// Down should advance the cursor, NOT switch wizard fields.
	updated, _ := m.updateConfigure(tea.KeyMsg{Type: tea.KeyDown})
	wm := updated.(wizardModel)
	if wm.inputs[0].modelCursor != 1 {
		t.Errorf("Down: modelCursor = %d, want 1", wm.inputs[0].modelCursor)
	}
	if wm.inputs[0].field != 2 {
		t.Errorf("Down should not change field; got %d, want 2", wm.inputs[0].field)
	}
	// Down again → 2, then bounded at last entry.
	updated, _ = wm.updateConfigure(tea.KeyMsg{Type: tea.KeyDown})
	wm = updated.(wizardModel)
	updated, _ = wm.updateConfigure(tea.KeyMsg{Type: tea.KeyDown})
	wm = updated.(wizardModel)
	if wm.inputs[0].modelCursor != 2 {
		t.Errorf("Down should bound at last entry; got %d, want 2", wm.inputs[0].modelCursor)
	}
	// Up retreats.
	updated, _ = wm.updateConfigure(tea.KeyMsg{Type: tea.KeyUp})
	wm = updated.(wizardModel)
	if wm.inputs[0].modelCursor != 1 {
		t.Errorf("Up: modelCursor = %d, want 1", wm.inputs[0].modelCursor)
	}
	// Tab still switches fields (so the user isn't trapped on the
	// model row when they want to revisit the key).
	updated, _ = wm.updateConfigure(tea.KeyMsg{Type: tea.KeyTab})
	wm = updated.(wizardModel)
	if wm.inputs[0].field == 2 {
		t.Errorf("Tab should advance field past 2; stayed at %d", wm.inputs[0].field)
	}
}

// TestFocusActiveConfigField_SecondProviderFreeForm replays the
// reported bug: first provider non-free-form (anthropic), second
// free-form (custom). After advancing configIdx, the customModel
// of provider #2 must be focused — not provider #1's, and not
// nothing.
func TestFocusActiveConfigField_SecondProviderFreeForm(t *testing.T) {
	anth := *FindCatalogEntry("anthropic")
	custom := *FindCatalogEntry("custom")
	m := newWizardModel(context.Background(), Options{})
	m.inputs = []providerInputs{
		m.newProviderInputs(anth),
		m.newProviderInputs(custom),
	}
	// User advanced past anthropic; now on provider #2, model field.
	m.configIdx = 1
	m.inputs[1].field = 2

	cmd := m.focusActiveConfigField()
	if cmd == nil {
		t.Fatalf("expected focus cmd for second provider's free-form model; got nil")
	}
	if !m.inputs[1].customModel.Focused() {
		t.Errorf("provider #2 customModel should be focused; bug regression")
	}
	if m.inputs[0].customModel.Focused() {
		t.Errorf("provider #1 customModel should NOT be focused; configIdx=1 means focus is on #2")
	}
}

// TestInputWidthFor enforces the floor / ceiling / middle band
// behavior. Floor matters on narrow terminals (40-col), ceiling
// keeps inputs from stretching across an ultra-wide monitor, and
// the unset case (width=0 — first frame before WindowSizeMsg) must
// fall back to the historic default.
func TestInputWidthFor(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"unset (pre-WindowSizeMsg)", 0, 48},
		{"very narrow", 24, 20}, // 24-8=16, floored to 20
		{"floor exact", 28, 20}, // 28-8=20
		{"narrow", 40, 32},
		{"common 80 col", 80, 72},
		{"wide", 100, 80}, // ceilinged
		{"ultra-wide", 200, 80},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := inputWidthFor(tc.in); got != tc.want {
				t.Errorf("inputWidthFor(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestPreviewWidthFor covers the same floor/ceiling logic but at
// the wider band the TOML preview uses.
func TestPreviewWidthFor(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"unset", 0, 80},
		{"very narrow", 30, 30}, // 30-6=24, floored to 30
		{"narrow", 50, 44},
		{"common 80 col", 80, 74},
		{"wide", 120, 100}, // ceilinged
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := previewWidthFor(tc.in); got != tc.want {
				t.Errorf("previewWidthFor(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestTruncate guards the ellipsis edge cases — short strings
// passthrough, long strings clip with "…", width<=1 returns empty.
func TestTruncate(t *testing.T) {
	tests := []struct {
		s     string
		width int
		want  string
	}{
		{"", 10, ""},
		{"abc", 10, "abc"},
		{"abcdefgh", 5, "abcd…"},
		{"abc", 1, ""}, // too narrow to even fit the ellipsis
		{"abc", 0, ""},
		{"unicode-é-é-é", 8, "unicode…"},
	}
	for _, tc := range tests {
		if got := truncate(tc.s, tc.width); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.s, tc.width, got, tc.want)
		}
	}
}

// TestProvidersFlow_EnterDropsIntoConfigure locks in the rework's
// core promise: pressing Enter on a provider row jumps directly into
// that provider's configure screen, no checkboxes / multi-select /
// commit batch. Replaces the cursor-vs-checkbox ambiguity that made
// users hit Enter expecting "select this" and accidentally commit.
func TestProvidersFlow_EnterDropsIntoConfigure(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	m.step = stepProviders
	target := catalogIndex("openai")
	m.providerCursor = target

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	wm := updated.(wizardModel)
	if wm.step != stepConfigure {
		t.Fatalf("Enter on provider row should drop into stepConfigure; got %v", wm.step)
	}
	if wm.configIdx != target {
		t.Errorf("configIdx = %d, want %d (cursor target)", wm.configIdx, target)
	}
}

// TestProvidersFlow_ConfiguredMarker guards the ✓ marker that
// replaces the old [✓] checkbox. A configured row must render with
// the OK marker and accent name styling so the user can see at a
// glance which providers are ready.
func TestProvidersFlow_ConfiguredMarker(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	m.width = 120
	idx := catalogIndex("anthropic")
	m.inputs[idx].configured = true
	m.inputs[idx].chosenModel = "claude-sonnet-4-6"

	got := stripANSI(m.viewProviders())
	// Look for "✓ anthropic" — the marker immediately precedes the
	// provider name on configured rows.
	if !strings.Contains(got, "✓ anthropic") {
		t.Errorf("configured row should render '✓ anthropic'; got:\n%s", got)
	}
	// And an unconfigured row should render with the dim · marker.
	if !strings.Contains(got, "· custom") {
		t.Errorf("unconfigured row should render '· custom'; got:\n%s", got)
	}
}

// TestProvidersFlow_ContinueRow asserts the sentinel row at the
// bottom of the list. Cursor parks on it at index len(Catalog),
// Enter advances to stepActive when at least one provider is
// configured, and is gated with an error otherwise.
func TestProvidersFlow_ContinueRow(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	m.step = stepProviders
	// Force a clean state — the constructor may have auto-configured
	// providers from env, which would mask the gating test.
	for i := range m.inputs {
		m.inputs[i].configured = false
	}

	// Park cursor on the Continue sentinel.
	m.providerCursor = len(Catalog)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	wm := updated.(wizardModel)
	if wm.step != stepProviders {
		t.Errorf("Continue with nothing configured should stay on stepProviders; got %v", wm.step)
	}
	if wm.err == nil {
		t.Errorf("Continue with nothing configured should set an error; got nil")
	}

	// Now mark one provider configured and try again.
	idx := catalogIndex("anthropic")
	wm.inputs[idx].configured = true
	wm.inputs[idx].chosenModel = "claude-sonnet-4-6"
	wm.err = nil
	updated, _ = wm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	wm = updated.(wizardModel)
	if wm.step != stepActive {
		t.Errorf("Continue with one configured should advance to stepActive; got %v", wm.step)
	}
}

// TestConfigureFlow_FinishMarksConfiguredAndReturns verifies that
// pressing Enter on the model field of the configure screen marks the
// provider configured and returns to the provider list (with the
// cursor parked on the just-configured row), instead of looping
// auto-advance into the next provider.
func TestConfigureFlow_FinishMarksConfiguredAndReturns(t *testing.T) {
	m := newWizardModel(context.Background(), Options{SkipValidation: true})
	idx := catalogIndex("anthropic")
	m.configIdx = idx
	m.inputs[idx].field = 2 // model field
	m.inputs[idx].chosenModel = ""
	m.inputs[idx].configured = false
	// Pre-fill the API key — the new guard refuses to mark configured
	// without a key in env, in input, or explicit skipKey, since
	// otherwise users end up with broken configs that fail at runtime
	// with a confusing 401.
	m.inputs[idx].key.SetValue("sk-test")
	// Anthropic is free-form now (no curated Models list) — so the
	// model branch reads in.customModel.Value() instead of
	// modelCursor. Pre-fill the textinput value so the Enter handler
	// has something to commit; an empty value would error with
	// "model tag required."
	m.inputs[idx].customModel.SetValue("claude-sonnet-4-6")
	// fieldKind for anthropic with no env key on field=0 returns
	// "key" — the model branch needs field=2 reachable. We jumped
	// straight to field=2 above to skip past key/baseURL.
	m.step = stepConfigure

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	wm := updated.(wizardModel)
	if !wm.inputs[idx].configured {
		t.Errorf("Enter on model field should mark inputs[%d].configured = true", idx)
	}
	if wm.step != stepProviders {
		t.Errorf("Enter on model field should return to stepProviders; got %v", wm.step)
	}
	if wm.providerCursor != idx {
		t.Errorf("provider cursor should park on the just-configured row %d; got %d", idx, wm.providerCursor)
	}
}

// TestConfigureFlow_RefusesEmptyKey is the regression test for the
// reported "I configured OpenAI without entering an API key and the
// agent failed at runtime with local-no-auth" bug. Pressing Enter on
// the model field with no key in env, no key entered, and no
// explicit skipKey choice must NOT mark the provider configured.
// The user gets bounced back to the key field with a clear error.
func TestConfigureFlow_RefusesEmptyKey(t *testing.T) {
	m := newWizardModel(context.Background(), Options{SkipValidation: true})
	idx := catalogIndex("openai")
	m.configIdx = idx
	in := &m.inputs[idx]
	// Force the gap that makes this bug reachable: envHas=false
	// (env var not set when wizard opened), skipKey=false (no
	// explicit Ctrl+S), key.Value()="" (user didn't type one),
	// but field=2 (somehow advanced past key — happens via Tab or
	// when the curated picker steals focus on field=2).
	in.envHas = false
	in.skipKey = false
	in.key.SetValue("")
	in.customModel.SetValue("gpt-4o")
	in.field = 2
	in.configured = false
	m.step = stepConfigure

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	wm := updated.(wizardModel)
	if wm.inputs[idx].configured {
		t.Errorf("provider must NOT be marked configured without an API key")
	}
	if wm.err == nil {
		t.Errorf("expected an error explaining the missing key; got nil")
	}
	if wm.inputs[idx].field != 0 {
		t.Errorf("user should be bounced back to key field (0); got %d", wm.inputs[idx].field)
	}
}

// TestConfigureFlow_AcceptsExplicitSkipKey: when the user explicitly
// chose to skip via Ctrl+S, the wizard accepts the empty-key state
// (the user signed off on it). Runtime will surface the missing-key
// error via the errored adapter.
func TestConfigureFlow_AcceptsExplicitSkipKey(t *testing.T) {
	m := newWizardModel(context.Background(), Options{SkipValidation: true})
	idx := catalogIndex("openai")
	m.configIdx = idx
	in := &m.inputs[idx]
	in.envHas = false
	in.skipKey = true // explicit user choice
	in.key.SetValue("")
	in.customModel.SetValue("gpt-4o")
	in.field = 2
	in.configured = false
	m.step = stepConfigure

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	wm := updated.(wizardModel)
	if !wm.inputs[idx].configured {
		t.Errorf("explicit skipKey should still mark configured (user accepts the trade-off)")
	}
}

// TestConfigureFlow_GoBackReturnsToList verifies ESC from the
// configure screen returns to the provider list with the cursor on
// the row that was being edited — so the user keeps their place when
// reviewing or jumping between providers.
func TestConfigureFlow_GoBackReturnsToList(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	idx := catalogIndex("openai")
	m.configIdx = idx
	m.providerCursor = 0 // simulate stale cursor from welcome
	m.step = stepConfigure

	back := m.goBack()
	if back.step != stepProviders {
		t.Errorf("goBack from stepConfigure should land on stepProviders; got %v", back.step)
	}
	if back.providerCursor != idx {
		t.Errorf("provider cursor should follow configIdx back to %d; got %d", idx, back.providerCursor)
	}
}

// TestNewWizardModel_EnvDetectedNotAutoConfigured guards the
// deliberate decision to NOT pre-mark configured for env-detected
// providers. ✓ exclusively means "user has walked through configure,"
// while the env badge carries the "key is detected" signal. The
// previous behavior surprised users into thinking Enter+ESC on a row
// had selected it, when really the constructor had marked it before
// they did anything.
func TestNewWizardModel_EnvDetectedNotAutoConfigured(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("NVIDIA_API_KEY", "")

	m := newWizardModel(context.Background(), Options{})
	anthIdx := catalogIndex("anthropic")
	openaiIdx := catalogIndex("openai")

	// envHas should still reflect the detection so configure can skip
	// the key field — but configured stays false until the user
	// explicitly walks the provider through.
	if !m.inputs[anthIdx].envHas {
		t.Errorf("anthropic with key in env should set envHas=true")
	}
	if m.inputs[anthIdx].configured {
		t.Errorf("anthropic with key in env should NOT auto-configure (✓ requires explicit walk-through)")
	}
	if m.inputs[openaiIdx].configured {
		t.Errorf("openai without key in env should also not be configured")
	}
}

// TestPluralModels_OneVsMany guards Pass 1 #2 — "1 model" not
// "1 models." Trivial but easy to regress when string templates get
// touched.
func TestPluralModels_OneVsMany(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "models"},
		{1, "model"},
		{2, "models"},
		{47, "models"},
	}
	for _, tc := range tests {
		if got := pluralModels(tc.n); got != tc.want {
			t.Errorf("pluralModels(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestCatalog_NvidiaNoteNo404 guards Pass 1 #4 — the "(404 means key
// ≠ model)" tail is gone from the provider-pick row. The explanation
// belongs at the point of failure (validation glyph), not as a
// preemptive warning before the user has done anything.
func TestCatalog_NvidiaNoteNo404(t *testing.T) {
	e := FindCatalogEntry("nvidia-nim")
	if e == nil {
		t.Fatal("nvidia-nim missing from catalog")
	}
	if strings.Contains(e.Note, "404") {
		t.Errorf("nvidia-nim Note should not preempt 404 warning; got %q", e.Note)
	}
}

// TestValidationGlyph_HintLabel guards Pass 1 #5 — the unstarted-
// validation hint reads "enter validate," not "ctrl+v validate."
// ctrl+v is universally paste; the old label fought muscle memory.
func TestValidationGlyph_HintLabel(t *testing.T) {
	in := providerInputs{} // unstarted
	got := stripANSI(validationGlyph(in, ""))
	if !strings.Contains(got, "enter validate") {
		t.Errorf("default hint should say 'enter validate'; got %q", got)
	}
	if strings.Contains(got, "ctrl+v") {
		t.Errorf("default hint must not mention ctrl+v; got %q", got)
	}
}

// TestValidationGlyph_OKLabel guards Pass 1 #7 — the OK glyph reads
// "✓ key valid" rather than the bare upstream "ok," so the user has
// unmistakable positive feedback. Upstream non-trivial details
// (e.g. provider-specific notes) stay visible after the label.
func TestValidationGlyph_OKLabel(t *testing.T) {
	in := providerInputs{
		validation: ValidationResult{Status: ValidationOK, Detail: "ok"},
	}
	got := stripANSI(validationGlyph(in, ""))
	if !strings.Contains(got, "key valid") {
		t.Errorf("OK glyph should say 'key valid'; got %q", got)
	}
	in.validation.Detail = "47 models"
	got = stripANSI(validationGlyph(in, ""))
	if !strings.Contains(got, "key valid · 47 models") {
		t.Errorf("OK glyph should preserve non-trivial detail; got %q", got)
	}
}

// TestFreeFormModelPlaceholder guards Pass 1 #6 — the model-tag
// example shown in the free-form input must match the provider being
// configured. Showing Ollama syntax under the NIM screen misleads.
func TestFreeFormModelPlaceholder(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		// Cloud providers — went free-form when their curated Models
		// lists were removed. Placeholder shape per vendor.
		{"anthropic", "claude-sonnet-4-6"},
		{"openai", "gpt-4o"},
		{"gemini", "gemini-2.5-flash"},
		{"xai", "grok-3"},
		// Always-free-form providers.
		{"ollama", "llama3.1:8b"},
		{"nvidia-nim", "nvidia/nemotron-3-super-120b-a12b"},
		{"custom", "your-model-name"},
		{"unknown-future-provider", "model tag"}, // generic fallback
	}
	for _, tc := range tests {
		if got := FreeFormModelPlaceholder(tc.provider); got != tc.want {
			t.Errorf("FreeFormModelPlaceholder(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

// TestValidationDoneMsg_SetsNotice guards Pass 1 #7 — a successful
// validation lifts a "✓ key valid" banner into m.notice so the
// confirmation is visible even after the user has tabbed away from
// the key field. Failures don't set the banner (the per-field glyph
// already shouts).
func TestValidationDoneMsg_SetsNotice(t *testing.T) {
	anth := *FindCatalogEntry("anthropic")
	m := newWizardModel(context.Background(), Options{})
	m.inputs = []providerInputs{m.newProviderInputs(anth)}

	updated, _ := m.Update(validationDoneMsg{
		provider: "anthropic",
		result:   ValidationResult{Status: ValidationOK, Detail: "ok"},
	})
	wm := updated.(wizardModel)
	if !strings.Contains(wm.notice, "key valid") {
		t.Errorf("OK validation should set notice to include 'key valid'; got %q", wm.notice)
	}

	// Failure should not raise the success banner.
	wm.notice = ""
	updated, _ = wm.Update(validationDoneMsg{
		provider: "anthropic",
		result:   ValidationResult{Status: ValidationFailed, Detail: "401 Unauthorized"},
	})
	wm = updated.(wizardModel)
	if wm.notice != "" {
		t.Errorf("failed validation should not set success banner; got %q", wm.notice)
	}
}

// TestWizardFlow_SkipsRouterStep guards the decision to hide the
// router picker from first-run UX (multi-provider failover only fires
// on early failures — invisible in the happy path, so it adds friction
// for a feature most users won't see fire). Forward flow Active → must
// land on AutoMemory, and AutoMemory's goBack must return to Active —
// both bypassing stepRouter. The router engine + its config block
// stay; only the wizard step is hidden.
func TestWizardFlow_SkipsRouterStep(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	idx := catalogIndex("anthropic")
	m.inputs[idx].chosenModel = "claude-sonnet-4-6"
	m.inputs[idx].configured = true
	m.step = stepActive

	// Forward: Active → Review (skips Router).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	wm := updated.(wizardModel)
	if wm.step != stepReview {
		t.Errorf("Active+enter should advance to stepReview; got %v", wm.step)
	}

	// Backward: Review → Active (skips Router).
	back := wm.goBack()
	if back.step != stepActive {
		t.Errorf("goBack from stepReview should return to stepActive; got %v", back.step)
	}
}

// TestWizardFlow_TotalStepsIsFour locks in the user-facing step count
// after hiding the router and removing auto-memory.
func TestWizardFlow_TotalStepsIsFour(t *testing.T) {
	if totalSteps != 4 {
		t.Errorf("totalSteps = %d, want 4 (router hidden, auto-memory removed)", totalSteps)
	}
	if got := stepNumber(stepRouter); got != 0 {
		t.Errorf("stepNumber(stepRouter) = %d, want 0 (hidden from header)", got)
	}
	if got := stepNumber(stepActive); got != 4 {
		t.Errorf("stepNumber(stepActive) = %d, want 4", got)
	}
}

// stripANSI removes ANSI escape sequences so tests can assert on
// substrings without escape codes inflating len(). Mirrors
// lipglossVisibleWidth's stripping logic but returns the cleaned
// string instead of just its length.
func stripANSI(s string) string {
	var b []rune
	in := []rune(s)
	for i := 0; i < len(in); i++ {
		if in[i] == 0x1b && i+1 < len(in) && in[i+1] == '[' {
			for i+1 < len(in) {
				i++
				r := in[i]
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
					break
				}
			}
			continue
		}
		b = append(b, in[i])
	}
	return string(b)
}

// TestFocusActiveConfigField_KeyAndBaseURL covers the fields that
// already worked, just so a future refactor of the switch can't
// accidentally regress them while fixing the model case.
func TestFocusActiveConfigField_KeyAndBaseURL(t *testing.T) {
	custom := *FindCatalogEntry("custom")
	m := newWizardModel(context.Background(), Options{})
	m.inputs = []providerInputs{m.newProviderInputs(custom)}
	m.configIdx = 0

	// field 0 = key (custom has APIKeyEnv = "" actually — it's
	// user-entered, but envHas is false too, so fieldKind goes
	// straight to "model" via the empty-APIKeyEnv branch). For a
	// concrete key-field test, use anthropic.
	anth := *FindCatalogEntry("anthropic")
	m.inputs = []providerInputs{m.newProviderInputs(anth)}
	m.inputs[0].field = 0

	cmd := m.focusActiveConfigField()
	if cmd == nil {
		t.Fatalf("expected focus cmd for key field; got nil")
	}
	if !m.inputs[0].key.Focused() {
		t.Errorf("key should be focused on field=0 with non-empty APIKeyEnv")
	}

	// Now swap to custom, field=1 → baseURL.
	m.inputs = []providerInputs{m.newProviderInputs(custom)}
	m.inputs[0].field = 1
	cmd = m.focusActiveConfigField()
	if cmd == nil {
		t.Fatalf("expected focus cmd for baseURL field; got nil")
	}
	if !m.inputs[0].baseURL.Focused() {
		t.Errorf("baseURL should be focused for custom on field=1")
	}
}

func TestPickedOpenAIAuth(t *testing.T) {
	if pickedOpenAIAuth(nil) {
		t.Errorf("nil choices should be false")
	}
	if pickedOpenAIAuth([][2]string{{"anthropic", "claude-sonnet-4-6"}}) {
		t.Errorf("anthropic-only choices should be false")
	}
	if !pickedOpenAIAuth([][2]string{{"openai", "gpt-4o"}, {"openai-auth", "gpt-5.5"}}) {
		t.Errorf("openai-auth in mix should be true")
	}
}

// writeDoneMsg routes to stepOpenAIAuthLogin when openai-auth was
// picked and the write succeeded. The tea.Quit that used to fire
// here is now deferred until after the inline OAuth flow finishes;
// otherwise the user lands in a half-configured state with no token.
func TestWriteDoneRoutesThroughOpenAIAuthLogin(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	// Mark openai-auth as configured so activeChoices includes it.
	for i := range m.inputs {
		if m.inputs[i].entry.Name == "openai-auth" {
			m.inputs[i].configured = true
			m.inputs[i].chosenModel = "gpt-5.5"
		}
	}
	// Sanity: activeChoices should now include openai-auth.
	choices := m.activeChoices()
	if !pickedOpenAIAuth(choices) {
		t.Fatalf("test setup: openai-auth not in active choices: %+v", choices)
	}

	updated, cmd := m.Update(writeDoneMsg{outcome: WriteOutcome{}, err: nil})
	wm := updated.(wizardModel)
	if wm.step != stepOpenAIAuthLogin {
		t.Errorf("step = %v, want stepOpenAIAuthLogin", wm.step)
	}
	if cmd == nil {
		t.Errorf("expected a tea.Cmd to drive StartLogin; got nil")
	}
}

// writeDoneMsg keeps the legacy fast-path when openai-auth wasn't
// picked — straight to stepDone + tea.Quit.
func TestWriteDoneSkipsOpenAIAuthStepWhenNotPicked(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	for i := range m.inputs {
		if m.inputs[i].entry.Name == "anthropic" {
			m.inputs[i].configured = true
			m.inputs[i].chosenModel = "claude-sonnet-4-6"
		}
	}
	updated, _ := m.Update(writeDoneMsg{outcome: WriteOutcome{}, err: nil})
	wm := updated.(wizardModel)
	if wm.step != stepDone {
		t.Errorf("step = %v, want stepDone", wm.step)
	}
}

// A failed write skips the OAuth detour entirely — there's nothing
// useful to log into when the config didn't even land.
func TestWriteDoneSkipsOpenAIAuthOnError(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	for i := range m.inputs {
		if m.inputs[i].entry.Name == "openai-auth" {
			m.inputs[i].configured = true
			m.inputs[i].chosenModel = "gpt-5.5"
		}
	}
	updated, _ := m.Update(writeDoneMsg{err: errors.New("disk full")})
	wm := updated.(wizardModel)
	if wm.step != stepDone {
		t.Errorf("step = %v, want stepDone (write error short-circuits OAuth)", wm.step)
	}
}

// openAIAuthDoneMsg with a successful login transitions to the model
// scan step and dispatches the scan cmd.
func TestOpenAIAuthDoneRoutesThroughScan(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	updated, cmd := m.Update(openAIAuthDoneMsg{err: nil, accessToken: "tkn"})
	wm := updated.(wizardModel)
	if wm.step != stepOpenAIAuthScan {
		t.Errorf("step = %v, want stepOpenAIAuthScan", wm.step)
	}
	if wm.openAIAuthAccessToken != "tkn" {
		t.Errorf("openAIAuthAccessToken = %q, want %q", wm.openAIAuthAccessToken, "tkn")
	}
	if cmd == nil {
		t.Errorf("expected scan cmd; got nil")
	}
}

// A failed login skips the scan — no point probing without a token.
func TestOpenAIAuthDoneSkipsScanOnLoginError(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	updated, _ := m.Update(openAIAuthDoneMsg{err: errors.New("user cancelled")})
	wm := updated.(wizardModel)
	if wm.step != stepDone {
		t.Errorf("step = %v, want stepDone (login error short-circuits scan)", wm.step)
	}
}

// openAIAuthScanDoneMsg lands on stepDone and stores the discovered
// list. Subsequent renders surface "N models available".
func TestOpenAIAuthScanDoneSuccessLandsOnDone(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	updated, _ := m.Update(openAIAuthScanDoneMsg{models: []string{"gpt-5.5", "gpt-5.4"}})
	wm := updated.(wizardModel)
	if wm.step != stepDone {
		t.Errorf("step = %v, want stepDone", wm.step)
	}
	if len(wm.openAIAuthModels) != 2 {
		t.Errorf("openAIAuthModels = %v, want 2 entries", wm.openAIAuthModels)
	}
	if wm.openAIAuthScanErr != nil {
		t.Errorf("openAIAuthScanErr = %v, want nil", wm.openAIAuthScanErr)
	}
}

// openAIAuthScanDoneMsg with an error still lands on stepDone but
// records the error so the summary renders the warning + retry hint.
func TestOpenAIAuthScanDoneFailureLandsOnDoneWithErr(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	updated, _ := m.Update(openAIAuthScanDoneMsg{err: errors.New("backend down")})
	wm := updated.(wizardModel)
	if wm.step != stepDone {
		t.Errorf("step = %v, want stepDone", wm.step)
	}
	if wm.openAIAuthScanErr == nil {
		t.Errorf("openAIAuthScanErr should be set after a scan failure")
	}
}

// TestViewWelcome_DetectedBlock guards the consolidated "Detected"
// section on the welcome screen: a single header followed by aligned
// rows for ollama, existing config, and env keys. Replaces three
// disparate inline lines (✓ env, ✓ ollama, warn note) so users get one
// scannable status block instead of three.
func TestViewWelcome_DetectedBlock(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	m.width = 100
	m.ollamaProbe = OllamaProbeResult{Reachable: true, BaseURL: "http://localhost:11434", Models: []string{"llama3"}}
	m.existingCfg = true
	m.envSnap = EnvSnapshot{SuggestedProviders: []string{"anthropic", "openai"}}

	got := stripANSI(m.viewWelcome())

	if !strings.Contains(got, "Detected") {
		t.Errorf("welcome should render Detected header; got:\n%s", got)
	}
	if !strings.Contains(got, "ollama running") || !strings.Contains(got, "http://localhost:11434") || !strings.Contains(got, "1 model") {
		t.Errorf("welcome should render ollama row with URL + model count; got:\n%s", got)
	}
	if !strings.Contains(got, "existing config") || !strings.Contains(got, "tunables preserved") {
		t.Errorf("welcome should render existing config row; got:\n%s", got)
	}
	if !strings.Contains(got, "env keys") || !strings.Contains(got, "anthropic, openai") {
		t.Errorf("welcome should render env keys row; got:\n%s", got)
	}

	// Bullets switched to → arrows; the legacy • marker should be gone.
	if strings.Contains(got, "• enable") || strings.Contains(got, "• collect") {
		t.Errorf("welcome bullets should use → not •; got:\n%s", got)
	}
	if !strings.Contains(got, "→ enable") {
		t.Errorf("welcome bullets should render with → marker; got:\n%s", got)
	}
}

// TestViewWelcome_DetectedBlockSuppressedWhenEmpty asserts the
// Detected header doesn't appear on a fresh machine with no probe
// signals — otherwise the user sees an empty block under the header.
func TestViewWelcome_DetectedBlockSuppressedWhenEmpty(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	m.width = 100
	m.ollamaProbe = OllamaProbeResult{}
	m.existingCfg = false
	m.envSnap = EnvSnapshot{}

	got := stripANSI(m.viewWelcome())
	if strings.Contains(got, "Detected") {
		t.Errorf("Detected header should be suppressed when nothing is detected; got:\n%s", got)
	}
}

// TestOptionSelector_NoTriangleCursor guards the migration from the
// old "▸ label" cursor mark to the green-background highlight bar
// (matching the chat TUI's slash palette). The bar IS the cursor
// signal — leaving ▸ behind on any option list would be visually
// redundant. This test holds the line on every option-list view.
func TestOptionSelector_NoTriangleCursor(t *testing.T) {
	m := newWizardModel(context.Background(), Options{})
	m.width = 100

	// Seed enough state that every view has rows to render.
	anthIdx := catalogIndex("anthropic")
	m.inputs[anthIdx].chosenModel = "claude-sonnet-4-6"
	m.inputs[anthIdx].configured = true

	views := map[string]string{
		"providers": m.viewProviders(),
		"active":    m.viewActive(),
		"router":    m.viewRouter(),
	}
	for name, raw := range views {
		got := stripANSI(raw)
		if strings.Contains(got, "▸") {
			t.Errorf("%s: ▸ cursor should be replaced by the highlight bar; got:\n%s", name, got)
		}
	}
}
