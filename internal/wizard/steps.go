package wizard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	copilotauth "github.com/yottadynamics/yottacode/internal/auth/copilot"
	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/providerops"
)

// stepID enumerates the wizard's screens. Order matters: stepBack /
// stepNext use it to navigate. Sub-loops (per-provider config) are
// driven by configCursor inside the model, not new step values.
type stepID int

const (
	stepWelcome stepID = iota
	stepProviders
	stepConfigure // loops over enabled providers
	stepActive
	stepRouter
	stepReview
	stepWriting
	// stepOpenAIAuthLogin runs the inline OAuth flow when the user
	// picked openai-auth. Sits between stepWriting and stepDone — the
	// config has already been written; the wizard just needs to drive
	// the browser sign-in before declaring success. Skipped entirely
	// when openai-auth wasn't picked.
	stepOpenAIAuthLogin
	// stepOpenAIAuthScan runs after a successful OAuth login: it
	// probes each candidate model against the codex backend and
	// persists the OK list to ~/.yottacode/auth/openai-auth-models.json.
	// The picker and adapter both read that file, so the scan is what
	// makes the provider usable in the very first session.
	stepOpenAIAuthScan
	// stepCopilotLogin runs the device code flow when the user
	// picked copilot. Sits between stepWriting and stepDone.
	stepCopilotLogin
	stepDone
)

// stepLabels are the short titles shown in the header.
var stepLabels = map[stepID]string{
	stepWelcome:         "Welcome",
	stepProviders:       "Pick providers",
	stepConfigure:       "Configure provider",
	stepActive:          "Default model",
	stepRouter:          "Router",
	stepReview:          "Review",
	stepWriting:         "Writing",
	stepOpenAIAuthLogin: "Browser sign-in",
	stepOpenAIAuthScan:  "Discovering models",
	stepDone:            "Done",
}

// totalSteps is the count shown as "Step X of N." We hide stepWriting
// and stepDone from the count — they're transitional. stepRouter is
// also hidden: the multi-provider router (see internal/adapter/multi.go)
// is a real, working feature but only fires on early-failure fallback —
// invisible in the happy path. Exposing the picker to first-run users
// adds friction for a feature most won't see fire. Power users still
// configure [router] in config.toml directly; merge.go preserves an
// existing block when the wizard skips it.
const totalSteps = 4

// stepNumber maps a step to its display index (1-based). Returns 0
// for hidden steps so the header can suppress the line. stepRouter
// returns 0 because the wizard flow skips it (Active → Review
// directly); the constant + handlers remain so we can put the screen
// back without re-plumbing.
func stepNumber(s stepID) int {
	switch s {
	case stepWelcome:
		return 1
	case stepProviders:
		return 2
	case stepConfigure:
		return 3
	case stepActive:
		return 4
	}
	return 0
}

// providerInputs holds the per-provider state collected on the
// stepConfigure screen.
type providerInputs struct {
	entry       CatalogEntry
	key         textinput.Model
	baseURL     textinput.Model
	project     textinput.Model
	customModel textinput.Model
	// curatedModels is the embedded catalog for curated kinds
	// (anthropic/openai/gemini/xai). Empty when the kind isn't curated
	// or the catalog hasn't been refreshed yet — in either case the
	// wizard falls back to the customModel textinput. modelCursor
	// indexes into this slice when non-empty.
	curatedModels []catalog.Model
	modelCursor   int
	chosenModel   string // resolved model id (free-form text or catalog entry)
	envHas        bool   // key already present in env
	skipKey       bool   // user explicitly skipped (chose to use env later)
	configured    bool   // user has finished the configure screen for this entry,
	// or it was auto-marked from env detection / Ollama probe.
	// Drives the ✓ marker on the provider list and is the
	// inclusion test for activeChoices / assemblePlan.
	validation ValidationResult
	validating bool
	field      int // 0=key/family, 1=baseURL (custom/vertex only), 2=model
	// vertexFamily stores the Gemini/Claude choice for the combined
	// Google Vertex AI setup row. The selected family rewrites entry to
	// the concrete provider kind before config is assembled.
	vertexFamily VertexFamily
}

// wizardModel is the Bubbletea state. Keeps every screen's data so
// users can navigate back without losing their work.
type wizardModel struct {
	ctx context.Context

	opts        Options
	envSnap     EnvSnapshot
	ollamaProbe OllamaProbeResult
	existingCfg bool // ~/.yottacode/config.toml exists

	step stepID

	// Provider list cursor. Ranges over [0, len(Catalog)] — index
	// len(Catalog) addresses the "Continue →" sentinel row at the
	// bottom of the list which advances to stepActive.
	providerCursor int

	// inputs is one slot per Catalog entry, pre-built at construction
	// time. We used to build it lazily after a multi-select commit,
	// but the new flow is "Enter on row → drop directly into
	// configure," which requires the input state to exist as soon as
	// the user lands on the provider list. configIdx is the catalog
	// index of the provider currently being edited (when on
	// stepConfigure) — same index space as inputs.
	configIdx int
	inputs    []providerInputs

	// Active selection.
	activeCursor int

	// Router.
	routerEnabled bool
	routerPolicy  string
	routerCursor  int // 0 = no, 1 = fallback-chain, 2 = cheap-first

	// Review.
	reviewBody   string
	reviewCursor int

	// Global.
	width, height int
	spin          spinner.Model
	err           error
	notice        string
	finalPlan     Plan
	outcome       WriteOutcome
	aborted       bool

	// openai-auth inline OAuth state. Set in writeDoneMsg when
	// openai-auth was picked, cleared after openAIAuthDoneMsg fires.
	openAIAuthPending *openaiauth.PendingLogin
	openAIAuthURL     string // displayed during the wait for fallback
	openAIAuthErr     error

	// openai-auth post-login model scan state.
	// openAIAuthAccessToken is forwarded from the login Wait result
	// to the scan cmd so the scanner doesn't have to re-load the
	// token store. openAIAuthModels is the discovered set on success;
	// openAIAuthScanErr captures any failure for the stepDone view.
	openAIAuthAccessToken string
	openAIAuthModels      []string
	openAIAuthScanErr     error

	copilotUserCode  string
	copilotVerifyURI string
	copilotErr       error

	// Embedding model sub-step state. Active when the user just
	// finished configuring Ollama and the wizard asks about semantic
	// search.
	embedSubStep     bool
	embedCursor      int // 0=nomic-embed-text, 1=all-minilm, 2=skip
	embedPulling     bool
	embedPullErr     error
	embedChosenModel string // set on success (auto-detected or pulled)
}

// runInteractive opens the Bubbletea program and translates its
// terminal Plan into the on-disk write. The interactive path's
// output (post-write summary) is consistent with the
// non-interactive path so users see the same "wrote /path" lines.
func runInteractive(ctx context.Context, opts Options) (Plan, error) {
	m := newWizardModel(ctx, opts)
	// Alt-screen mode is now set declaratively in View() (v.AltScreen
	// = true) rather than as a NewProgram option. The post-write
	// summary still goes to the regular terminal buffer because Run()
	// prints it after the program exits.
	prog := tea.NewProgram(m)
	final, err := prog.Run()
	if err != nil {
		return Plan{}, fmt.Errorf("wizard: tui: %w", err)
	}
	wm, ok := final.(wizardModel)
	if !ok {
		return Plan{}, errors.New("wizard: unexpected model type")
	}
	if wm.aborted {
		return Plan{}, errAborted
	}
	if wm.err != nil {
		return Plan{}, wm.err
	}
	return wm.finalPlan, nil
}

// errAborted is the sentinel for Ctrl+C / ESC aborts. Surfaced as a
// clean exit (status 1) by the cobra command.
var errAborted = errors.New("wizard aborted")

func newWizardModel(ctx context.Context, opts Options) wizardModel {
	envSnap := SnapshotEnv()
	probe := ProbeOllama(ctx, "")
	exists := false
	if opts.ConfigPath != "" {
		if cfg, err := Decode(opts.ConfigPath); err == nil && len(cfg.Providers) > 0 {
			exists = true
		}
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	m := wizardModel{
		ctx:          ctx,
		opts:         opts,
		envSnap:      envSnap,
		ollamaProbe:  probe,
		existingCfg:  exists,
		step:         stepWelcome,
		spin:         sp,
		routerCursor: 1, // fallback-chain default
		routerPolicy: "fallback-chain",
	}

	// Pre-build inputs for every catalog entry. The new flow has the
	// user land on the provider list and press Enter to drop directly
	// into a configure screen, so the per-provider input state must
	// exist before they make a choice.
	//
	// Note: providers are NOT auto-marked configured even when their
	// API key is in env or Ollama is reachable. ✓ exclusively means
	// "the user has explicitly walked through this provider's
	// configure screen." Auto-marking confused users — they'd see ✓
	// before doing anything, hit Enter+ESC, and think the navigation
	// selected the provider. The env badge ([$ANTHROPIC_API_KEY]) and
	// ollama probe badge ([N models]) carry the "detected" signal;
	// ✓ carries the "explicitly chosen" signal. Two badges, two
	// concepts.
	m.inputs = make([]providerInputs, len(Catalog))
	for i, e := range Catalog {
		m.inputs[i] = m.newProviderInputs(e)
	}

	// --provider list (interactive) just hints at where to put the
	// initial cursor. Pre-config rules above still apply; this flag is
	// most useful for non-interactive runs where buildPlanFromOptions
	// reads it directly.
	if len(opts.Providers) > 0 {
		first := strings.TrimSpace(opts.Providers[0])
		for i, e := range Catalog {
			if e.Name == first {
				m.providerCursor = i
				break
			}
		}
	} else {
		// Position cursor on the first provider that still needs
		// attention — saves the user a keystroke when the env already
		// pre-configured the leading entries.
		for i, in := range m.inputs {
			if !in.configured {
				m.providerCursor = i
				break
			}
		}
	}

	return m
}

// hasAnyConfigured reports whether at least one provider has been
// marked configured. Gates the Continue → stepActive transition;
// without this guard the active picker would render an empty list.
func (m wizardModel) hasAnyConfigured() bool {
	for _, in := range m.inputs {
		if in.configured {
			return true
		}
	}
	return false
}

// configuredCount returns the number of providers ready for stepActive.
// Surfaced on the Continue row so the user can see progress at a
// glance without counting ✓ markers.
func (m wizardModel) configuredCount() int {
	n := 0
	for _, in := range m.inputs {
		if in.configured {
			n++
		}
	}
	return n
}

// Init starts the spinner ticker for the live validation glyph and
// warms the models.dev cache in the background. The warm matters
// here: stepConfigure reads catalog.Curated synchronously when
// building the Gemini model picker, and on a fresh install (no disk
// cache yet) a cold read could block the UI on the one-shot network
// fetch. Warming at Init makes that read an in-memory hit by the
// time the user reaches the configure step; offline, the embedded
// snapshot floor keeps the picker populated either way.
func (m wizardModel) Init() tea.Cmd {
	return tea.Batch(
		m.spin.Tick,
		func() tea.Msg { catalog.WarmModelsDev(); return nil },
	)
}

// validationDoneMsg is sent when an async key validation completes.
type validationDoneMsg struct {
	provider string
	result   ValidationResult
}

// writeDoneMsg is sent after the write phase completes.
type writeDoneMsg struct {
	outcome WriteOutcome
	err     error
}

// openAIAuthURLReadyMsg is sent by the StartLogin cmd once the
// callback server is listening + the auth URL is built. The wait
// hasn't started yet; the model uses this to render the URL for
// fallback display before dispatching the blocking Wait cmd.
type openAIAuthURLReadyMsg struct {
	pending *openaiauth.PendingLogin
	err     error
}

// openAIAuthDoneMsg is sent by the Wait cmd when the user finishes
// the browser flow (or it fails / times out). On success the token
// has already been persisted to ~/.yottacode/auth/openai-auth.json
// and accessToken is forwarded to the scan cmd.
type openAIAuthDoneMsg struct {
	err         error
	accessToken string
}

// openAIAuthScanDoneMsg is sent by the scan cmd after the post-login
// model probe completes. On success the per-user model list has been
// written to ~/.yottacode/auth/openai-auth-models.json; on failure
// the file (if any) is untouched and err carries the cause.
type openAIAuthScanDoneMsg struct {
	models []string
	err    error
}

// pickedOpenAIAuth reports whether one of the user's selected
// providers is openai-auth — driver for the post-write OAuth jump.
func pickedOpenAIAuth(choices [][2]string) bool {
	for _, c := range choices {
		if c[0] == "openai-auth" {
			return true
		}
	}
	return false
}

func pickedCopilot(choices [][2]string) bool {
	for _, c := range choices {
		if c[0] == "copilot-auth" {
			return true
		}
	}
	return false
}

// embedPullDoneMsg is sent when the async Ollama pull for an embedding
// model completes.
type embedPullDoneMsg struct {
	model string
	err   error
}

// copilotDeviceCodeMsg is sent once the device code is obtained.
type copilotDeviceCodeMsg struct {
	dc  copilotauth.DeviceCode
	err error
}

// copilotAuthDoneMsg is sent when the user completes device auth.
type copilotAuthDoneMsg struct {
	err error
}

func startCopilotLoginCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		dc, err := copilotauth.RequestDeviceCode(ctx, nil, "")
		return copilotDeviceCodeMsg{dc: dc, err: err}
	}
}

func pollCopilotLoginCmd(ctx context.Context, dc copilotauth.DeviceCode) tea.Cmd {
	return func() tea.Msg {
		token, err := copilotauth.PollForToken(ctx, nil, "", dc)
		if err != nil {
			return copilotAuthDoneMsg{err: err}
		}
		storePath, err := copilotauth.DefaultStorePath()
		if err != nil {
			return copilotAuthDoneMsg{err: fmt.Errorf("resolve store path: %w", err)}
		}
		ts := copilotauth.TokenSet{GitHubToken: token}
		if err := copilotauth.Save(storePath, ts); err != nil {
			return copilotAuthDoneMsg{err: fmt.Errorf("save token: %w", err)}
		}
		ct, err := copilotauth.FetchCopilotToken(ctx, nil, token)
		if err != nil {
			return copilotAuthDoneMsg{err: fmt.Errorf("verify Copilot: %w", err)}
		}
		_, _ = copilotauth.FetchAndCacheModels(ctx, ct)
		return copilotAuthDoneMsg{err: nil}
	}
}

func (m wizardModel) viewCopilotLogin() string {
	var b strings.Builder
	w := lineWidthFor(m.width)
	b.WriteString("\n  ")
	b.WriteString(m.spin.View())
	b.WriteString(" ")
	b.WriteString(styleSelected.Render("waiting for device code authorization"))
	b.WriteString("\n\n")
	if m.copilotUserCode != "" {
		b.WriteString("  ")
		b.WriteString(styleHint.Render(truncate("Open "+m.copilotVerifyURI+" and enter code:", w-2)))
		b.WriteString("\n  ")
		b.WriteString(stylePathHL.Render(m.copilotUserCode))
		b.WriteString("\n")
	}
	b.WriteString("\n  ")
	b.WriteString(styleMuted.Render("Ctrl-C to cancel."))
	b.WriteString("\n")
	return b.String()
}

// startOpenAIAuthLoginCmd does the synchronous prep phase of the
// OAuth flow: starts the loopback listener, builds the auth URL,
// best-effort launches the browser. On success returns a
// PendingLogin in openAIAuthURLReadyMsg so the wizard can render
// the URL while the user is signing in. On failure (port bind
// failure, PKCE generation failure, etc.) the err field is set
// and the wizard surfaces it on stepDone.
func startOpenAIAuthLoginCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		pending, err := openaiauth.StartLogin(ctx, openaiauth.LoginOptions{})
		return openAIAuthURLReadyMsg{pending: pending, err: err}
	}
}

// viewOpenAIAuthLogin renders the in-progress OAuth wait screen.
// Shows the auth URL so the user can copy-paste it if the browser
// launcher silently failed (headless box, missing $DISPLAY, etc.).
// Spinner ticks live; the wait blocks in waitOpenAIAuthLoginCmd
// until the callback fires.
func (m wizardModel) viewOpenAIAuthLogin() string {
	var b strings.Builder
	w := lineWidthFor(m.width)
	b.WriteString("\n  ")
	b.WriteString(m.spin.View())
	b.WriteString(" ")
	b.WriteString(styleSelected.Render("waiting for browser sign-in"))
	b.WriteString("\n\n")
	b.WriteString("  ")
	b.WriteString(styleHint.Render(truncate("A browser window should have opened to OpenAI. Sign in there.", w-2)))
	b.WriteString("\n")
	if m.openAIAuthURL != "" {
		b.WriteString("\n  ")
		b.WriteString(styleHint.Render("If it didn't, paste this URL into your browser:"))
		b.WriteString("\n  ")
		b.WriteString(stylePathHL.Render(truncate(m.openAIAuthURL, w-2)))
		b.WriteString("\n")
	}
	b.WriteString("\n  ")
	b.WriteString(styleMuted.Render("Ctrl-C to cancel."))
	b.WriteString("\n")
	return b.String()
}

// waitOpenAIAuthLoginCmd blocks on the user's browser sign-in and
// persists the resulting token to the default store on success.
// Failures (timeout, state mismatch, network error during the code
// exchange) come back via openAIAuthDoneMsg's err field. The access
// token is forwarded so the follow-up scan doesn't have to re-load
// the token store.
func waitOpenAIAuthLoginCmd(ctx context.Context, pending *openaiauth.PendingLogin) tea.Cmd {
	return func() tea.Msg {
		ts, err := pending.Wait(ctx)
		if err != nil {
			return openAIAuthDoneMsg{err: err}
		}
		path, err := openaiauth.DefaultStorePath()
		if err != nil {
			return openAIAuthDoneMsg{err: fmt.Errorf("resolve token store path: %w", err)}
		}
		if err := openaiauth.Save(path, ts); err != nil {
			return openAIAuthDoneMsg{err: fmt.Errorf("save tokens: %w", err)}
		}
		return openAIAuthDoneMsg{err: nil, accessToken: ts.AccessToken}
	}
}

// scanOpenAIAuthCmd runs the post-login model scan. Failures don't
// touch the existing models file (if any); the caller surfaces the
// error on stepDone so the user knows to retry login.
func scanOpenAIAuthCmd(ctx context.Context, accessToken string) tea.Cmd {
	return func() tea.Msg {
		models, err := openaiauth.ScanAndPersist(ctx, accessToken)
		return openAIAuthScanDoneMsg{models: models, err: err}
	}
}

// rollbackOpenAIAuth removes any openai-auth provider from the just-
// written config so a cancelled or failed OAuth flow doesn't leave a
// broken profile on disk. The wizard writes everything in one shot
// before OAuth runs (multi-provider semantics — anthropic+openai-auth
// must persist anthropic even if openai-auth fails), so we treat the
// failure as a rollback instead of deferring like the TUI's
// commitProviderAddNow path. Same primitive (providerops.Remove) the
// TUI's persist-on-failure path uses — the abstraction is one layer
// down, in providerops.
//
// When [active] pointed at the openai-auth profile, providerops.Remove
// already clears it; the user lands on a config with no active and
// the welcome message on next run prompts them to pick. Errors during
// rollback are silently swallowed — the OAuth failure is the primary
// signal; stacking a rollback error would just confuse the user.
func (m *wizardModel) rollbackOpenAIAuth() {
	if !m.outcome.WroteConfig || m.outcome.ConfigPath == "" {
		return
	}
	cfg, err := config.Load(m.outcome.ConfigPath)
	if err != nil {
		return
	}
	dropped := false
	for {
		idx := -1
		for i, p := range cfg.Providers {
			if p.Kind == "openai-auth" {
				idx = i
				break
			}
		}
		if idx < 0 {
			break
		}
		updated, err := providerops.Remove(cfg, cfg.Providers[idx].Name)
		if err != nil {
			return
		}
		cfg = updated
		dropped = true
	}
	if !dropped {
		return
	}
	_ = config.Save(cfg, m.outcome.ConfigPath)
}

// viewOpenAIAuthScan renders the in-progress scan screen: the OAuth
// flow has completed, the token is persisted, and the wizard is
// probing each candidate model against the codex backend.
func (m wizardModel) viewOpenAIAuthScan() string {
	var b strings.Builder
	w := lineWidthFor(m.width)
	b.WriteString("\n  ")
	b.WriteString(m.spin.View())
	b.WriteString(" ")
	b.WriteString(styleSelected.Render("scanning available models"))
	b.WriteString("\n\n")
	b.WriteString("  ")
	b.WriteString(styleHint.Render(truncate("Probing each candidate against the ChatGPT backend so the picker shows what your account actually supports. Takes a few seconds.", w-2)))
	b.WriteString("\n")
	return b.String()
}

// Update handles every keypress. We dispatch to per-step handlers so
// each screen owns its own keys and the function stays scannable.
func (m wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Resize every per-provider textinput to fit the new
		// terminal width. Floor of 20 keeps the inputs usable on
		// very narrow terminals; ceiling of 80 keeps lines from
		// stretching beyond comfortable reading width on wide
		// monitors. We subtract 8 to leave room for the leading
		// "  > " gutter and a trailing margin.
		w := inputWidthFor(msg.Width)
		for i := range m.inputs {
			m.inputs[i].key.SetWidth(w)
			m.inputs[i].baseURL.SetWidth(w)
			m.inputs[i].customModel.SetWidth(w)
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case validationDoneMsg:
		for i := range m.inputs {
			if m.inputs[i].entry.Name == msg.provider {
				m.inputs[i].validating = false
				m.inputs[i].validation = msg.result
				// Surface a top-level confirmation banner on success so
				// it's unmistakable even after the user has advanced
				// past the key field. Cleared when leaving the provider
				// or starting a new validation. Failures stay on the
				// per-field glyph (validationGlyph) — a top banner for
				// every fail would shout, and the user is going to see
				// the ✗ next to the input anyway.
				if msg.result.Status == ValidationOK {
					m.notice = "✓ " + msg.provider + " key valid"
				}
				break
			}
		}
		return m, nil
	case writeDoneMsg:
		m.outcome = msg.outcome
		m.err = msg.err
		// If the write failed or openai-auth isn't on the picked list,
		// jump straight to stepDone like before. Otherwise route through
		// the inline OAuth flow before declaring success — the user
		// just authorized us in their head; finishing without the
		// browser sign-in would leave a half-configured provider.
		if msg.err == nil && pickedOpenAIAuth(m.activeChoices()) {
			m.step = stepOpenAIAuthLogin
			return m, startOpenAIAuthLoginCmd(m.ctx)
		}
		if msg.err == nil && pickedCopilot(m.activeChoices()) {
			m.step = stepCopilotLogin
			return m, startCopilotLoginCmd(m.ctx)
		}
		m.step = stepDone
		return m, tea.Quit
	case openAIAuthURLReadyMsg:
		if msg.err != nil {
			m.openAIAuthErr = msg.err
			// Apply already wrote the openai-auth profile to disk
			// before this step ran; roll it back so a port-bind /
			// browser-launch failure doesn't leave a broken profile
			// the user can't easily clean up. Same primitive
			// (providerops.Remove) the TUI's persist-on-failure path
			// uses — applied here as post-hoc rollback because the
			// wizard's batch write semantics make pure deferral
			// awkward.
			m.rollbackOpenAIAuth()
			m.step = stepDone
			return m, tea.Quit
		}
		m.openAIAuthPending = msg.pending
		m.openAIAuthURL = msg.pending.AuthURL
		return m, waitOpenAIAuthLoginCmd(m.ctx, msg.pending)
	case openAIAuthDoneMsg:
		m.openAIAuthErr = msg.err
		m.openAIAuthPending = nil
		// On login failure, skip the scan — there's nothing to scan
		// against. Otherwise transition to the scan step and dispatch
		// the scan cmd before declaring done.
		if msg.err != nil {
			// See URL-ready branch above: roll back the openai-auth
			// profile from the just-written config so a cancelled
			// sign-in doesn't leave the wizard's output broken. The
			// user gets a working config without openai-auth and the
			// review screen shows the failure so they know to retry.
			m.rollbackOpenAIAuth()
			m.step = stepDone
			return m, tea.Quit
		}
		m.openAIAuthAccessToken = msg.accessToken
		m.step = stepOpenAIAuthScan
		return m, scanOpenAIAuthCmd(m.ctx, msg.accessToken)
	case openAIAuthScanDoneMsg:
		m.openAIAuthModels = msg.models
		m.openAIAuthScanErr = msg.err
		m.openAIAuthAccessToken = ""
		m.step = stepDone
		return m, tea.Quit
	case embedPullDoneMsg:
		m.embedPulling = false
		if msg.err != nil {
			m.embedPullErr = msg.err
			return m, nil
		}
		m.embedChosenModel = msg.model
		m.embedSubStep = false
		m.providerCursor = m.configIdx
		m.step = stepProviders
		m.notice = msg.model + " pulled — semantic search enabled"
		return m, nil
	case copilotDeviceCodeMsg:
		if msg.err != nil {
			m.copilotErr = msg.err
			m.step = stepDone
			return m, tea.Quit
		}
		m.copilotUserCode = msg.dc.UserCode
		m.copilotVerifyURI = msg.dc.VerificationURI
		return m, pollCopilotLoginCmd(m.ctx, msg.dc)
	case copilotAuthDoneMsg:
		m.copilotErr = msg.err
		m.step = stepDone
		return m, tea.Quit
	case tea.KeyPressMsg:
		// Global keys.
		switch msg.String() {
		case "ctrl+c":
			m.aborted = true
			if m.openAIAuthPending != nil {
				// Release the loopback listener so the port frees
				// before exit.
				m.openAIAuthPending.Close()
				m.openAIAuthPending = nil
			}
			return m, tea.Quit
		case "esc":
			if m.step == stepWelcome {
				m.aborted = true
				return m, tea.Quit
			}
			// During inline OAuth or the post-login scan, esc is a
			// no-op — going back would tear down the listener (or
			// abandon a partial scan) without giving the user a
			// recovery path. Ctrl-C cancels cleanly.
			if m.step == stepOpenAIAuthLogin || m.step == stepOpenAIAuthScan || m.step == stepCopilotLogin {
				return m, nil
			}
			return m.goBack(), nil
		}
		switch m.step {
		case stepWelcome:
			return m.updateWelcome(msg)
		case stepProviders:
			return m.updateProviders(msg)
		case stepConfigure:
			return m.updateConfigure(msg)
		case stepActive:
			return m.updateActive(msg)
		case stepRouter:
			return m.updateRouter(msg)
		case stepReview:
			return m.updateReview(msg)
		}
	}
	return m, nil
}

// View dispatches to per-step renderers wrapped with the consistent
// header/footer chrome.
func (m wizardModel) View() tea.View {
	v := tea.NewView(m.viewString())
	// Alt-screen for the wizard: it's a modal one-shot flow, not a
	// scrollback-friendly chat. Alt-screen repaints fully on resize so
	// border/separator characters don't smear into the user's
	// scrollback the way they do with inline rendering.
	v.AltScreen = true
	return v
}

func (m wizardModel) viewString() string {
	if m.aborted {
		return styleMuted.Render("aborted") + "\n"
	}
	if m.step == stepWriting {
		return m.viewChrome("Writing configuration",
			fmt.Sprintf("\n  %s saving %s\n",
				m.spin.View(),
				stylePathHL.Render(m.opts.ConfigPath)))
	}
	if m.step == stepOpenAIAuthLogin {
		return m.viewChrome("Browser sign-in", m.viewOpenAIAuthLogin())
	}
	if m.step == stepOpenAIAuthScan {
		return m.viewChrome("Discovering models", m.viewOpenAIAuthScan())
	}
	if m.step == stepCopilotLogin {
		return m.viewChrome("GitHub Copilot sign-in", m.viewCopilotLogin())
	}
	if m.step == stepDone {
		if m.err != nil {
			return m.viewChrome("Done",
				"\n"+styleErr.Render("error: "+m.err.Error())+"\n")
		}
		return m.viewChrome("Done", m.viewDoneSummary())
	}
	switch m.step {
	case stepWelcome:
		return m.viewChrome(stepLabels[m.step], m.viewWelcome())
	case stepProviders:
		return m.viewChrome(stepLabels[m.step], m.viewProviders())
	case stepConfigure:
		return m.viewChrome(m.configHeading(), m.viewConfigure())
	case stepActive:
		return m.viewChrome(stepLabels[m.step], m.viewActive())
	case stepRouter:
		return m.viewChrome(stepLabels[m.step], m.viewRouter())
	case stepReview:
		return m.viewChrome(stepLabels[m.step], m.viewReview())
	}
	return ""
}

// viewChrome wraps a step body with the standard header (logo + step
// counter + section title) and footer (key hints). The chrome is the
// thing that makes the wizard feel like one coherent program rather
// than a stack of unrelated screens. Every styled segment is
// truncated to fit the terminal width so resize never produces wrapped
// lines that stack the rest of the screen vertically.
func (m wizardModel) viewChrome(title, body string) string {
	var b strings.Builder
	w := lineWidthFor(m.width)
	b.WriteString("\n  ")
	b.WriteString(styleTitle.Render("yottacode"))
	b.WriteString(" ")
	b.WriteString(styleMuted.Render("setup wizard"))
	b.WriteString("\n  ")
	// headerCounter adapts to width itself (dots-row vs label-only)
	// because the dots are styled per-segment and can't be truncated
	// after composition without slicing through ANSI escapes.
	b.WriteString(styleStep.Render(m.headerCounter(w)))
	b.WriteString("\n\n  ")
	b.WriteString(styleHeading.Render(truncate(title, w)))
	b.WriteString("\n")
	b.WriteString(body)
	if m.err != nil {
		b.WriteString("\n  ")
		b.WriteString(styleErr.Render(truncate("• "+m.err.Error(), w)))
	} else if m.notice != "" {
		b.WriteString("\n  ")
		b.WriteString(styleHint.Render(truncate("• "+m.notice, w)))
	}
	b.WriteString("\n  ")
	// Footer keys are the second-most common wrap source after long
	// option descriptions: "tab next field · enter continue · ctrl+s
	// skip key · esc back" is 64 visible chars, which wraps on
	// anything < 70 cols. footerKeys truncates internally before
	// styling so we don't slice through ANSI escapes.
	b.WriteString(m.footerKeys(w))
	b.WriteString("\n")
	return b.String()
}

// headerCounter renders the "Step X of N   ● ● ◉ ◯ ◯ ◯ ◯" progress
// indicator. The dots are styled per-segment, so we can't truncate
// the composed string after the fact (ANSI escapes inflate the rune
// count). Instead we pick a representation that fits the supplied
// width up front: full dots-row when there's room, label-only on
// narrow terminals.
func (m wizardModel) headerCounter(width int) string {
	n := stepNumber(m.step)
	if n == 0 {
		return ""
	}
	label := fmt.Sprintf("Step %d of %d", n, totalSteps)
	// Dots cost: 7 visible runes + 6 separator spaces + 3-space
	// gap before the dots = 16 visible width. Falls back to label-
	// only when the budget can't carry both.
	const dotsVisibleWidth = 16
	if width < lipgloss.Width(label)+dotsVisibleWidth {
		return label
	}
	dots := make([]string, totalSteps)
	for i := 0; i < totalSteps; i++ {
		switch {
		case i+1 < n:
			dots[i] = styleProgress.Render("●")
		case i+1 == n:
			dots[i] = styleMuted.Render("◉")
		default:
			dots[i] = styleDim.Render("○")
		}
	}
	return label + "   " + strings.Join(dots, " ")
}

// footerKeys returns the per-step keybinding hint, truncated to fit
// the supplied width. We truncate BEFORE styling so the rune-count
// truncation doesn't slice through ANSI escapes (which would emit
// unterminated colors and a visually broken footer). The hint
// strings are tuned to fit 60 cols at the longest; on narrower
// terminals the tail clips to "…" rather than wrapping.
func (m wizardModel) footerKeys(width int) string {
	plain := ""
	switch m.step {
	case stepWelcome:
		plain = "enter continue · esc quit"
	case stepProviders:
		plain = "↑↓ move · enter configure · esc back"
	case stepConfigure:
		plain = "tab next field · enter continue · ctrl+s skip key · esc back"
	case stepActive, stepRouter:
		plain = "↑↓ choose · enter continue · esc back"
	case stepReview:
		plain = "↑↓ choose · enter select · y write · n abort · b back"
	}
	if plain == "" {
		return ""
	}
	return styleHint.Render(truncate(plain, width))
}

// goBack moves the step pointer one screen earlier. Empty
// implementation for stepConfigure: it walks the per-provider sub-
// loop backwards before crossing back to stepProviders.
func (m wizardModel) goBack() wizardModel {
	m.err = nil
	m.notice = ""
	switch m.step {
	case stepProviders:
		m.step = stepWelcome
	case stepConfigure:
		if m.embedSubStep {
			m.embedSubStep = false
			m.embedPulling = false
			m.embedPullErr = nil
			return m
		}
		// Return to the provider list with the cursor on the row the
		// user was just editing — preserves orientation across the
		// back-and-forth pattern that's now the dominant flow.
		m.providerCursor = m.configIdx
		m.step = stepProviders
	case stepActive:
		// Was: step back into the last per-provider config screen via
		// the old enabledOrder loop. The new flow has no such loop;
		// the natural back step is the provider list itself.
		m.step = stepProviders
	case stepRouter:
		// stepRouter is currently skipped from the wizard flow — kept
		// for symmetry so re-enabling restores the back-edge cleanly.
		m.step = stepActive
	case stepReview:
		// Was: stepAutoMemory. Skipped to match the forward flow
		// Active → Review.
		m.step = stepActive
	}
	return m
}

// =============================================================================
// stepWelcome
// =============================================================================

func (m wizardModel) updateWelcome(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		// Cursor placement is handled in newWizardModel (first row
		// that still needs attention) and preserved across goBack —
		// nothing to do here besides the step transition.
		m.step = stepProviders
	}
	return m, nil
}

func (m wizardModel) viewWelcome() string {
	var b strings.Builder
	w := lineWidthFor(m.width)
	bulletW := w - 4 // leading "    → "
	b.WriteString("\n  " + truncate("Welcome — let's set up yottacode in under a minute.", w) + "\n\n")
	b.WriteString("  This wizard will:\n")
	b.WriteString("    " + styleProgress.Render("→") + " " + truncate("enable one or more providers (Anthropic, OpenAI, Gemini, xAI, Ollama, …)", bulletW) + "\n")
	b.WriteString("    " + styleProgress.Render("→") + " " + truncate("collect API keys (saved to ~/.yottacode/.env, mode 0600)", bulletW) + "\n")
	b.WriteString("    " + styleProgress.Render("→") + " " + truncate("pick a default model and (optionally) a multi-provider router", bulletW) + "\n")
	b.WriteString("    " + styleProgress.Render("→") + " " + truncate("write ~/.yottacode/config.toml", bulletW) + "\n")

	hasOllama := m.ollamaProbe.Reachable
	hasEnv := len(m.envSnap.SuggestedProviders) > 0
	if hasOllama || m.existingCfg || hasEnv {
		b.WriteString("\n  " + styleHeading.Render("Detected") + "\n")
		// labelW = visual width reserved for the row label so the
		// detail column lines up across rows. "existing config" is
		// the longest label at 15 runes; pad to 17 for breathing room.
		const labelW = 17
		row := func(glyph lipgloss.Style, mark, label, detail string) {
			pad := ""
			if visW := lipgloss.Width(label); visW < labelW {
				pad = strings.Repeat(" ", labelW-visW)
			}
			prefix := "    " + glyph.Render(mark) + "  " + label + pad + "  "
			budget := w - lipgloss.Width(prefix)
			b.WriteString(prefix + truncate(detail, budget) + "\n")
		}
		if hasOllama {
			detail := m.ollamaProbe.BaseURL
			if n := len(m.ollamaProbe.Models); n > 0 {
				detail += "  ·  " + fmt.Sprintf("%d %s", n, pluralModels(n))
			}
			row(styleOK, "✓", "ollama running", detail)
		}
		if m.existingCfg {
			row(styleWarn, "◐", "existing config", "tunables preserved · providers merged")
		}
		if hasEnv {
			row(styleOK, "✓", "env keys", strings.Join(m.envSnap.SuggestedProviders, ", "))
		}
	}
	return b.String()
}

// pluralModels returns "model" for 1 and "models" otherwise. Tiny
// helper so the wizard never reads "1 models" anywhere — the welcome
// screen and the provider list both render counts.
func pluralModels(n int) string {
	if n == 1 {
		return "model"
	}
	return "models"
}

// =============================================================================
// stepProviders — pick a provider to configure (no multi-select)
// =============================================================================
//
// Cursor index space is [0, len(Catalog)] — index len(Catalog) is the
// "Continue →" sentinel row at the bottom of the list. Enter on a
// catalog row drops directly into that provider's configure screen;
// Enter on Continue advances to stepActive. Replaces the old
// space/checkbox + commit-batch flow whose two-state UX (cursor vs.
// selection) repeatedly tripped users into pressing Enter expecting
// "select this" and getting "commit and advance."

const continueRowIdx = -1 // sentinel returned by cursorTarget for the Continue row

// cursorTarget returns the catalog index the cursor points at, or
// continueRowIdx (-1) when the cursor is on the Continue sentinel.
func (m wizardModel) cursorTarget() int {
	if m.providerCursor >= len(Catalog) {
		return continueRowIdx
	}
	return m.providerCursor
}

func (m wizardModel) updateProviders(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	maxCursor := len(Catalog) // includes Continue row at the bottom
	switch msg.String() {
	case "up", "k":
		if m.providerCursor > 0 {
			m.providerCursor--
		}
	case "down", "j":
		if m.providerCursor < maxCursor {
			m.providerCursor++
		}
	case "enter":
		if m.cursorTarget() == continueRowIdx {
			if !m.hasAnyConfigured() {
				m.err = errors.New("configure at least one provider first")
				return m, nil
			}
			m.err = nil
			m.activeCursor = 0
			m.step = stepActive
			return m, nil
		}
		// Drop directly into configure for the cursor's provider.
		m.err = nil
		m.notice = ""
		m.configIdx = m.cursorTarget()
		// Reset the field cursor each time we re-enter so the user
		// always lands on the meaningful first field. fieldKind()
		// will skip the key field automatically when it's pre-filled
		// from env.
		m.inputs[m.configIdx].field = 0
		m.step = stepConfigure
		// Focus the right field on entry. Returned cmd is the
		// cursor-blink ticker — required for the input to render a
		// cursor and (via Update's focus gate) accept keys.
		focusCmd := m.focusActiveConfigField()
		return m, focusCmd
	}
	return m, nil
}

func (m wizardModel) viewProviders() string {
	var b strings.Builder
	w := lineWidthFor(m.width)
	header := "Pick a provider to configure. "
	hint := styleHint.Render(truncate("Press Enter to configure; ✓ rows are ready.", w-len(header)))
	b.WriteString("\n  " + truncate(header, w) + hint + "\n\n")
	for i, e := range Catalog {
		// Status marker. ✓ for configured rows, dim · for unconfigured.
		// Cursor signal is the green full-row highlight bar (matches
		// the chat TUI's slash palette).
		configured := i < len(m.inputs) && m.inputs[i].configured
		envHas := e.APIKeyEnv != "" && m.envSnap.Present[e.APIKeyEnv]
		ollamaReach := e.Name == "ollama" && m.ollamaProbe.Reachable
		ollamaModels := 0
		if ollamaReach {
			ollamaModels = len(m.ollamaProbe.Models)
		}

		if i == m.providerCursor {
			// Cursor row: build plain content, paint with full-row bar.
			marker := "·"
			if configured {
				marker = "✓"
			}
			content := marker + " " + e.Name
			if e.APIKeyEnv != "" {
				content += " [$" + e.APIKeyEnv + "]"
			}
			if e.Name == "ollama" {
				if ollamaReach {
					content += fmt.Sprintf(" [%d %s]", ollamaModels, pluralModels(ollamaModels))
				} else {
					content += " [not running]"
				}
			}
			b.WriteString(m.renderRowBar(content) + "\n")
		} else {
			// Bullet and "absent" badges render muted (not dim) so the
			// whole row stays legible. The present/ready states keep their
			// green (styleOK), so muted-vs-green still signals set/unset.
			marker := styleMuted.Render("·")
			// Unconfigured names render in the terminal default
			// foreground (bright, like the Welcome page) so the provider
			// list is easy to scan; configured rows stay accented.
			nameStyle := lipgloss.NewStyle()
			if configured {
				marker = styleOK.Render("✓")
				nameStyle = styleSelected
			}
			envBadge := ""
			if envHas {
				envBadge = " " + styleOK.Render("[$"+e.APIKeyEnv+"]")
			} else if e.APIKeyEnv != "" {
				envBadge = " " + styleMuted.Render("[$"+e.APIKeyEnv+"]")
			}
			ollamaBadge := ""
			if e.Name == "ollama" {
				if ollamaReach {
					ollamaBadge = " " + styleOK.Render(fmt.Sprintf("[%d %s]", ollamaModels, pluralModels(ollamaModels)))
				} else {
					ollamaBadge = " " + styleMuted.Render("[not running]")
				}
			}
			firstRowFixed := lipgloss.Width("  "+marker+" ") + lipgloss.Width(envBadge)
			firstRowBudget := w - firstRowFixed
			switch {
			case firstRowBudget-lipgloss.Width(ollamaBadge) >= len(e.Name):
				fmt.Fprintf(&b, "  %s %s%s\n",
					marker, nameStyle.Render(e.Name), envBadge+ollamaBadge)
			case firstRowBudget >= len(e.Name):
				fmt.Fprintf(&b, "  %s %s%s\n",
					marker, nameStyle.Render(e.Name), envBadge)
			default:
				fmt.Fprintf(&b, "  %s %s%s\n",
					marker, nameStyle.Render(truncate(e.Name, firstRowBudget)), envBadge)
			}
		}
		// Second row: 8-char indent, note text only. Muted (not dim)
		// regardless of cursor — readable as a description, but still a
		// step below the name above so it doesn't compete with the bar.
		noteW := w - 8
		fmt.Fprintf(&b, "        %s\n", styleMuted.Render(truncate(e.Note, noteW)))
	}
	// Continue sentinel row. Always rendered, but the label and
	// cursor styling change based on whether anything's configured —
	// dim when nothing is, accented when at least one provider is
	// ready (a quiet "you can advance now" signal). Cursor signal is
	// the green highlight bar (matches chat TUI's slash palette).
	b.WriteString("\n")
	n := m.configuredCount()
	contLabel := "Continue →"
	contStyle := styleSelected
	if n == 0 {
		contLabel = "Continue → (configure at least one provider first)"
		contStyle = styleDim
	} else {
		contLabel = fmt.Sprintf("Continue →  (%d %s ready)", n, pluralProviders(n))
	}
	contLabel = truncate(contLabel, w-4)
	if m.providerCursor == len(Catalog) {
		b.WriteString(m.renderRowBar(contLabel) + "\n")
	} else {
		b.WriteString("  " + contStyle.Render(contLabel) + "\n")
	}
	return b.String()
}

// pluralProviders mirrors pluralModels for provider-count messaging.
// Defined separately so changing one verb form doesn't accidentally
// shift the other.
func pluralProviders(n int) string {
	if n == 1 {
		return "provider"
	}
	return "providers"
}

// =============================================================================
// stepConfigure — per-provider key + model
// =============================================================================

func (m wizardModel) newProviderInputs(e CatalogEntry) providerInputs {
	in := providerInputs{
		entry: e,
	}
	in.envHas = e.APIKeyEnv != "" && m.envSnap.Present[e.APIKeyEnv]
	in.field = 0

	w := inputWidthFor(m.width)

	in.key = textinput.New()
	in.key.Placeholder = "paste API key (input hidden)"
	in.key.EchoMode = textinput.EchoPassword
	in.key.EchoCharacter = '•'
	in.key.CharLimit = 256
	in.key.SetWidth(w)

	in.baseURL = textinput.New()
	in.baseURL.Placeholder = "https://your-endpoint/v1"
	in.baseURL.CharLimit = 256
	in.baseURL.SetWidth(w)

	in.project = textinput.New()
	in.project.Placeholder = "your-gcp-project-id"
	in.project.CharLimit = 128
	in.project.SetWidth(w)

	in.customModel = textinput.New()
	in.customModel.Placeholder = FreeFormModelPlaceholder(e.Name)
	in.customModel.CharLimit = 128
	in.customModel.SetWidth(w)

	// Pre-fill model defaults for free-form providers where we have a
	// reasonable starting tag — saves the user from typing one out
	// when they just want to accept defaults. Ollama gets the first
	// probed model (a real local model the user definitely has).
	// openai-auth gets gpt-5.5 (the canonical default — every ChatGPT
	// account, including free, has it; the post-login scan will
	// discover the rest of the user's allow-list and the picker will
	// show the full set on next session). For "custom" we have no
	// idea what their endpoint exposes, so we stay silent.
	//
	// nvidia-nim is deliberately NOT pre-filled. NIM API keys minted
	// at build.nvidia.com are commonly scoped to a single model —
	// silently auto-accepting our suggested default would steer
	// users into a 404 when their key wasn't minted for the model
	// we picked. The suggestion is still visible via the
	// FreeFormModelPlaceholder placeholder set above, so the user
	// knows what to type, but the empty-model guard in the save
	// handler forces an explicit pick.
	switch {
	case e.Name == "ollama" && m.ollamaProbe.Reachable && len(m.ollamaProbe.Models) > 0:
		in.customModel.SetValue(m.ollamaProbe.Models[0])
		in.chosenModel = m.ollamaProbe.Models[0]
	case e.Kind == "openai-auth":
		const openAIAuthDefault = "gpt-5.5"
		in.customModel.SetValue(openAIAuthDefault)
		in.chosenModel = openAIAuthDefault
	case e.Kind == "copilot":
		const copilotDefault = "claude-haiku-4.5"
		in.customModel.SetValue(copilotDefault)
		in.chosenModel = copilotDefault
	}
	// For curated kinds (anthropic/openai/gemini/xai), source the
	// model list from the curated catalog — the embedded
	// catalog.gen.json, augmented for Gemini from the local models.dev
	// snapshot so the picker offers newly published IDs even when the
	// generated catalog lags. Empty when the maintainer hasn't run
	// `go run ./cmd/yotta-models refresh` yet — in that case we
	// fall back to the free-form textinput so the wizard remains
	// usable without a populated catalog.
	//
	// openai-auth is curated but its model list is per-user and only
	// populated after the post-login scan — so the catalog is
	// intentionally empty here. We deliberately skip Curated() so the
	// UI renders the textinput (pre-filled with gpt-5.5) plus the
	// "discovered after sign-in" hint instead of the misleading
	// "yotta-models refresh" suggestion.
	if catalog.IsCuratedKind(e.Kind) && e.Kind != "openai-auth" && e.Kind != "copilot" {
		in.curatedModels = catalog.Curated(e.Kind)
		if len(in.curatedModels) > 0 {
			in.chosenModel = in.curatedModels[0].ID
		}
	}
	return in
}

// FreeFormModelPlaceholder picks the model-tag hint the textinput
// shows for free-form providers. The default tag shape varies by
// vendor — Anthropic uses "claude-<family>-<version>", OpenAI uses
// flat "gpt-X" / "o-N", Google uses "gemini-<gen>-<size>", Ollama
// uses "<family>:<size>", NIM uses "<org>/<model>". A single shared
// example would mislead more than half. The strings are recent
// shapes, not endorsements — the Note for each provider points to
// the live docs so users get current model names without us
// curating a stale list. Exported so internal/tui/provider_picker.go
// can reuse the same hints in the /provider Add form.
func FreeFormModelPlaceholder(name string) string {
	switch name {
	case "anthropic":
		return "claude-sonnet-4-6"
	case "openai":
		return "gpt-4o"
	case "gemini":
		return "gemini-2.5-flash"
	case "xai":
		return "grok-4.20-0309-non-reasoning"
	case "ollama":
		return "llama3.1:8b"
	case "nvidia-nim":
		return "nvidia/nemotron-3-super-120b-a12b"
	case "copilot-auth":
		return "claude-haiku-4.5"
	case "vertex-claude":
		// Vertex qualifies Claude ids with a version suffix; the bare id
		// is not servable.
		return "claude-sonnet-4-5@20250929"
	case "vertex-gemini":
		// The adapter also accepts bare gemini-* for older configs and
		// normalizes it at request time.
		return "google/gemini-2.5-pro"
	case "custom":
		return "your-model-name"
	}
	return "model tag"
}

// configHeading builds the per-provider configure header.
func (m wizardModel) configHeading() string {
	if m.embedSubStep {
		return "Configure semantic memory"
	}
	if m.configIdx >= len(m.inputs) {
		return "Configure provider"
	}
	in := m.inputs[m.configIdx]
	return fmt.Sprintf("Configure %s  (%d/%d)",
		in.entry.Name, m.configIdx+1, len(m.inputs))
}

// focusActiveConfigField focuses the textinput appropriate for the
// current provider+field combination and returns the cursor-blink
// cmd Focus() yields. Bubbles' textinput.Update is a no-op when
// !focused, so missing this call silently drops every keystroke —
// that's the bug class this function exists to prevent. Callers
// must batch the returned cmd into their tea.Cmd return so the
// cursor renders.
func (m *wizardModel) focusActiveConfigField() tea.Cmd {
	if m.configIdx >= len(m.inputs) {
		return nil
	}
	in := &m.inputs[m.configIdx]
	in.key.Blur()
	in.baseURL.Blur()
	in.project.Blur()
	in.customModel.Blur()
	switch m.fieldKind() {
	case "key":
		return in.key.Focus()
	case "baseURL":
		return in.baseURL.Focus()
	case "project":
		return in.project.Focus()
	case "model":
		// Model pickers drive selection via arrow keys, so no
		// textinput needs focus. Free-form providers still focus
		// customModel so typed model tags are accepted.
		if !m.modelPickerActive(*in) {
			return in.customModel.Focus()
		}
	}
	return nil
}

// fieldKind returns the symbolic kind of the current field, taking
// the provider's nature into account. Custom-baseURL only exists for
// the "custom" entry; key field is suppressed when the provider has
// no APIKeyEnv (Ollama) or the env already has the key.
func (m wizardModel) fieldKind() string {
	if m.configIdx >= len(m.inputs) {
		return ""
	}
	in := m.inputs[m.configIdx]
	switch in.field {
	case 0:
		if isCombinedVertexEntry(in.entry) || in.entry.Kind == "vertex" || in.entry.Kind == "vertex-anthropic" {
			return "family"
		}
		// Providers without API keys usually jump straight to model
		// selection. Vertex is the exception: it has no API key, but
		// the project/location live inside base_url and the catalog
		// entry is only a PROJECT template, so setup must stop on the
		// Base URL field.
		if in.entry.APIKeyEnv == "" || in.envHas {
			if in.entry.Kind == "vertex" || in.entry.Kind == "vertex-anthropic" {
				return "project"
			}
			if providerNeedsBaseURLInput(in.entry) {
				return "baseURL"
			}
			return "model"
		}
		return "key"
	case 1:
		if in.entry.Name == "custom" {
			return "baseURL"
		}
		if isCombinedVertexEntry(in.entry) || in.entry.Kind == "vertex" || in.entry.Kind == "vertex-anthropic" {
			return "project"
		}
		return "model"
	case 2:
		return "model"
	}
	return ""
}

func (m wizardModel) updateConfigure(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.embedSubStep {
		return m.updateEmbedSubStep(msg)
	}
	if m.configIdx >= len(m.inputs) {
		return m, nil
	}
	in := &m.inputs[m.configIdx]
	// When a picker is active (curated cloud providers, or Ollama with
	// detected local models), Down/Up navigate the model list rather
	// than switching wizard fields. Tab/Shift+Tab still switch fields
	// so the user can jump back to key without leaving the screen.
	familyPickerActive := m.familyPickerActive(*in)
	modelPickerActive := m.modelPickerActive(*in)
	switch msg.String() {
	case "tab":
		in.field = nextField(*in)
		return m, m.focusActiveConfigField()
	case "shift+tab":
		in.field = prevField(*in)
		return m, m.focusActiveConfigField()
	case "down":
		if familyPickerActive {
			in.vertexFamily = VertexFamilyClaude
			m.applyVertexFamily(in.vertexFamily)
			return m, nil
		}
		if modelPickerActive {
			if in.modelCursor < len(m.modelPickerIDs(*in))-1 {
				in.modelCursor++
			}
			return m, nil
		}
		in.field = nextField(*in)
		return m, m.focusActiveConfigField()
	case "up":
		if familyPickerActive {
			in.vertexFamily = VertexFamilyGemini
			m.applyVertexFamily(in.vertexFamily)
			return m, nil
		}
		if modelPickerActive {
			if in.modelCursor > 0 {
				in.modelCursor--
			}
			return m, nil
		}
		in.field = prevField(*in)
		return m, m.focusActiveConfigField()
	case "ctrl+s":
		// Skip key for this provider — user expects the key to be
		// in env at runtime even though it isn't right now.
		in.skipKey = true
		in.field = 1
		if in.entry.Name != "custom" && !providerNeedsBaseURLInput(in.entry) {
			in.field = 2
		}
		return m, m.focusActiveConfigField()
	case "enter":
		// Validate before advancing. If on key field, capture key
		// and move to next field. If on last field (model), advance
		// to next provider or step.
		switch m.fieldKind() {
		case "family":
			m.applyVertexFamily(in.vertexFamily)
			m.err = nil
			in.field = 1
			return m, m.focusActiveConfigField()
		case "key":
			if in.entry.APIKeyEnv != "" && !in.envHas && !in.skipKey {
				if strings.TrimSpace(in.key.Value()) == "" {
					m.err = errors.New("API key empty (ctrl+s to skip)")
					return m, nil
				}
			}
			m.err = nil
			in.field = 1
			if in.entry.Name != "custom" && !providerNeedsBaseURLInput(in.entry) {
				in.field = 2 // skip baseURL/project for non-custom providers that do not need it
			}
			focusCmd := m.focusActiveConfigField()
			// Kick off async validation if we got a key. Clear the
			// previous success banner before kicking off — if we don't,
			// the old "✓ key valid" hangs in the chrome while the new
			// key is being checked, which reads like the new key is
			// already approved.
			if v := strings.TrimSpace(in.key.Value()); v != "" && !m.opts.SkipValidation {
				in.validating = true
				in.validation = ValidationResult{}
				m.notice = ""
				provider := in.entry
				return m, tea.Batch(focusCmd, func() tea.Msg {
					res := ValidateKey(m.ctx, provider, v)
					return validationDoneMsg{provider: provider.Name, result: res}
				})
			}
			return m, focusCmd
		case "project":
			project := strings.TrimSpace(in.project.Value())
			if project == "" {
				m.err = errors.New("GCP project ID is required")
				return m, nil
			}
			in.baseURL.SetValue(VertexBaseURL(in.vertexFamily, project))
			m.err = nil
			in.field = 2
			return m, m.focusActiveConfigField()
		case "baseURL":
			if strings.TrimSpace(in.baseURL.Value()) == "" {
				m.err = errors.New("base URL is required")
				return m, nil
			}
			m.err = nil
			in.field = 2
			return m, m.focusActiveConfigField()
		case "model":
			if in.entry.Kind == "vertex" || in.entry.Kind == "vertex-anthropic" {
				project := strings.TrimSpace(in.project.Value())
				if project == "" {
					m.err = errors.New("GCP project ID is required before saving")
					in.field = 1
					return m, m.focusActiveConfigField()
				}
				in.baseURL.SetValue(VertexBaseURL(in.vertexFamily, project))
			}
			// Refuse to mark a provider configured if its API key isn't
			// available somewhere — env, .env via in.key, or explicit
			// Ctrl+S skip. Without this guard the user can Tab past
			// the key field (or have the wizard auto-skip on envHas)
			// and end up with a provider in their config that fails at
			// runtime with a confusing 401. Bounces the cursor back to
			// the key field with an actionable error.
			if in.entry.APIKeyEnv != "" && !in.envHas && !in.skipKey &&
				strings.TrimSpace(in.key.Value()) == "" {
				m.err = errors.New("API key required before saving (ctrl+s to skip and set later)")
				in.field = 0
				return m, m.focusActiveConfigField()
			}
			// Validate model. Providers with a picker commit the
			// highlighted row; everything else takes the textinput value.
			if ids := m.modelPickerIDs(*in); len(ids) > 0 {
				if in.modelCursor < 0 || in.modelCursor >= len(ids) {
					in.modelCursor = 0
				}
				in.chosenModel = ids[in.modelCursor]
			} else {
				name := strings.TrimSpace(in.customModel.Value())
				if name == "" {
					m.err = errors.New("model tag required")
					return m, nil
				}
				in.chosenModel = name
			}
			m.err = nil
			// The success banner from this provider's validation is
			// scoped to this provider — drop it before we advance.
			m.notice = ""
			// Mark this provider configured and return to the provider
			// list so the user can pick another or hit Continue. No
			// more auto-advance to the next provider in a hard-coded
			// loop — the user chooses pace and order.
			if in.entry.Kind == "vertex" || in.entry.Kind == "vertex-anthropic" {
				project := strings.TrimSpace(in.project.Value())
				if project == "" {
					m.err = errors.New("GCP project ID is required before saving")
					in.field = 1
					return m, m.focusActiveConfigField()
				}
				in.baseURL.SetValue(VertexBaseURL(in.vertexFamily, project))
			}
			m.inputs[m.configIdx].configured = true
			if in.entry.Kind == "ollama" && m.ollamaProbe.Reachable {
				detected := DetectEmbeddingModels(m.ollamaProbe.Models)
				if len(detected) > 0 {
					m.embedChosenModel = detected[0]
				}
				m.embedSubStep = true
				m.embedCursor = 0
				m.embedPullErr = nil
				return m, nil
			}
			m.providerCursor = m.configIdx
			m.step = stepProviders
			return m, nil
		}
	}

	// Per-field updates.
	var cmd tea.Cmd
	switch m.fieldKind() {
	case "family":
		switch msg.String() {
		case "left", "h", "up", "k":
			in.vertexFamily = VertexFamilyGemini
			m.applyVertexFamily(in.vertexFamily)
		case "right", "l", "down", "j":
			in.vertexFamily = VertexFamilyClaude
			m.applyVertexFamily(in.vertexFamily)
		}
	case "key":
		in.key, cmd = in.key.Update(msg)
	case "project":
		in.project, cmd = in.project.Update(msg)
	case "baseURL":
		in.baseURL, cmd = in.baseURL.Update(msg)
	case "model":
		// Model pickers handle Up/Down at the top of updateConfigure
		// (so they don't fall through to nextField); j/k vim-style
		// keystrokes still arrive here and drive the cursor too. Free-
		// form providers route everything to customModel.
		if m.modelPickerActive(*in) {
			switch msg.String() {
			case "k":
				if in.modelCursor > 0 {
					in.modelCursor--
				}
			case "j":
				if in.modelCursor < len(m.modelPickerIDs(*in))-1 {
					in.modelCursor++
				}
			}
		} else {
			in.customModel, cmd = in.customModel.Update(msg)
		}
	}
	return m, cmd
}

func (m *wizardModel) applyVertexFamily(f VertexFamily) {
	if m.configIdx >= len(m.inputs) {
		return
	}
	in := &m.inputs[m.configIdx]
	in.vertexFamily = f
	entry := f.CatalogEntry()
	in.entry = entry
	in.baseURL.SetValue(VertexBaseURL(f, strings.TrimSpace(in.project.Value())))
	in.curatedModels = nil
	in.modelCursor = 0
	in.chosenModel = ""
	if catalog.IsCuratedKind(entry.Kind) {
		in.curatedModels = catalog.Curated(entry.Kind)
		if len(in.curatedModels) > 0 {
			in.chosenModel = in.curatedModels[0].ID
		}
	}
}

func (m wizardModel) familyPickerActive(in providerInputs) bool {
	return m.fieldKind() == "family" && (isCombinedVertexEntry(in.entry) || in.entry.Kind == "vertex" || in.entry.Kind == "vertex-anthropic")
}

// modelPickerActive reports whether the current model field is driven
// by arrow-key selection instead of a textinput. Ollama joins curated
// providers here only when the probe returned concrete local models.
func (m wizardModel) modelPickerActive(in providerInputs) bool {
	return m.fieldKind() == "model" && len(m.modelPickerIDs(in)) > 0
}

// modelPickerIDs returns the model IDs currently shown in the default
// model picker. Curated providers use the catalog; Ollama uses the
// live probe so detected local models are selectable instead of being
// hidden in a passive hint below a free-form input.
func (m wizardModel) modelPickerIDs(in providerInputs) []string {
	if len(in.curatedModels) > 0 {
		ids := make([]string, 0, len(in.curatedModels))
		for _, model := range in.curatedModels {
			ids = append(ids, model.ID)
		}
		return ids
	}
	if in.entry.Name == "ollama" && len(m.ollamaProbe.Models) > 0 {
		return m.ollamaProbe.Models
	}
	return nil
}

func effectiveProviderEntry(in providerInputs) CatalogEntry {
	if isCombinedVertexEntry(in.entry) || in.entry.Kind == "vertex" || in.entry.Kind == "vertex-anthropic" {
		return in.vertexFamily.CatalogEntry()
	}
	return in.entry
}

func isCombinedVertexEntry(e CatalogEntry) bool {
	return e.Name == "google-vertex"
}

func providerNeedsBaseURLInput(e CatalogEntry) bool {
	return e.Kind == "vertex" || e.Kind == "vertex-anthropic" || isCombinedVertexEntry(e)
}

func nextField(in providerInputs) int {
	switch in.field {
	case 0:
		if in.entry.Name == "custom" || providerNeedsBaseURLInput(in.entry) {
			return 1
		}
		return 2
	case 1:
		return 2
	case 2:
		return 0
	}
	return 0
}

func prevField(in providerInputs) int {
	switch in.field {
	case 0:
		return 2
	case 1:
		return 0
	case 2:
		if in.entry.Name == "custom" || providerNeedsBaseURLInput(in.entry) {
			return 1
		}
		return 0
	}
	return 0
}

func (m wizardModel) viewConfigure() string {
	if m.embedSubStep {
		return m.viewEmbedSubStep()
	}
	if m.configIdx >= len(m.inputs) {
		return ""
	}
	in := m.inputs[m.configIdx]
	var b strings.Builder
	w := lineWidthFor(m.width)
	b.WriteString("\n")
	kindLabel := "  " + styleMuted.Render("kind:     ")
	b.WriteString(kindLabel + truncate(in.entry.Kind, w-lipgloss.Width(kindLabel)) + "\n")
	urlLabel := "  " + styleMuted.Render("base URL: ")
	if in.entry.Name == "custom" {
		b.WriteString(urlLabel + styleDim.Render("(enter below)") + "\n")
	} else {
		b.WriteString(urlLabel + stylePathHL.Render(truncate(in.entry.BaseURL, w-lipgloss.Width(urlLabel))) + "\n")
	}
	b.WriteString("\n")

	// Vertex family row.
	if isCombinedVertexEntry(in.entry) || strings.HasPrefix(in.entry.Name, "vertex-") {
		b.WriteString("  " + fieldHeading("Model family", m.fieldKind() == "family"))
		b.WriteString("\n")
		families := []VertexFamily{VertexFamilyGemini, VertexFamilyClaude}
		for _, family := range families {
			cursor := "    "
			if m.fieldKind() == "family" && in.vertexFamily == family {
				cursor = "  ▸ "
			}
			label := cursor + family.Label()
			if in.vertexFamily == family {
				label += " ✓"
			}
			b.WriteString(styleDim.Render(truncate(label, w-2)))
			b.WriteString("\n")
		}
		b.WriteString("  ")
		b.WriteString(styleDim.Render(truncate("↑↓ choose Gemini or Claude; each uses a different Vertex API", w-2)))
		b.WriteString("\n\n")
	}

	// Key row.
	if in.entry.APIKeyEnv != "" {
		b.WriteString("  " + fieldHeading("API key", m.fieldKind() == "key"))
		b.WriteString("\n  ")
		switch {
		case in.envHas:
			line := "✓ found in env $" + in.entry.APIKeyEnv + " — using as-is"
			b.WriteString(styleOK.Render(truncate(line, w-2)))
		case in.skipKey:
			line := "• skipped — set $" + in.entry.APIKeyEnv + " before launching"
			b.WriteString(styleHint.Render(truncate(line, w-2)))
		default:
			b.WriteString(in.key.View())
			b.WriteString("  ")
			b.WriteString(validationGlyph(in, m.spin.View()))
		}
		b.WriteString("\n  ")
		b.WriteString(styleDim.Render(truncate("env var: $"+in.entry.APIKeyEnv, w-2)))
		// Provider-specific contextual help on validation failure. NIM
		// keys minted from build.nvidia.com are commonly scoped to a
		// single model, so /chat/completions returns 404 when the key
		// is valid but doesn't include the requested model — and our
		// /v1/models probe sometimes also 404s on those keys. Surface
		// the explanation here at the point of failure rather than as
		// a preemptive warning on the provider-pick screen.
		if in.entry.Name == "nvidia-nim" && in.validation.HTTPStatus == 404 {
			b.WriteString("\n  ")
			b.WriteString(styleDim.Render(truncate("NVIDIA NIM keys are often per-model. Check build.nvidia.com.", w-2)))
		}
		b.WriteString("\n\n")
	}

	// Project row for Vertex. The full base_url is derived from the
	// project id and selected family so users do not have to edit a long
	// endpoint by hand.
	if in.entry.Kind == "vertex" || in.entry.Kind == "vertex-anthropic" {
		b.WriteString("  " + fieldHeading("GCP project", m.fieldKind() == "project"))
		b.WriteString("\n  ")
		b.WriteString(in.project.View())
		b.WriteString("\n")
		project := strings.TrimSpace(in.project.Value())
		b.WriteString("  ")
		b.WriteString(styleDim.Render(truncate("full URL: "+VertexBaseURL(in.vertexFamily, project), w-2)))
		b.WriteString("\n\n")
	}

	// Custom base URL row. Vertex derives its full URL from the GCP
	// project field above, so users do not edit the raw endpoint.
	if in.entry.Name == "custom" {
		b.WriteString("  " + fieldHeading("Base URL", m.fieldKind() == "baseURL"))
		b.WriteString("\n  ")
		b.WriteString(in.baseURL.View())
		b.WriteString("\n\n")
	}

	// Model row.
	b.WriteString("  " + fieldHeading("Default model", m.fieldKind() == "model"))
	b.WriteString("\n")
	if len(in.curatedModels) > 0 {
		// Compute the largest model-name pad that still leaves room
		// for the cursor + capability chips + context-window label on
		// this terminal width. Falls back to 28 (the historic value)
		// on wide terminals and shrinks gracefully on narrow ones —
		// never wider than the longest name in the list.
		nameW := curatedModelNameColumnWidth(in.curatedModels, lineWidthFor(m.width))
		for i, model := range in.curatedModels {
			label := truncate(model.Label(), nameW)
			selected := i == in.modelCursor && m.fieldKind() == "model"
			chips := wizardCapChips(model.Capabilities) // plain text already
			ctxLabel := ""
			if model.ContextWindow > 0 {
				ctxLabel = fmt.Sprintf("ctx %dK", model.ContextWindow/1000)
			}
			if selected {
				// Cursor row: plain content, full-row bar.
				content := label
				if pad := nameW - lipgloss.Width(label); pad > 0 {
					content += strings.Repeat(" ", pad)
				}
				if chips != "" {
					content += "  " + chips
				}
				if ctxLabel != "" {
					content += "  " + ctxLabel
				}
				b.WriteString(m.renderRowBar(content) + "\n")
			} else {
				name := lipgloss.NewStyle().Width(nameW).Render(label)
				line := name
				if chips != "" {
					line += "  " + styleMuted.Render(chips)
				}
				if ctxLabel != "" {
					line += "  " + styleMuted.Render(ctxLabel)
				}
				b.WriteString("  " + truncate(line, lineWidthFor(m.width)) + "\n")
			}
		}
	} else if in.entry.Name == "ollama" && len(m.ollamaProbe.Models) > 0 {
		for i, model := range m.ollamaProbe.Models {
			selected := i == in.modelCursor && m.fieldKind() == "model"
			label := truncate(model, w-2)
			if selected {
				b.WriteString(m.renderRowBar(label) + "\n")
				continue
			}
			b.WriteString("  " + label + "\n")
		}
	} else {
		b.WriteString("  ")
		b.WriteString(in.customModel.View())
		switch {
		case in.entry.Kind == "openai-auth":
			b.WriteString("\n  ")
			b.WriteString(styleHint.Render("(your full model list is discovered after browser sign-in below — gpt-5.5 is the universal default)"))
		case in.entry.Kind == "copilot":
			b.WriteString("\n  ")
			b.WriteString(styleHint.Render("(your model list is cached after device code sign-in — run `yottacode copilot-auth login` after setup)"))
		case catalog.IsCuratedKind(in.entry.Kind):
			b.WriteString("\n  ")
			b.WriteString(styleHint.Render("(catalog empty — run `go run ./cmd/yotta-models refresh` to populate)"))
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Embedding model sub-step (shown after Ollama provider configuration)
// ---------------------------------------------------------------------------

func (m wizardModel) updateEmbedSubStep(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.embedPulling {
		return m, nil
	}

	// Detected mode: let the same picker choose whether to enable the
	// already-installed embedding model or skip semantic search.
	if m.embedChosenModel != "" && m.embedPullErr == nil {
		switch msg.String() {
		case "up", "k":
			if m.embedCursor > 0 {
				m.embedCursor--
			}
		case "down", "j":
			if m.embedCursor < 1 {
				m.embedCursor++
			}
		case "enter", "y":
			if m.embedCursor == 1 {
				m.embedChosenModel = ""
			}
			m.embedSubStep = false
			m.providerCursor = m.configIdx
			m.step = stepProviders
			return m, nil
		case "n":
			m.embedChosenModel = ""
			m.embedSubStep = false
			m.providerCursor = m.configIdx
			m.step = stepProviders
			return m, nil
		}
		return m, nil
	}

	// Pull error: enter retries the same model, esc skips.
	if m.embedPullErr != nil {
		switch msg.String() {
		case "enter":
			model := KnownEmbeddingModels[m.embedCursor].Name
			m.embedPulling = true
			m.embedPullErr = nil
			baseURL := m.ollamaProbe.BaseURL
			return m, tea.Batch(m.spin.Tick, func() tea.Msg {
				err := OllamaPull(m.ctx, baseURL, model)
				return embedPullDoneMsg{model: model, err: err}
			})
		case "esc":
			m.embedPullErr = nil
			m.embedSubStep = false
			m.providerCursor = m.configIdx
			m.step = stepProviders
			return m, nil
		}
		return m, nil
	}

	// Picker mode: 3 rows.
	switch msg.String() {
	case "up", "k":
		if m.embedCursor > 0 {
			m.embedCursor--
		}
	case "down", "j":
		if m.embedCursor < 2 {
			m.embedCursor++
		}
	case "enter":
		if m.embedCursor == 2 {
			m.embedSubStep = false
			m.providerCursor = m.configIdx
			m.step = stepProviders
			return m, nil
		}
		model := KnownEmbeddingModels[m.embedCursor].Name
		m.embedPulling = true
		m.embedPullErr = nil
		baseURL := m.ollamaProbe.BaseURL
		return m, tea.Batch(m.spin.Tick, func() tea.Msg {
			err := OllamaPull(m.ctx, baseURL, model)
			return embedPullDoneMsg{model: model, err: err}
		})
	}
	return m, nil
}

func (m wizardModel) viewEmbedSubStep() string {
	var b strings.Builder
	w := lineWidthFor(m.width)

	if m.embedPulling {
		b.WriteString("\n  ")
		b.WriteString(m.spin.View())
		b.WriteString(" ")
		b.WriteString(truncate("pulling embedding model...", w-4))
		b.WriteString("\n\n  ")
		b.WriteString(styleHint.Render(truncate("This may take a minute for the first download.", w-2)))
		b.WriteString("\n")
		return b.String()
	}

	if m.embedPullErr != nil {
		b.WriteString("\n  ")
		b.WriteString(styleErr.Render(truncate("pull failed: "+m.embedPullErr.Error(), w-2)))
		b.WriteString("\n\n  ")
		b.WriteString(styleHint.Render(truncate("Enter to retry, esc to skip.", w-2)))
		b.WriteString("\n")
		return b.String()
	}

	if m.embedChosenModel != "" {
		b.WriteString("\n  ")
		b.WriteString(styleOK.Render("found: ") + truncate(m.embedChosenModel, w-10))
		b.WriteString("\n\n  ")
		b.WriteString(truncate("Enable semantic memory search?", w-2))
		b.WriteString("\n  ")
		b.WriteString(styleHint.Render(truncate("Uses the detected local embedding model for relevance-scored memory.", w-2)))
		b.WriteString("\n\n")
		rows := []struct {
			label string
			desc  string
		}{
			{"Yes", m.embedChosenModel},
			{"Skip", "use keyword search only (BM25)"},
		}
		for i, r := range rows {
			labelStyle := lipgloss.NewStyle()
			if i == m.embedCursor {
				b.WriteString(m.renderRowBar(r.label+"  "+r.desc) + "\n")
				continue
			}
			line := labelStyle.Render(r.label) + "  " + styleMuted.Render(r.desc)
			b.WriteString("  " + truncate(line, w-2) + "\n")
		}
		return b.String()
	}

	// Picker mode.
	b.WriteString("\n  ")
	b.WriteString(truncate("Enable semantic memory search?", w-2))
	b.WriteString("\n  ")
	b.WriteString(styleHint.Render(truncate("Uses a small local embedding model for relevance-scored memory.", w-2)))
	b.WriteString("\n\n")

	type row struct {
		label string
		desc  string
	}
	rows := []row{
		{KnownEmbeddingModels[0].Name, KnownEmbeddingModels[0].Desc + " · " + KnownEmbeddingModels[0].Size},
		{KnownEmbeddingModels[1].Name, KnownEmbeddingModels[1].Desc + " · " + KnownEmbeddingModels[1].Size},
		{"Skip", "use keyword search only (BM25)"},
	}
	for i, r := range rows {
		cursor := "  "
		// Non-cursor labels render in the terminal default foreground
		// (bright, like the Welcome page) so every option is easy to
		// spot — a dim label here was getting overlooked entirely.
		labelStyle := lipgloss.NewStyle()
		if i == m.embedCursor {
			cursor = styleSelected.Render("> ")
			labelStyle = styleSelected
		}
		line := cursor + labelStyle.Render(r.label) + "  " + styleMuted.Render(r.desc)
		b.WriteString(truncate(line, w) + "\n")
	}
	return b.String()
}

// wizardCapChips renders the "thinking · vision · pdf"-style chip
// row for a curated model. Same semantics as the TUI picker: only
// known-true capabilities are shown; unknown/false stay silent.
func wizardCapChips(c catalog.Capabilities) string {
	parts := make([]string, 0, 5)
	if c.Thinking != nil && *c.Thinking {
		parts = append(parts, "thinking")
	}
	if c.Vision != nil && *c.Vision {
		parts = append(parts, "vision")
	}
	if c.PDF != nil && *c.PDF {
		parts = append(parts, "pdf")
	}
	if c.StructuredOutputs != nil && *c.StructuredOutputs {
		parts = append(parts, "json")
	}
	if c.Tools != nil && *c.Tools {
		parts = append(parts, "tools")
	}
	return strings.Join(parts, " · ")
}

// curatedModelNameColumnWidth picks a column width for the
// model-name cell in the picker. Tries the widest actual label in
// the list, capped at half the available line width so the chip
// and ctx columns still have room. Floor of 12 prevents
// single-char column silliness on extremely narrow terminals.
func curatedModelNameColumnWidth(models []catalog.Model, lineW int) int {
	const floor = 12
	const cap = 28
	widest := 0
	for _, m := range models {
		if len(m.Label()) > widest {
			widest = len(m.Label())
		}
	}
	limit := lineW / 2
	if limit > cap {
		limit = cap
	}
	if widest > limit {
		widest = limit
	}
	if widest < floor {
		widest = floor
	}
	return widest
}

func fieldHeading(label string, active bool) string {
	if active {
		return styleSelected.Render("▸ " + label)
	}
	return styleMuted.Render("  " + label)
}

func validationGlyph(in providerInputs, spinView string) string {
	if in.validating {
		return styleHint.Render(spinView + " checking…")
	}
	switch in.validation.Status {
	case ValidationOK:
		// Replace the bare "ok" detail (from ValidateKey) with a label
		// the user can scan. Keep upstream details (e.g. provider-
		// specific notes) when present and non-trivial.
		label := "key valid"
		if d := in.validation.Detail; d != "" && d != "ok" {
			label = "key valid · " + d
		}
		return styleOK.Render("✓ " + label)
	case ValidationFailed:
		return styleErr.Render("✗ " + in.validation.Detail)
	}
	if in.validation.Detail != "" {
		return styleDim.Render("· " + in.validation.Detail)
	}
	return styleDim.Render("  enter validate")
}

// =============================================================================
// stepActive
// =============================================================================

func (m wizardModel) updateActive(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	choices := m.activeChoices()
	switch msg.String() {
	case "up", "k":
		if m.activeCursor > 0 {
			m.activeCursor--
		}
	case "down", "j":
		if m.activeCursor < len(choices)-1 {
			m.activeCursor++
		}
	case "enter":
		if len(choices) == 0 {
			m.err = errors.New("no provider:model combos available")
			return m, nil
		}
		m.err = nil
		// Skip stepRouter — see comment on totalSteps. Reintroducing the
		// router screen is a one-line change here plus restoring its
		// stepNumber mapping.
		m.finalPlan = m.assemblePlan()
		m.reviewBody = m.finalPlan.RenderTOML()
		m.reviewCursor = 0
		m.step = stepReview
	}
	return m, nil
}

func (m wizardModel) activeChoices() [][2]string {
	out := [][2]string{}
	for _, in := range m.inputs {
		// configured is the inclusion test now — m.inputs is one slot
		// per Catalog entry, so iterating without the filter would
		// surface providers the user hasn't touched.
		if in.configured && in.chosenModel != "" {
			entry := effectiveProviderEntry(in)
			out = append(out, [2]string{entry.Name, in.chosenModel})
		}
	}
	return out
}

func (m wizardModel) viewActive() string {
	choices := m.activeChoices()
	var b strings.Builder
	w := lineWidthFor(m.width)
	b.WriteString("\n  " + truncate("Pick the default — the provider:model that loads on launch.", w) + "\n\n")
	for i, ch := range choices {
		// Truncate the model tail (which is the variable-length
		// piece) so the line as a whole fits in the budget.
		// Provider names are short (<= 12 chars), so we can cap the
		// model tail at w - 4 - len(provider) - 1 (colon + padding).
		tail := truncate(ch[1], w-4-len(ch[0])-1)
		if i == m.activeCursor {
			b.WriteString(m.renderRowBar(ch[0]+":"+tail) + "\n")
		} else {
			fmt.Fprintf(&b, "  %s%s\n",
				styleSelected.Render(ch[0]),
				styleMuted.Render(":"+tail))
		}
	}
	return b.String()
}

// =============================================================================
// stepRouter
// =============================================================================

func (m wizardModel) updateRouter(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.routerCursor > 0 {
			m.routerCursor--
		}
	case "down", "j":
		if m.routerCursor < 2 {
			m.routerCursor++
		}
	case "enter":
		switch m.routerCursor {
		case 0:
			m.routerEnabled = false
		case 1:
			m.routerEnabled = true
			m.routerPolicy = "fallback-chain"
		case 2:
			m.routerEnabled = true
			m.routerPolicy = "cheap-first"
		}
		// Skip the router step entirely if the user picked "off" with
		// only one provider; nothing to fall back to.
		if m.routerEnabled && len(m.inputs) < 2 {
			m.err = errors.New("router needs at least two providers")
			return m, nil
		}
		m.err = nil
		m.finalPlan = m.assemblePlan()
		m.reviewBody = m.finalPlan.RenderTOML()
		m.reviewCursor = 0
		m.step = stepReview
	}
	return m, nil
}

func (m wizardModel) viewRouter() string {
	var b strings.Builder
	w := lineWidthFor(m.width)
	b.WriteString("\n  " + truncate("Multi-provider router falls through on early failure.", w) + "\n  ")
	b.WriteString(styleHint.Render(truncate("Skip this if you're not sure — you can /provider edit later.", w)))
	b.WriteString("\n\n")
	options := []struct {
		label string
		desc  string
	}{
		{"Off", "single active provider only"},
		{"Fallback chain", "try in declared order; fall through on early failure"},
		{"Cheap first", "sort by tier (cheap → balanced → expensive); fall through on failure"},
	}
	// Pad labels to a fixed column so descriptions line up regardless
	// of which row is the cursor. 2 leading indent + label column +
	// 2-char gap, then description truncated to fit.
	labelW := 0
	for _, o := range options {
		if w := lipgloss.Width(o.label); w > labelW {
			labelW = w
		}
	}
	for i, o := range options {
		pad := strings.Repeat(" ", labelW-lipgloss.Width(o.label))
		descBudget := w - 4 - labelW
		if descBudget < 4 {
			descBudget = 4
		}
		desc := truncate(o.desc, descBudget)
		if i == m.routerCursor {
			b.WriteString(m.renderRowBar(o.label+pad+"  "+desc) + "\n")
		} else {
			// Default-foreground label (bright, like the Welcome page);
			// muted — not dim — description so both stay legible.
			fmt.Fprintf(&b, "  %s%s  %s\n",
				o.label, pad,
				styleMuted.Render(desc))
		}
	}
	return b.String()
}

// =============================================================================
// stepReview
// =============================================================================

func (m wizardModel) updateReview(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.reviewCursor > 0 {
			m.reviewCursor--
		}
	case "down", "j", "tab":
		if m.reviewCursor < 2 {
			m.reviewCursor++
		}
	case "shift+tab":
		if m.reviewCursor > 0 {
			m.reviewCursor--
		}
	case "y":
		return m.startWrite()
	case "enter":
		switch m.reviewCursor {
		case 0:
			return m.startWrite()
		case 1:
			m.step = stepActive
		case 2:
			m.aborted = true
			return m, tea.Quit
		}
	case "n":
		m.aborted = true
		return m, tea.Quit
	case "b":
		m.step = stepActive
	}
	return m, nil
}

// startWrite transitions from the review screen to the async disk-write
// phase. It is shared by the legacy y shortcut and the focused action
// picker so both paths run exactly the same write code.
func (m wizardModel) startWrite() (tea.Model, tea.Cmd) {
	m.step = stepWriting
	plan := m.finalPlan
	opts := m.opts
	return m, tea.Batch(m.spin.Tick, func() tea.Msg {
		oc, err := Apply(plan, WriteOptions{
			ConfigPath:   opts.ConfigPath,
			EnvPath:      opts.EnvPath,
			Force:        opts.Force,
			SkipEnvWrite: opts.SkipEnvWrite || plan.SkipEnvWrite,
		})
		return writeDoneMsg{outcome: oc, err: err}
	})
}

func (m wizardModel) viewReview() string {
	var b strings.Builder
	w := lineWidthFor(m.width)
	b.WriteString("\n  " + truncate("Review what will be written:", w) + "\n\n")

	// Summary chips. Each row's variable tail (a path or a comma-
	// joined list) gets truncated to fit the remaining budget after
	// the styled "config: " / ".env:   " / "active: " label, which
	// is rendered by lipgloss so we measure it with lipgloss.Width
	// rather than len() to skip ANSI escapes.
	configLabel := "  " + styleMuted.Render("config: ")
	b.WriteString(configLabel + stylePathHL.Render(truncate(m.opts.ConfigPath, w-lipgloss.Width(configLabel))) + "\n")

	envLabel := "  " + styleMuted.Render(".env:   ")
	if !m.finalPlan.SkipEnvWrite && len(m.finalPlan.EnvKeys) > 0 {
		count := styleDim.Render(fmt.Sprintf("  (%d key(s), mode 0600)", len(m.finalPlan.EnvKeys)))
		budget := w - lipgloss.Width(envLabel) - lipgloss.Width(count)
		b.WriteString(envLabel + stylePathHL.Render(truncate(m.opts.EnvPath, budget)) + count + "\n")
	} else {
		b.WriteString(envLabel +
			styleDim.Render(truncate("not written (every key already in env)", w-lipgloss.Width(envLabel))) + "\n")
	}

	activeLabel := "  " + styleMuted.Render("active: ")
	activeText := m.finalPlan.ActiveProvider + ":" + m.finalPlan.ActiveModel
	b.WriteString(activeLabel + styleSelected.Render(truncate(activeText, w-lipgloss.Width(activeLabel))) + "\n")

	if m.finalPlan.EnableRouter {
		routerLabel := "  " + styleMuted.Render("router: ")
		policy := styleSelected.Render(m.finalPlan.RouterPolicy)
		dash := " — "
		budget := w - lipgloss.Width(routerLabel) - lipgloss.Width(policy) - len(dash)
		tail := styleDim.Render(dash + truncate(strings.Join(m.finalPlan.RouterCandList, ", "), budget))
		b.WriteString(routerLabel + policy + tail + "\n")
	}
	if m.finalPlan.EmbeddingModel != "" {
		searchLabel := "  " + styleMuted.Render("search: ")
		searchText := "semantic (" + m.finalPlan.EmbeddingModel + ")"
		b.WriteString(searchLabel + styleSelected.Render(truncate(searchText, w-lipgloss.Width(searchLabel))) + "\n")
	}
	if m.existingCfg && !m.opts.Force {
		b.WriteString("  " + styleHint.Render(truncate("merging into existing config — tunables (context / retrieval) preserved", w)) + "\n\n")
	}
	if m.existingCfg && m.opts.Force {
		b.WriteString("  " + styleWarn.Render(truncate("--force: existing config will be backed up to *.bak-<ts>", w)) + "\n\n")
	}
	b.WriteString("\n  " + styleHeading.Render("Action") + "\n")
	actions := []struct {
		label string
		desc  string
	}{
		{"Write config", "save config.toml and any entered keys"},
		{"Back", "return to default model selection"},
		{"Abort", "exit without writing"},
	}
	for i, action := range actions {
		line := action.label + "  " + action.desc
		if i == m.reviewCursor {
			b.WriteString(m.renderRowBar(line) + "\n")
			continue
		}
		budget := w - lipgloss.Width(action.label) - 4
		b.WriteString("  " + action.label + "  " + styleMuted.Render(truncate(action.desc, budget)) + "\n")
	}
	b.WriteString("\n")

	// File preview.
	b.WriteString("  " + styleHeading.Render("config.toml preview") + "\n")
	b.WriteString(indentPreview(m.reviewBody, "    ", previewWidthFor(m.width)))
	return b.String()
}

func boolWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func indentPreview(body, indent string, width int) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(indent)
		b.WriteString(styleDim.Render(truncate(l, width)))
		b.WriteString("\n")
	}
	return b.String()
}

// =============================================================================
// stepDone
// =============================================================================

func (m wizardModel) viewDoneSummary() string {
	var b strings.Builder
	w := lineWidthFor(m.width)
	b.WriteString("\n  ")
	b.WriteString(styleOK.Render("✓") + " setup complete.\n\n")
	// Each path line is "    • <verb>     → <path>"; the verb +
	// arrow chunk is fixed-width, so the path is the variable
	// piece we truncate. pathBudget = w - prefix length, where
	// prefix is "    • backed up → " (= 19 visible chars; bullet
	// counts as 1).
	const pathPrefix = 19
	if m.outcome.WroteBackup {
		fmt.Fprintf(&b, "    %s backed up → %s\n",
			styleMuted.Render("•"),
			stylePathHL.Render(truncate(m.outcome.BackupPath, w-pathPrefix)))
	}
	if m.outcome.WroteConfig {
		fmt.Fprintf(&b, "    %s wrote     → %s\n",
			styleMuted.Render("•"),
			stylePathHL.Render(truncate(m.outcome.ConfigPath, w-pathPrefix)))
	}
	if m.outcome.WroteEnv {
		mode := styleDim.Render("(0600)")
		fmt.Fprintf(&b, "    %s wrote     → %s %s\n",
			styleMuted.Render("•"),
			stylePathHL.Render(truncate(m.outcome.EnvPath, w-pathPrefix-lipgloss.Width(mode)-1)),
			mode)
		// Tell the user yottacode reads keys from this file
		// automatically. Without this, users assume they need to keep
		// "export ANTHROPIC_API_KEY=…" in their shell rc forever and
		// get a worse story than the wizard set up for them.
		hint := "yottacode reads keys from this file — no need to export them in your shell."
		b.WriteString("      " + styleHint.Render(truncate(hint, w-6)) + "\n")
	}
	// openai-auth runs the OAuth flow inline as stepOpenAIAuthLogin,
	// then a model scan as stepOpenAIAuthScan, before reaching here.
	// Three outcomes drive the summary:
	//   - login failed → tell the user how to retry
	//   - login OK but scan failed → token saved, model list missing
	//   - both OK → list the discovered models
	for _, ch := range m.activeChoices() {
		if ch[0] != "openai-auth" {
			continue
		}
		switch {
		case m.openAIAuthErr != nil:
			b.WriteString("\n  ")
			b.WriteString(styleErr.Render("openai-auth sign-in failed: " + m.openAIAuthErr.Error()))
			b.WriteString("\n  ")
			b.WriteString(styleHint.Render("The openai-auth profile was rolled back from "))
			b.WriteString(stylePathHL.Render(m.outcome.ConfigPath))
			b.WriteString(styleHint.Render(" — re-run "))
			b.WriteString(styleSelected.Render("yottacode setup"))
			b.WriteString(styleHint.Render(" to retry."))
			b.WriteString("\n")
		case m.openAIAuthScanErr != nil:
			b.WriteString("\n  ")
			b.WriteString(styleOK.Render("✓"))
			b.WriteString(" ")
			b.WriteString(styleHint.Render("signed in — token saved to "))
			b.WriteString(stylePathHL.Render("~/.yottacode/auth/openai-auth.json"))
			b.WriteString("\n  ")
			b.WriteString(styleErr.Render("⚠ model scan failed: " + m.openAIAuthScanErr.Error()))
			b.WriteString("\n  ")
			b.WriteString(styleHint.Render("Re-run "))
			b.WriteString(styleSelected.Render("yottacode openai-auth login"))
			b.WriteString(styleHint.Render(" to retry; until then the picker shows no models."))
			b.WriteString("\n")
		default:
			b.WriteString("\n  ")
			b.WriteString(styleOK.Render("✓"))
			b.WriteString(" ")
			b.WriteString(styleHint.Render("openai-auth signed in — token saved to "))
			b.WriteString(stylePathHL.Render("~/.yottacode/auth/openai-auth.json"))
			b.WriteString("\n")
			if len(m.openAIAuthModels) > 0 {
				b.WriteString("  ")
				b.WriteString(styleOK.Render("✓"))
				b.WriteString(" ")
				b.WriteString(styleHint.Render(fmt.Sprintf("%d models available: ", len(m.openAIAuthModels))))
				b.WriteString(styleSelected.Render(strings.Join(m.openAIAuthModels, ", ")))
				b.WriteString("\n")
			}
		}
		break
	}
	b.WriteString("\n  Launch with: ")
	b.WriteString(styleSelected.Render("yottacode"))
	b.WriteString("\n")
	return b.String()
}

// =============================================================================
// Plan assembly
// =============================================================================

// assemblePlan turns the wizard's collected inputs into a Plan ready
// for review and write. Pulls keys into EnvKeys for providers whose
// env wasn't already set; flips skipKey ones into "the user expects
// the key in env later" semantics by leaving them out of EnvKeys.
func (m wizardModel) assemblePlan() Plan {
	choices := m.activeChoices()
	plan := Plan{
		EnvKeys:      map[string]string{},
		SkipEnvWrite: m.opts.SkipEnvWrite,
	}
	for _, in := range m.inputs {
		// inputs spans every catalog entry now; only the configured
		// ones become provider blocks in the plan. Without this filter
		// the wizard would write empty/incomplete entries for every
		// catalog row the user didn't touch.
		if !in.configured {
			continue
		}
		entry := effectiveProviderEntry(in)
		pp := PlanProvider{
			Name:         entry.Name,
			Kind:         entry.Kind,
			BaseURL:      entry.BaseURL,
			APIKeyEnv:    entry.APIKeyEnv,
			DefaultModel: in.chosenModel,
		}
		// Custom and Vertex providers derive their base URL from fields the
		// user controls in setup. Custom uses the raw URL field; Vertex uses
		// the GCP project field plus the selected Gemini/Claude family.
		if (entry.Name == "custom" || entry.Kind == "vertex" || entry.Kind == "vertex-anthropic") && strings.TrimSpace(in.baseURL.Value()) != "" {
			pp.BaseURL = strings.TrimSpace(in.baseURL.Value())
		}
		// pp.Models intentionally not populated. The interactive
		// wizard no longer authors per-provider model lists into
		// config.toml; the curated list lives in
		// internal/catalog/catalog.gen.json and the picker reads it
		// at /model time.
		// Promote env presence.
		pp.EnvAlreadySet = in.envHas
		// Persist key only when the user actually entered one.
		if entry.APIKeyEnv != "" && !in.envHas && !in.skipKey {
			if v := strings.TrimSpace(in.key.Value()); v != "" {
				plan.EnvKeys[entry.APIKeyEnv] = v
			}
		}
		plan.Providers = append(plan.Providers, pp)
	}
	if len(choices) > 0 && m.activeCursor < len(choices) {
		plan.ActiveProvider = choices[m.activeCursor][0]
		plan.ActiveModel = choices[m.activeCursor][1]
	}
	if m.routerEnabled {
		plan.EnableRouter = true
		plan.RouterPolicy = m.routerPolicy
		for _, in := range m.inputs {
			if in.configured && in.chosenModel != "" {
				entry := effectiveProviderEntry(in)
				plan.RouterCandList = append(plan.RouterCandList,
					entry.Name+":"+in.chosenModel)
			}
		}
	}
	if m.embedChosenModel != "" {
		plan.RetrievalStrategy = "auto"
		plan.EmbeddingModel = m.embedChosenModel
	}
	return plan
}

// =============================================================================
// kicked-off-but-unused helpers, kept for future enhancements
// =============================================================================

// asyncTimer is reserved for future per-step time budgets (e.g.
// auto-advance after probe completes). Not currently wired.
func asyncTimer(_ time.Duration, _ tea.Msg) tea.Cmd {
	return nil
}

var _ = asyncTimer

// inputWidthFor maps the terminal's reported width to a textinput
// Width that's comfortable to type into. Floor of 20 keeps the
// input usable on very narrow terminals (without it, a 40-col
// terminal would render a 1-2 char input box). Ceiling of 80
// stops the input from stretching across an ultra-wide monitor —
// API keys are short, URLs are mid-length; a textbox wider than
// 80 looks misshapen and makes the layout feel sparse.
//
// The 8 deducted from terminalWidth covers the screen's leading
// gutter ("  ") plus the input's own "> " prefix plus a small
// trailing margin so cursor edge cases never push the cell off
// screen. Returns the floor when terminalWidth is 0 (the very
// first frame, before WindowSizeMsg lands).
func inputWidthFor(terminalWidth int) int {
	const min = 20
	const max = 80
	if terminalWidth <= 0 {
		return 48
	}
	w := terminalWidth - 8
	if w < min {
		return min
	}
	if w > max {
		return max
	}
	return w
}

// previewWidthFor mirrors inputWidthFor but for the review-screen
// TOML preview. The preview lives inside an indented gutter, so the
// usable width is the terminal width minus the indent (4 chars).
// We cap at 100 for readability — TOML lines longer than that are
// almost always a base URL or a key=value with a very long string,
// neither of which gets clearer past 100 columns.
func previewWidthFor(terminalWidth int) int {
	const min = 30
	const max = 100
	if terminalWidth <= 0 {
		return 80
	}
	w := terminalWidth - 6 // 4-char indent + 2-char margin
	if w < min {
		return min
	}
	if w > max {
		return max
	}
	return w
}

// renderRowBar wraps plain content in the green option-selected bar
// at full row width (minus the 2-char left gutter). Mirrors the chat
// TUI's slash palette: the whole row gets painted, not just the
// label. Content must be plain text — inline ANSI escapes inside
// would reset the bar's background. Truncates content to fit.
func (m wizardModel) renderRowBar(content string) string {
	barW := lineWidthFor(m.width) - 2
	if barW < 4 {
		barW = 4
	}
	return "  " + styleOptionSelected.Width(barW).Render(truncate(content, barW))
}

// lineWidthFor returns the budget for one rendered line in the
// wizard body. Body lines sit under the standard "  " left gutter
// the chrome adds, with a 2-char right margin for safety against
// off-by-one terminals. Floor of 30 keeps lines from clipping to
// near-illegible widths on extremely narrow terminals; ceiling of
// 110 prevents the wizard from feeling sparse on ultra-wide
// screens. Returns the historic 76 default when terminalWidth is
// 0 (pre-WindowSizeMsg first paint).
func lineWidthFor(terminalWidth int) int {
	const min = 30
	const max = 110
	if terminalWidth <= 0 {
		return 76
	}
	w := terminalWidth - 4 // 2-char left gutter + 2-char right margin
	if w < min {
		return min
	}
	if w > max {
		return max
	}
	return w
}

// truncate cuts s to width runes, appending "…" when the cut
// happened. Used to keep the review preview from overflowing on
// narrow terminals — a wrapped line would smear the section
// alignment we render with fixed-width labels.
func truncate(s string, width int) string {
	if width <= 1 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-1]) + "…"
}
