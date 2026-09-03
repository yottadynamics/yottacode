package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/dotenv"
	"github.com/yottadynamics/yottacode/internal/providerops"
	"github.com/yottadynamics/yottacode/internal/wizard"
)

// providerPickerMode is the picker's current screen. The state
// machine starts in menuMode (List/Use/Remove/Add) and transitions
// into a sub-picker on selection. Esc returns to the previous level.
type providerPickerMode int

const (
	providerMenuMode providerPickerMode = iota
	providerUsePickerMode
	providerRemovePickerMode
	providerAddKindMode
	providerAddFieldsMode
)

// providerMenuItem describes one row of the top-level menu.
type providerMenuItem struct {
	Label    string
	Subtitle string
	Action   providerPickerMode // mode to transition into; menuMode = inline action
	Key      string             // tag for the inline-action dispatcher
}

// providerPickerState holds the in-flight Bubbletea overlay for
// /provider. Layered state machine: menu → action-specific picker.
// The picker stays open across mode transitions so Esc can pop
// back one level.
type providerPickerState struct {
	mode providerPickerMode

	// menuItems is the top-level menu. Built once at open time so
	// keyboard navigation never has to recompute.
	menuItems  []providerMenuItem
	menuCursor int

	// usePickerCursor is the highlighted row inside the use-picker
	// (also reused for remove-picker since they share the layout).
	// Reset to 0 each time the sub-picker is entered.
	usePickerCursor int

	// providers is a snapshot of cfg.Providers taken when the picker
	// opens; we don't re-read the config mid-picker so the index
	// stays stable while the user navigates.
	providers []config.Provider

	// activeProvider is the name of the currently-active profile,
	// used to render the ◉ marker in the Use list.
	activeProvider string

	// Add flow ----------------------------------------------------

	// addCatalog is the wizard.Catalog snapshot used by the
	// addKindMode list. We snapshot rather than re-walk on every
	// render so the indices stay stable.
	addCatalog []wizard.CatalogEntry

	// addKindCursor is the highlighted row inside addKindMode.
	addKindCursor int

	// addPicked is the catalog entry the user selected after the
	// kind step; stays around through addFieldsMode so the form
	// knows which kind it's saving.
	addPicked *wizard.CatalogEntry

	// addFields is the array of textinput.Model the form renders.
	// Layout depends on addPicked.FreeForm — see populateAddFields
	// for the field order. addFocused tracks which field has the
	// cursor.
	addFields  []textinput.Model
	addLabels  []string // human-readable label per field
	addFocused int

	// addInputErr carries the last form-input validation error
	// (missing name / base URL / default_model) so the user sees
	// what's wrong without having to dig through the transcript.
	// Live API health checks were removed — every provider's
	// /models endpoint authenticates differently and we were
	// either over-claiming verification or under-claiming it.
	// A bad key surfaces as a 401 from the first chat call now.
	addInputErr string

	// addCuratedModels is the embedded catalog snapshot for the
	// picked entry's kind, when that kind is curated
	// (anthropic/openai/gemini/xai) AND the catalog is non-empty. When
	// populated, the form renders a list under "Default model" and
	// Up/Down on that field navigates the list (mirrors the wizard's
	// stepConfigure picker). When empty, the form falls back to the
	// free-form textinput.
	addCuratedModels []catalog.Model

	// addModelCursor is the highlighted row inside addCuratedModels.
	// Only meaningful when addCuratedModels is non-empty.
	addModelCursor int

	// addVertexFamily stores the Gemini/Claude choice for the combined
	// Google Vertex AI catalog row. The picker writes a concrete provider
	// kind after this choice, keeping runtime adapters separate.
	addVertexFamily      wizard.VertexFamily
	addVertexModelCursor map[wizard.VertexFamily]int
	// addVertexModelText remembers manually typed model IDs per family so
	// switching Gemini ↔ Claude and back does not discard non-catalog tags.
	addVertexModelText map[wizard.VertexFamily]string
}

// openProviderPicker installs a fresh sub-menu picker on m. The
// optional initialMode argument lets callers (e.g. /provider list)
// open the picker directly into a sub-mode rather than the top-level
// menu — a shortcut for the common "I just want the list inline"
// case. Pass providerMenuMode (or omit) for the default behavior.
func (m *Model) openProviderPicker(initialMode ...providerPickerMode) {
	cfg := loadConfigForCommand(*m)
	st := &providerPickerState{
		mode: providerMenuMode,
		menuItems: []providerMenuItem{
			{Label: "Add", Subtitle: "configure a new provider", Action: providerAddKindMode},
			{Label: "Remove", Subtitle: "delete a configured provider", Action: providerRemovePickerMode},
			{Label: "Use", Subtitle: "browse providers and switch the active one", Action: providerUsePickerMode},
		},
		addCatalog:     append([]wizard.CatalogEntry(nil), wizard.Catalog...),
		providers:      append([]config.Provider(nil), cfg.Providers...),
		activeProvider: cfg.Active.Provider,
	}
	if st.activeProvider == "" {
		// Fall back to whichever profile matches the running base URL.
		st.activeProvider = profileForActiveBaseURL(cfg, m.baseURL)
	}
	if len(initialMode) > 0 && initialMode[0] != providerMenuMode {
		st.mode = initialMode[0]
		// Default the use-picker cursor to the active provider so
		// Enter just confirms, mirroring the menu→sub-picker path.
		if st.mode == providerUsePickerMode {
			for i, prov := range st.providers {
				if prov.Name == st.activeProvider {
					st.usePickerCursor = i
					break
				}
			}
		}
	}
	m.providerPicker = st
	m.providerPickerOpen = true
}

// updateProviderPicker handles keystrokes while the provider picker
// is foreground. Returns the new model + any cmd to run (e.g. a
// provider probe after a Use selection).
func (m Model) updateProviderPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.providerPicker == nil {
		m.providerPickerOpen = false
		return m, nil
	}
	p := m.providerPicker
	if msg.String() == "shift+tab" {
		if p.mode == providerAddFieldsMode {
			cycleAddFocus(p, -1)
		}
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEsc:
		// Esc from a sub-picker pops back to the menu; Esc from the
		// menu closes outright. Add-fields pops back to add-kind.
		switch p.mode {
		case providerMenuMode:
			m.providerPickerOpen = false
			m.providerPicker = nil
			m.openSlashPalette()
			return m, nil
		case providerAddFieldsMode:
			p.mode = providerAddKindMode
			return m, nil
		default:
			p.mode = providerMenuMode
			return m, nil
		}
	case tea.KeyUp:
		switch p.mode {
		case providerMenuMode:
			if p.menuCursor > 0 {
				p.menuCursor--
			}
		case providerUsePickerMode, providerRemovePickerMode:
			if p.usePickerCursor > 0 {
				p.usePickerCursor--
			}
		case providerAddKindMode:
			if p.addKindCursor > 0 {
				p.addKindCursor--
			}
		case providerAddFieldsMode:
			if p.familyPickerActive() {
				p.setVertexFamily(wizard.VertexFamilyGemini)
			} else if p.curatedPickerActive() && p.addModelCursor > 0 {
				p.rememberVertexModelCursor()
				p.addModelCursor--
				p.syncModelTextinputToCursor()
			}
		}
		return m, nil
	case tea.KeyDown:
		switch p.mode {
		case providerMenuMode:
			if p.menuCursor < len(p.menuItems)-1 {
				p.menuCursor++
			}
		case providerUsePickerMode, providerRemovePickerMode:
			if p.usePickerCursor < len(p.providers)-1 {
				p.usePickerCursor++
			}
		case providerAddKindMode:
			if p.addKindCursor < len(p.addCatalog)-1 {
				p.addKindCursor++
			}
		case providerAddFieldsMode:
			if p.familyPickerActive() {
				p.setVertexFamily(wizard.VertexFamilyClaude)
			} else if p.curatedPickerActive() && p.addModelCursor < len(p.addCuratedModels)-1 {
				p.rememberVertexModelCursor()
				p.addModelCursor++
				p.syncModelTextinputToCursor()
			}
		}
		return m, nil
	case tea.KeyLeft:
		if p.mode == providerAddFieldsMode && p.familyPickerActive() {
			p.setVertexFamily(wizard.VertexFamilyGemini)
			return m, nil
		}
	case tea.KeyRight:
		if p.mode == providerAddFieldsMode && p.familyPickerActive() {
			p.setVertexFamily(wizard.VertexFamilyClaude)
			return m, nil
		}
	case tea.KeyTab:
		if p.mode == providerAddFieldsMode {
			cycleAddFocus(p, 1)
		}
		return m, nil
	case tea.KeyEnter:
		switch p.mode {
		case providerMenuMode:
			return m.dispatchProviderMenu()
		case providerUsePickerMode:
			return m.commitProviderUse()
		case providerRemovePickerMode:
			return m.commitProviderRemove()
		case providerAddKindMode:
			return m.enterAddFields()
		case providerAddFieldsMode:
			return m.commitProviderAdd()
		}
	}
	// In the form, forward keystrokes to the focused textinput so
	// typing actually fills the fields.
	if p.mode == providerAddFieldsMode && p.addFocused < len(p.addFields) {
		if p.familyPickerActive() {
			return m, nil
		}
		var cmd tea.Cmd
		p.addFields[p.addFocused], cmd = p.addFields[p.addFocused].Update(msg)
		return m, cmd
	}
	return m, nil
}

// updateProviderPickerPaste forwards a bracketed paste to the focused
// add-fields textinput, mirroring the tail of updateProviderPicker
// above. Paste carries no navigation semantics (no Tab/Esc/Enter to
// intercept), so unlike the keypress path this is a direct handoff.
func updateProviderPickerPaste(m Model, msg tea.PasteMsg) (Model, tea.Cmd) {
	p := m.providerPicker
	if p == nil || p.mode != providerAddFieldsMode || p.addFocused >= len(p.addFields) || p.familyPickerActive() {
		return m, nil
	}
	var cmd tea.Cmd
	p.addFields[p.addFocused], cmd = p.addFields[p.addFocused].Update(msg)
	return m, cmd
}

// curatedPickerActive reports whether the form is currently sitting
// on the "Default model" field AND a non-empty curated model list is
// loaded. Up/Down should drive the catalog cursor in that combination
// only — on every other field we want the textinput to keep its
// (current) ignore-arrow-keys behavior.
func (p *providerPickerState) curatedPickerActive() bool {
	if len(p.addCuratedModels) == 0 || p.addFocused >= len(p.addLabels) {
		return false
	}
	return p.addLabels[p.addFocused] == "Default model"
}

// familyPickerActive reports whether the focused form row is the Vertex
// family dropdown. It must not key off addPicked.Name because choosing a
// family rewrites addPicked to vertex-gemini or vertex-claude.
func (p *providerPickerState) familyPickerActive() bool {
	if p.addFocused >= len(p.addLabels) {
		return false
	}
	return p.addLabels[p.addFocused] == "Model family"
}

func (p *providerPickerState) rememberVertexModelCursor() {
	if p.addVertexModelCursor == nil {
		return
	}
	if p.addModelCursor >= 0 {
		p.addVertexModelCursor[p.addVertexFamily] = p.addModelCursor
	}
	if p.addVertexModelText != nil {
		for i, label := range p.addLabels {
			if label == "Default model" {
				p.addVertexModelText[p.addVertexFamily] = strings.TrimSpace(p.addFields[i].Value())
				return
			}
		}
	}
}

func (p *providerPickerState) setVertexFamily(f wizard.VertexFamily) {
	p.rememberVertexModelCursor()
	p.addVertexFamily = f
	entry := f.CatalogEntry()
	if p.addPicked != nil {
		*p.addPicked = entry
	}
	for i, label := range p.addLabels {
		switch label {
		case "Model family":
			p.addFields[i].SetValue(vertexFamilyDropdownValue(f))
		case "Name":
			v := strings.TrimSpace(p.addFields[i].Value())
			if v == "" || v == "google-vertex" || v == "vertex-gemini" || v == "vertex-claude" {
				p.addFields[i].SetValue(uniqueProviderName(entry.Name))
			}
		case "GCP project":
			project := strings.TrimSpace(p.addFields[i].Value())
			p.addFields[i].SetValue(project)
		case "Base URL":
			p.addFields[i].SetValue(entry.BaseURL)
		case "Default model":
			p.addFields[i].SetValue("")
		}
	}
	p.addCuratedModels = catalog.Curated(entry.Kind)
	p.addModelCursor = -1
	if p.addVertexModelCursor != nil {
		if saved, ok := p.addVertexModelCursor[f]; ok && saved >= 0 && saved < len(p.addCuratedModels) {
			p.addModelCursor = saved
		}
	}
	if p.addModelCursor >= 0 {
		if p.addVertexModelText != nil {
			if saved := p.addVertexModelText[f]; saved != "" {
				for i, label := range p.addLabels {
					if label == "Default model" {
						p.addFields[i].SetValue(saved)
						return
					}
				}
			}
		}
		p.syncModelTextinputToCursor()
	} else if p.addVertexModelText != nil {
		if saved := p.addVertexModelText[f]; saved != "" {
			for i, label := range p.addLabels {
				if label == "Default model" {
					p.addFields[i].SetValue(saved)
					break
				}
			}
		}
	}
}

// syncModelTextinputToCursor copies the cursor's catalog ID into the
// "Default model" textinput so the existing values-by-label
// persistence (commitProviderAdd) reads the picked value without
// branching on whether the user typed it or arrowed to it.
func (p *providerPickerState) syncModelTextinputToCursor() {
	if p.addModelCursor < 0 || p.addModelCursor >= len(p.addCuratedModels) {
		return
	}
	for i, label := range p.addLabels {
		if label == "Default model" {
			p.addFields[i].SetValue(p.addCuratedModels[p.addModelCursor].ID)
			return
		}
	}
}

// cycleAddFocus moves focus among the textinput fields, blurring
// the previous one and focusing the new. delta = +1 for Tab, -1 for
// Shift+Tab.
func cycleAddFocus(p *providerPickerState, delta int) {
	if len(p.addFields) == 0 {
		return
	}
	p.addFields[p.addFocused].Blur()
	p.addFocused = (p.addFocused + delta + len(p.addFields)) % len(p.addFields)
	p.addFields[p.addFocused].Focus()
}

// dispatchProviderMenu reacts to Enter on the top-level menu and
// transitions the picker to the corresponding sub-screen (Use / Add
// / Remove). The standalone "List" entry was retired — Use already
// shows the configured-providers list AND lets the user switch the
// active one in a single screen.
func (m Model) dispatchProviderMenu() (Model, tea.Cmd) {
	p := m.providerPicker
	if p == nil || p.menuCursor >= len(p.menuItems) {
		return m, nil
	}
	item := p.menuItems[p.menuCursor]
	// Sub-mode transition. Some sub-modes need an empty-state hint
	// when the operation isn't actionable.
	if (item.Action == providerUsePickerMode || item.Action == providerRemovePickerMode) && len(p.providers) == 0 {
		m.providerPickerOpen = false
		m.providerPicker = nil
		m.appendLine(styleAuto.Render("no providers configured — try /provider add"))
		return m, nil
	}
	p.mode = item.Action
	p.usePickerCursor = 0
	// Use sub-picker: cursor defaults to the active provider so
	// Enter-to-confirm works without arrowing.
	if item.Action == providerUsePickerMode {
		for i, prov := range p.providers {
			if prov.Name == p.activeProvider {
				p.usePickerCursor = i
				break
			}
		}
	}
	// Remove sub-picker: leave cursor at 0. Defaulting to the active
	// provider would be a small foot-gun (Enter immediately blows
	// away the running profile) — start at the top of the list and
	// make the user navigate.
	if item.Action == providerAddKindMode {
		p.addKindCursor = 0
		p.addPicked = nil
		p.addFields = nil
	}
	return m, nil
}

// enterAddFields transitions from kind selection into the form
// stage. Captures the catalog entry, populates the textinputs with
// per-kind defaults, and focuses the first editable field.
func (m Model) enterAddFields() (Model, tea.Cmd) {
	p := m.providerPicker
	if p == nil || p.addKindCursor >= len(p.addCatalog) {
		return m, nil
	}
	picked := p.addCatalog[p.addKindCursor]
	p.addPicked = &picked
	populateAddFields(p, picked, providerAddInputWidth(m.width))
	p.mode = providerAddFieldsMode
	if len(p.addFields) > 0 {
		p.addFields[0].Focus()
	}
	return m, nil
}

// populateAddFields lays out the form for a given catalog entry.
// Cloud entries (FreeForm=false) only need the API key — name and
// base URL are pre-filled and uneditable in the form (we still
// display them for confirmation). Free-form entries need the user
// to fill in name + base URL + default model + (optional) key.
//
// Field order is fixed across kinds so muscle memory carries: Name,
// Base URL, API key, Default model. Disabled fields render as
// blurred textinputs so the user can still see the value but Tab
// skips them.
func populateAddFields(p *providerPickerState, e wizard.CatalogEntry, inputWidth int) {
	p.addFields = nil
	p.addLabels = nil
	p.addFocused = 0
	p.addCuratedModels = nil
	// -1 = "user has not picked yet" — the renderer shows no row
	// highlight and commitProviderAdd's "default model is required"
	// guard fires until the user arrows down (or types) to pick.
	// Save with the auto-prefilled first model is a footgun: users
	// were registering providers without ever seeing the model list.
	p.addModelCursor = -1

	// Name — pre-filled with the catalog name; always editable so
	// users can declare e.g. "openai-personal" alongside "openai".
	name := textinput.New()
	name.Placeholder = "provider profile name"
	name.SetValue(uniqueProviderName(e.Name))
	name.Prompt = ""
	name.CharLimit = 64
	name.SetWidth(inputWidth)
	p.addFields = append(p.addFields, name)
	p.addLabels = append(p.addLabels, "Name")

	if e.Name == "google-vertex" {
		family := textinput.New()
		family.Prompt = ""
		family.SetValue(vertexFamilyDropdownValue(wizard.VertexFamilyGemini))
		family.SetWidth(inputWidth)
		p.addFields = append(p.addFields, family)
		p.addLabels = append(p.addLabels, "Model family")
		p.addVertexFamily = wizard.VertexFamilyGemini
		p.addVertexModelCursor = map[wizard.VertexFamily]int{}
		p.addVertexModelText = map[wizard.VertexFamily]string{}

		project := textinput.New()
		project.Prompt = ""
		project.Placeholder = "your-gcp-project-id"
		project.CharLimit = 128
		project.SetWidth(inputWidth)
		p.addFields = append(p.addFields, project)
		p.addLabels = append(p.addLabels, "GCP project")

		e = p.addVertexFamily.CatalogEntry()
		if p.addPicked != nil {
			*p.addPicked = e
		}
	}

	// Base URL — pre-fill from the catalog when present (cloud
	// providers always have one; free-form catalog entries like
	// NVIDIA NIM also ship with a known integrate.api URL). The
	// field stays editable so users on a corporate proxy or a
	// self-hosted endpoint can override. The "custom" catch-all
	// is the only entry with an empty catalog URL — that one gets
	// a placeholder hint instead.
	if e.Kind != "vertex" && e.Kind != "vertex-anthropic" {
		baseURL := textinput.New()
		if strings.TrimSpace(e.BaseURL) != "" {
			baseURL.SetValue(e.BaseURL)
		} else {
			baseURL.Placeholder = "https://your-endpoint.example/v1"
		}
		baseURL.Prompt = ""
		baseURL.CharLimit = 200
		baseURL.SetWidth(inputWidth)
		p.addFields = append(p.addFields, baseURL)
		p.addLabels = append(p.addLabels, "Base URL")
	}

	// API key — for Ollama (FreeForm + no APIKeyEnv) we omit the
	// field entirely. For everything else, we collect it; it's
	// written to .env, never to config.toml. Width is bounded so
	// long keys (Anthropic's are ~100 chars) don't push the cursor
	// off the right edge of the inline overlay; the textinput's
	// own horizontal scroll keeps the rest visible. Placeholder is
	// kept short — the .env destination is already in the trailing
	// "(API key written to ~/.yottacode/.env as …)" hint, so we
	// don't repeat it on the input row.
	if e.APIKeyEnv != "" {
		key := textinput.New()
		key.Placeholder = "paste your API key"
		key.Prompt = ""
		key.CharLimit = 256
		key.EchoMode = textinput.EchoPassword
		key.EchoCharacter = '•'
		key.SetWidth(inputWidth)
		p.addFields = append(p.addFields, key)
		p.addLabels = append(p.addLabels, "API key")
	}

	// Default model. For curated kinds (anthropic/openai/gemini/xai)
	// we load the curated catalog (embedded catalog.gen.json, plus the
	// local models.dev snapshot for Gemini) and let the user pick a
	// model with Up/Down — same UX the wizard's stepConfigure offers,
	// so a user adding a provider mid-session doesn't have to remember
	// the current model tag. The textinput is still kept (pre-filled
	// with the catalog default and editable) so the user can still
	// type in a non-catalog tag — useful when our list lags a vendor
	// release. When the catalog is empty for a curated kind (anything
	// pre-refresh) we fall back to the free-form placeholder; the
	// renderer surfaces a "(catalog empty — run
	// `go run ./cmd/yotta-models refresh`)" hint, matching the
	// wizard's wording so the operator knows the fix.
	model := textinput.New()
	model.Prompt = ""
	model.CharLimit = 128
	model.SetWidth(inputWidth)
	// openai-auth's model list is per-user and only populated by the
	// post-login scan, so the catalog is intentionally empty here.
	// Skip Get() so the renderer falls into the free-form branch and
	// shows the "discovered after sign-in" hint; pre-fill gpt-5.5 (the
	// universal default) so the user can save without typing.
	//
	// nvidia-nim deliberately does NOT pre-fill the model field. NIM
	// API keys minted at build.nvidia.com are commonly scoped to a
	// single model — silently auto-accepting our suggested default
	// would steer users into a 404 when their key wasn't minted for
	// nemotron-3-super. The suggestion is still visible via the
	// FreeFormModelPlaceholder fallback below, so the user knows what
	// to type, but the empty-model guard in commitProviderAdd forces
	// an explicit pick.
	switch {
	case e.Kind == "openai-auth":
		model.SetValue("gpt-5.5")
	case e.Kind == "copilot":
		model.SetValue("claude-haiku-4.5")
	case e.Kind == "xai":
		// xAI has a stable hosted default we can safely pre-fill in the TUI
		// Add form. Keep the curated catalog loaded too, when present, so the
		// user can still arrow-pick another Grok model instead of typing it.
		model.SetValue("grok-4.20-0309-non-reasoning")
		p.addCuratedModels = catalog.Get(e.Kind)
	case catalog.IsCuratedKind(e.Kind):
		p.addCuratedModels = catalog.Curated(e.Kind)
	}
	// xAI has a stable default; other curated cloud providers still start
	// empty so the user deliberately chooses from the embedded list.
	if model.Value() == "" {
		model.Placeholder = wizard.FreeFormModelPlaceholder(e.Name)
	}
	p.addFields = append(p.addFields, model)
	p.addLabels = append(p.addLabels, "Default model")
}

// providerAddInputWidth is the column budget the form's textinputs
// claim, given the terminal's outer width. Mirrors the wizard's
// inputWidthFor (internal/wizard/steps.go) but tuned for the inline
// picker chrome: the form sits under a 2-char "❯ "/"  " marker plus
// a 14-char label column ("Default model: "), so subtract ~18 from
// the terminal width and clamp to a sane range. Without this, the
// API-key input (256-char limit) and a long Anthropic key spill past
// the right edge of the overlay.
func providerAddInputWidth(termWidth int) int {
	const min = 24
	const max = 70
	if termWidth <= 0 {
		return 48
	}
	w := termWidth - 22
	if w < min {
		return min
	}
	if w > max {
		return max
	}
	return w
}

// uniqueProviderName returns a profile name unused by the existing
// config. Picks "<base>" first, then "<base>-2", "<base>-3", etc.
// Lets the user type "/provider add" twice for the same provider
// without having to remove the first.
func uniqueProviderName(base string) string {
	cfg, err := config.LoadDefault()
	if err != nil || cfg.FindProvider(base) == nil {
		return base
	}
	for i := 2; i < 100; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if cfg.FindProvider(candidate) == nil {
			return candidate
		}
	}
	return base
}

// commitProviderAdd is the Enter-on-form handler. Reads the form
// values, runs the synchronous input checks (name + base URL +
// default model required), and persists. There's no live key/model
// health check — the provider's /models endpoint authenticates
// inconsistently across vendors (NVIDIA NIM accepts any bearer
// token there), so a "verified ✓" message would over-promise. A
// real auth issue surfaces as 401 from the first chat call, which
// is unambiguous.
func (m Model) commitProviderAdd() (Model, tea.Cmd) {
	p := m.providerPicker
	if p == nil || p.addPicked == nil || len(p.addFields) == 0 {
		return m, nil
	}
	picked := *p.addPicked
	values := make(map[string]string, len(p.addFields))
	for i, label := range p.addLabels {
		values[strings.ToLower(label)] = strings.TrimSpace(p.addFields[i].Value())
	}

	name := values["name"]
	baseURL := values["base url"]
	if picked.Kind == "vertex" || picked.Kind == "vertex-anthropic" {
		project := strings.TrimSpace(values["gcp project"])
		if project == "" {
			p.addInputErr = "GCP project ID is required"
			return m, nil
		}
		baseURL = wizard.VertexBaseURL(p.addVertexFamily, project)
	}
	apiKey := values["api key"]
	defaultModel := values["default model"]
	if len(p.addCuratedModels) > 0 && defaultModel == "" {
		p.addInputErr = "default model is required"
		for i, label := range p.addLabels {
			if label == "Default model" {
				if p.addFocused < len(p.addFields) {
					p.addFields[p.addFocused].Blur()
				}
				p.addFocused = i
				p.addFields[i].Focus()
				break
			}
		}
		return m, nil
	}

	if name == "" {
		p.addInputErr = "name is required"
		return m, nil
	}
	if baseURL == "" && picked.Kind != "vertex" && picked.Kind != "vertex-anthropic" {
		p.addInputErr = "base URL is required"
		return m, nil
	}
	if defaultModel == "" {
		p.addInputErr = "default model is required"
		return m, nil
	}
	// Resolve the env var this profile will save its key under. The
	// catalog ships one default per vendor (NVIDIA_API_KEY,
	// XAI_API_KEY, …) but vendors like NVIDIA NIM mint per-model API
	// keys, so a second profile sharing one env var means switching
	// between models in the picker fails with 401. SuggestAPIKeyEnv
	// keeps the well-known default for the first profile of a vendor
	// and mints "<NAME>_API_KEY" (e.g. NVIDIA_NIM_2_API_KEY) for
	// subsequent profiles. Each profile then carries its own bearer
	// in ~/.yottacode/.env, indexed by [[providers]].api_key_env.
	cfg := loadConfigForCommand(m)
	effectiveEnv := providerops.SuggestAPIKeyEnv(cfg, name, picked.APIKeyEnv)

	// API-key presence check. Refuses to save when the effective env
	// var is empty AND the form field is empty. The check looks at
	// the *effective* env var, not the catalog default — for a
	// second NVIDIA NIM profile we ask whether NVIDIA_NIM_2_API_KEY
	// is set, not whether the (already-taken) NVIDIA_API_KEY is.
	// Without this, the user could dismiss the picker thinking they
	// added a working profile, then the first chat call hits a 401
	// because the new profile is silently sharing the first
	// profile's key. Ollama / openai-auth (effectiveEnv == "") are
	// exempt by definition.
	if effectiveEnv != "" && apiKey == "" && strings.TrimSpace(os.Getenv(effectiveEnv)) == "" {
		p.addInputErr = fmt.Sprintf("API key required (paste it above — saves as %s)", effectiveEnv)
		// Drop focus on the API-key field so typing immediately
		// fills the right input.
		for i, label := range p.addLabels {
			if label == "API key" {
				if p.addFocused < len(p.addFields) {
					p.addFields[p.addFocused].Blur()
				}
				p.addFocused = i
				p.addFields[i].Focus()
				break
			}
		}
		return m, nil
	}
	p.addInputErr = ""
	return m.persistProviderAdd(picked, name, baseURL, apiKey, defaultModel, effectiveEnv)
}

// persistProviderAdd is the save path. For non-openai-auth, writes
// config.toml + .env now and rebuilds the adapter. For openai-auth,
// stashes the AddProvider on the Model and kicks off the OAuth flow
// instead — the on-disk write is deferred to handleInlineOpenAIAuthDone's
// success branch so a failed sign-in never leaves a broken profile in
// config.toml. effectiveEnv is the env var name this profile is saving
// under (catalog default for the first profile of a vendor, otherwise
// a per-profile name minted by SuggestAPIKeyEnv).
func (m Model) persistProviderAdd(picked wizard.CatalogEntry, name, baseURL, apiKey, defaultModel, effectiveEnv string) (Model, tea.Cmd) {
	add := providerops.AddProvider{
		Name:         name,
		Kind:         picked.Kind,
		BaseURL:      baseURL,
		APIKeyEnv:    effectiveEnv,
		DefaultModel: defaultModel,
	}
	if picked.Kind == "openai-auth" {
		// Defer the on-disk write until OAuth completes successfully.
		// The user sees "starting browser sign-in" but nothing has
		// touched config.toml yet — handleInlineOpenAIAuthDone replays
		// the persist on success or drops the stash on failure.
		cfg := loadConfigForCommand(m)
		if cfg.FindProvider(add.Name) != nil {
			m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("provider %q already exists; remove it first", add.Name))))
			return m, nil
		}
		m.providerPickerOpen = false
		m.providerPicker = nil
		m.openAIAuthPendingAdd = &pendingOpenAIAuthAdd{
			add:           add,
			becomesActive: cfg.Active.Provider == "",
			fromPicker:    true,
		}
		m.appendLine(styleAuto.Render(statusActionLine("openai-auth", "starting browser sign-in…")))
		m.appendLine(styleAuto.Render(statusHintLine(fmt.Sprintf("profile %q will be saved after sign-in", add.Name))))
		// Reclaim the loopback port from any sign-in the user abandoned
		// (e.g. closed the browser) so this retry can bind.
		m.closePendingOpenAIAuthLogin()
		return m, startInlineOpenAIAuthLoginCmd(m.parentCtx)
	}
	if picked.Kind == "copilot" {
		cfg := loadConfigForCommand(m)
		if cfg.FindProvider(add.Name) != nil {
			m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("provider %q already exists; remove it first", add.Name))))
			return m, nil
		}
		m.providerPickerOpen = false
		m.providerPicker = nil
		m.copilotPendingAdd = &pendingCopilotAdd{
			add:           add,
			becomesActive: cfg.Active.Provider == "",
			fromPicker:    true,
		}
		m.appendLine(styleAuto.Render(statusActionLine("copilot", "starting device code sign-in…")))
		m.appendLine(styleAuto.Render(statusHintLine(fmt.Sprintf("profile %q will be saved after sign-in", add.Name))))
		return m, startInlineCopilotAuthCmd(m.parentCtx)
	}
	updated, becameActive, ok := commitProviderAddNow(&m, add, apiKey, picked)
	if !ok {
		return m, nil
	}
	if becameActive {
		return providerUse(m, add.Name)
	}
	_ = updated // kept for tests that snapshot the post-write cfg
	return m, nil
}

// commitProviderAddNow runs the synchronous on-disk write +
// transcript log lines for a non-deferred add. Shared between the
// picker path (persistProviderAdd) and the OAuth-success path
// (finalizePendingOpenAIAuthAdd) so the success transcript looks
// the same regardless of how we got there. Returns (updated cfg,
// becameActive, ok). ok is false when validation or write fails;
// the caller has already had the error logged via m.appendLine.
func commitProviderAddNow(m *Model, add providerops.AddProvider, apiKey string, picked wizard.CatalogEntry) (config.Config, bool, bool) {
	cfg := loadConfigForCommand(*m)
	updated, err := providerops.Add(cfg, add)
	if err != nil {
		m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("%v", err))))
		return cfg, false, false
	}
	// Always switch to the newly added provider — if you're adding one,
	// you want to use it. Also covers the first-add case where no
	// active was set yet.
	if next, err := providerops.SetActive(updated, add.Name); err == nil {
		updated = next
	}
	becameActive := true
	if err := config.Validate(updated); err != nil {
		m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("config invalid: %v", err))))
		return cfg, false, false
	}
	if err := writeConfig(updated); err != nil {
		m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("write config: %v", err))))
		return cfg, false, false
	}
	if apiKey != "" && add.APIKeyEnv != "" {
		// Propagate to the running process FIRST so the key takes
		// effect this session — `/provider use NAME` (and the
		// adapter probe right after) reads from os.Getenv.
		_ = os.Setenv(add.APIKeyEnv, apiKey)
		if err := writeAPIKeyToEnv(add.APIKeyEnv, apiKey); err != nil {
			m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("config saved, but writing .env failed: %v", err))))
		}
	}
	if err := ensureGitignoreCoversDotEnv(m.cwd); err != nil {
		m.appendLine(styleAuto.Render(fmt.Sprintf("(gitignore note: %v)", err)))
	}

	m.providerPickerOpen = false
	m.providerPicker = nil
	// Render the catalog identity (e.g. "nvidia-nim") instead of the
	// generic kind ("openai-compatible") when we know the profile
	// came from a catalog entry.
	identity := picked.Name
	if identity == "" {
		identity = picked.Kind
	}
	m.appendLine(styleAuto.Render(statusOKLine("provider", fmt.Sprintf("added %q (%s)", add.Name, identity))))
	if cfgPath, err := config.DefaultPath(); err == nil {
		m.appendLine(styleAuto.Render(statusHintLine("config saved to " + displayPath(cfgPath, m.cwd))))
	}
	if apiKey != "" && add.APIKeyEnv != "" {
		if home, err := os.UserHomeDir(); err == nil {
			m.appendLine(styleAuto.Render(statusHintLine(fmt.Sprintf("API key saved to %s as %s",
				displayPath(filepath.Join(home, ".yottacode", ".env"), m.cwd), add.APIKeyEnv))))
		}
	}
	if add.APIKeyEnv != "" && apiKey == "" {
		m.appendLine(styleAuto.Render(statusHintLine(fmt.Sprintf("using existing %s from environment", add.APIKeyEnv))))
	}
	return updated, becameActive, true
}

// writeAPIKeyToEnv merges one KEY=value into ~/.yottacode/.env at
// 0600. Thin wrapper over dotenv.SetKey kept for the picker's
// existing call sites — the actual merge-and-write logic lives in
// internal/dotenv now that three callers need it.
func writeAPIKeyToEnv(name, value string) error {
	path, err := dotenv.DefaultPath()
	if err != nil {
		return err
	}
	return dotenv.SetKey(path, name, value)
}

// commitProviderUse persists the user's choice through providerops,
// switches the running adapter + memory, closes the picker.
func (m Model) commitProviderUse() (Model, tea.Cmd) {
	p := m.providerPicker
	if p == nil || p.usePickerCursor >= len(p.providers) {
		return m, nil
	}
	chosen := p.providers[p.usePickerCursor]
	cfg := loadConfigForCommand(m)
	updated, err := providerops.SetActive(cfg, chosen.Name)
	if err != nil {
		m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("%v", err))))
		return m, nil
	}
	if err := config.Validate(updated); err != nil {
		m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("config invalid: %v", err))))
		return m, nil
	}
	if err := writeConfig(updated); err != nil {
		m.appendLine(styleError.Render(statusLine("provider", fmt.Sprintf("write config: %v", err))))
		return m, nil
	}
	m.providerPickerOpen = false
	m.providerPicker = nil
	// Apply the switch in-session via the existing providerUse path
	// so we get a fresh adapter, probe, memory reload, and the same
	// "[provider] switched" line as the legacy /provider use.
	return providerUse(m, chosen.Name)
}

// commitProviderRemove deletes the highlighted provider and persists.
// Closes the picker, then hands the post-remove orchestration to
// applyProviderRemove (commands.go) — auto-switch to the first
// remaining provider when the removed one was active, or invalidate
// the in-memory adapter if nothing else is configured.
func (m Model) commitProviderRemove() (Model, tea.Cmd) {
	p := m.providerPicker
	if p == nil || p.usePickerCursor >= len(p.providers) {
		return m, nil
	}
	chosen := p.providers[p.usePickerCursor]
	m.providerPickerOpen = false
	m.providerPicker = nil
	return applyProviderRemove(m, chosen.Name)
}

// renderProviderPicker draws the overlay. Mode-specific render
// functions share the bordered box and footer.
func renderProviderPicker(p *providerPickerState, width int, hits ...*pickerHits) string {
	var h *pickerHits
	if len(hits) > 0 {
		h = hits[0]
	}
	var body string
	switch p.mode {
	case providerMenuMode:
		body = renderProviderMenu(p, width, h)
	case providerUsePickerMode:
		body = renderProviderUseList(p, width, "Switch provider", h)
	case providerRemovePickerMode:
		body = renderProviderUseList(p, width, "Remove provider", h)
	case providerAddKindMode:
		body = renderProviderAddKind(p, width, h)
	case providerAddFieldsMode:
		body = renderProviderAddFields(p, width)
	default:
		body = styleEmpty.Render("(unknown picker state)")
	}
	footerText := "↵ confirm · esc back · ↑↓ navigate"
	if p.mode == providerAddFieldsMode {
		footerText = "↵ save · tab/shift+tab switch fields · esc back"
	}
	footer := styleFooter.Render(footerText)
	// Deliberately borderless/uncentered: popupBox (popup.go) supplies
	// the single rounded border and composePopup does the centering, so
	// adding either here would read as "modal floating on a modal".
	_ = width
	return body + "\n\n" + footer
}

func renderProviderMenu(p *providerPickerState, width int, h *pickerHits) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Provider",
		"Manage configured providers. Pick an action to continue.", width))
	b.WriteString("\n")
	for i, item := range p.menuItems {
		row := strings.Count(b.String(), "\n")
		h.row(row, i)
		b.WriteString(renderMenuItem(menuItemOpts{
			Number:     i + 1,
			Label:      item.Label,
			LabelWidth: 10,
			Desc:       item.Subtitle,
			Cursor:     i == p.menuCursor,
		}))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderProviderAddKind shows the catalog list — the first stage
// of /provider Add. Each row carries the provider name plus its
// short Note so the user can pick by purpose, not by URL.
func renderProviderAddKind(p *providerPickerState, width int, h *pickerHits) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Add provider — pick a kind",
		"Cloud providers need only an API key. Free-form endpoints (Ollama, custom) need URL + model + key.", width))
	b.WriteString("\n")
	for i, e := range p.addCatalog {
		row := strings.Count(b.String(), "\n")
		h.row(row, i)
		desc := e.Note
		b.WriteString(renderMenuItem(menuItemOpts{
			Number:     i + 1,
			Label:      e.Name,
			LabelWidth: 14,
			Desc:       desc,
			Cursor:     i == p.addKindCursor,
		}))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderProviderAddFields shows the form. Each field gets one line:
// label, then the textinput's view (with a cursor on the focused
// row). addInputErr is rendered above the fields when an input
// check fails (missing name / base URL / default model).
func renderProviderAddFields(p *providerPickerState, width int) string {
	var b strings.Builder
	title := "Add provider"
	desc := "Fill in the fields and press Enter to save."
	if p.addPicked != nil {
		title = "Add provider — " + p.addPicked.Name
	}
	b.WriteString(renderMenuHeader(title, desc, width))
	b.WriteString("\n")
	if p.addInputErr != "" {
		b.WriteString(styleError.Render("✗ " + p.addInputErr))
		b.WriteString("\n\n")
	}
	for i, label := range p.addLabels {
		marker := "  "
		if i == p.addFocused {
			marker = "❯ "
		}
		fmt.Fprintf(&b, "%s%-14s %s\n", marker, label+":", p.addFields[i].View())
		if label == "Model family" {
			b.WriteString(renderProviderAddFamilyExtras(p))
		}
		if label == "GCP project" {
			project := strings.TrimSpace(p.addFields[i].Value())
			b.WriteString("  ")
			b.WriteString(styleMeta.Render("full URL: " + wizard.VertexBaseURL(p.addVertexFamily, project)))
			b.WriteString("\n")
		}
		if label == "Default model" {
			b.WriteString(renderProviderAddModelExtras(p))
		}
	}
	if p.addPicked != nil && p.addPicked.APIKeyEnv != "" {
		b.WriteString("\n")
		hint := fmt.Sprintf("(API key written to ~/.yottacode/.env as %s)", p.addPicked.APIKeyEnv)
		b.WriteString(styleMeta.Render(hint))
	}
	return strings.TrimRight(b.String(), "\n")
}

func vertexFamilyDropdownValue(f wizard.VertexFamily) string {
	return "▾ " + f.Label()
}

func renderProviderAddFamilyExtras(p *providerPickerState) string {
	families := []wizard.VertexFamily{wizard.VertexFamilyGemini, wizard.VertexFamilyClaude}
	active := p.familyPickerActive()
	var b strings.Builder
	for _, f := range families {
		cursor := "    "
		if active && p.addVertexFamily == f {
			cursor = "  ▸ "
		}
		label := cursor + f.Label()
		if p.addVertexFamily == f {
			label += " ✓"
		}
		b.WriteString(styleMeta.Render(label))
		b.WriteString("\n")
	}
	b.WriteString("  ")
	if active {
		b.WriteString(styleHint.Render("(↑↓ choose Gemini or Claude, Enter/Tab to continue)"))
	} else {
		b.WriteString(styleHint.Render("(focus this row with Tab to choose Gemini or Claude)"))
	}
	b.WriteString("\n")
	return b.String()
}

// renderProviderAddModelExtras draws the catalog list (when curated
// and populated) or the "catalog empty" hint (when curated but not
// yet refreshed) under the Default model field. Free-form kinds
// (ollama / openai-compatible) get nothing extra here — the
// FreeFormModelPlaceholder on the textinput is sufficient.
func renderProviderAddModelExtras(p *providerPickerState) string {
	if len(p.addCuratedModels) > 0 {
		var b strings.Builder
		active := p.curatedPickerActive()
		for i, model := range p.addCuratedModels {
			cursor := "    "
			if i == p.addModelCursor && active {
				cursor = "  ▸ "
			}
			label := truncateLabel(model.Label(), 38)
			line := fmt.Sprintf("%s%s", cursor, label)
			if model.ContextWindow > 0 {
				line += fmt.Sprintf("  (ctx %dK)", model.ContextWindow/1000)
			}
			b.WriteString(styleMeta.Render(line))
			b.WriteString("\n")
		}
		switch {
		case !active:
			b.WriteString("  ")
			b.WriteString(styleHint.Render("(focus this row with Tab, then ↑↓ to pick from the catalog)"))
			b.WriteString("\n")
		case p.addModelCursor < 0:
			// Field is focused but the user hasn't arrowed yet —
			// nudge them so save doesn't fail with the generic
			// "default model is required" without a path forward.
			b.WriteString("  ")
			b.WriteString(styleHint.Render("(↑↓ to pick a model from the list above, or type a tag to override)"))
			b.WriteString("\n")
		}
		return b.String()
	}
	if p.addPicked != nil && catalog.IsCuratedKind(p.addPicked.Kind) {
		if p.addPicked.Kind == "openai-auth" {
			return "  " + styleMeta.Render(
				"(your full model list is discovered after browser sign-in — gpt-5.5 is the universal default)") + "\n"
		}
		if p.addPicked.Kind == "copilot" {
			return "  " + styleMeta.Render(
				"(your model list is cached after device code sign-in — claude-haiku-4.5 is a safe default)") + "\n"
		}
		return "  " + styleMeta.Render(
			"(catalog empty — run `go run ./cmd/yotta-models refresh` to populate)") + "\n"
	}
	return ""
}

func renderProviderUseList(p *providerPickerState, width int, title string, h *pickerHits) string {
	var b strings.Builder
	desc := "Pick a provider to switch the running session to. Switching mid-session resets the provider's prompt cache — the next turn reprocesses full history."
	if title == "Remove provider" {
		desc = "Pick a provider to delete from your config. The active provider is cleared if removed."
	}
	b.WriteString(renderMenuHeader(title, desc, width))
	b.WriteString("\n")
	if len(p.providers) == 0 {
		b.WriteString(styleEmpty.Render("  no providers configured — try /provider add"))
		return strings.TrimRight(b.String(), "\n")
	}
	for i, prov := range p.providers {
		row := strings.Count(b.String(), "\n")
		h.row(row, i)
		// Render the catalog identity (e.g. "nvidia-nim") when we can
		// derive it from the profile name; falls back to Kind for
		// hand-rolled or "custom" profiles that don't trace back to a
		// catalog entry. Without this, two NVIDIA NIM profiles both
		// read as "openai-compatible" and the user has no way to tell
		// which vendor any given profile is for.
		descCol := prov.Kind
		if id := wizard.CatalogIdentity(prov.Name); id != "" {
			descCol = id
		}
		if prov.DefaultModel != "" {
			descCol += " · default=" + prov.DefaultModel
		}
		b.WriteString(renderMenuItem(menuItemOpts{
			Number:     i + 1,
			Label:      prov.Name,
			LabelWidth: 18,
			Desc:       descCol,
			Cursor:     i == p.usePickerCursor,
			Checked:    prov.Name == p.activeProvider,
		}))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
