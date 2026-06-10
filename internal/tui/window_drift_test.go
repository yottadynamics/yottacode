package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
)

// driftTestModel wires a Model whose active provider is a synthetic
// kind/model pair that exists in no embedded catalog, baseline, or
// models.dev source — resolution falls to the default window until a
// drift pin lands, so tests observe exactly what drift wrote. Each
// test passes a unique model name: the window store is process-global,
// and key collisions across tests would let one test's pin leak into
// another's assertions.
func driftTestModel(t *testing.T, model string, defaultWindow int) Model {
	t.Helper()
	m := newTestModel(t)
	m.fileCfg = config.Default()
	m.fileCfg.Context.WarnThreshold = 0.65
	m.fileCfg.Context.AutoThreshold = 0.85
	m.fileCfg.Context.DefaultWindow = defaultWindow
	m.fileCfg.Active.Provider = "p"
	m.fileCfg.Providers = []config.Provider{{Name: "p", Kind: "testkind", DefaultModel: model}}
	m.modelName = model
	return m
}

// TestWindowDrift_ShrinkAndRecoverOnOverflow is the end-to-end test
// for the incident this feature exists for: a context-overflow
// rejection the estimator didn't predict must (a) pin a smaller
// provider-qualified window in the overlay and (b) heal in the same
// tick — the turn-end watermark check resolves the corrected window
// and fires auto-summarize, instead of letting the next send fail too.
func TestWindowDrift_ShrinkAndRecoverOnOverflow(t *testing.T) {
	const model = "drift-shrink-x"
	m := driftTestModel(t, model, 100_000)
	m.sess.Messages = []adapter.Message{
		{Role: adapter.RoleUser, Content: strings.Repeat("x", 60_000)}, // ~15K tokens, well under 100K
	}
	estimate := contextwindow.EstimateTokens(m.sess.Messages)
	wantPin := int(float64(estimate) * driftShrinkFactor)

	overflow := errors.New("openai-auth: context_length_exceeded: Your input exceeds the context window of this model.")
	out, cmd := m.update(turnEndedMsg{err: overflow})
	mm := out.(Model)

	if got := catalog.ResolveWindowForProvider("testkind", model, 0, 100_000); got != wantPin {
		t.Errorf("pinned window = %d, want %d", got, wantPin)
	}
	if !strings.Contains(mm.transcript.String(), "[window]") {
		t.Errorf("expected drift notice; transcript: %q", mm.transcript.String())
	}
	// Recovery: estimate (~15K) is over 85% of the corrected ~13.5K
	// window, so the same tick must start auto-summarize.
	if cmd == nil {
		t.Fatal("expected auto-summarize Cmd in the same tick as the shrink")
	}
	if !mm.summarizing {
		t.Error("recovery should flip summarizing in the same tick")
	}
	if !strings.Contains(mm.transcript.String(), "auto-summarizing") {
		t.Errorf("expected auto-summarize banner after shrink; transcript: %q", mm.transcript.String())
	}
}

// TestWindowDrift_NonOverflowErrorIsIgnored: only classified overflow
// rejections are drift evidence — a 5xx or rate-limit turn failure
// must not move the window.
func TestWindowDrift_NonOverflowErrorIsIgnored(t *testing.T) {
	const model = "drift-ignore-x"
	m := driftTestModel(t, model, 100_000)
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: strings.Repeat("x", 60_000)}}

	m.noteWindowOverflow(errors.New("openai-auth: HTTP 503 after 3 attempts: upstream connect error"))

	if got := catalog.ResolveWindowForProvider("testkind", model, 0, 100_000); got != 100_000 {
		t.Errorf("window moved to %d on a non-overflow error", got)
	}
}

// TestWindowDrift_ForeseenOverflowIsNotDrift: when the estimate was
// already over the resolved window, the rejection proves nothing about
// the window (the session is just too big — watermark territory), so
// no pin is written.
func TestWindowDrift_ForeseenOverflowIsNotDrift(t *testing.T) {
	const model = "drift-foreseen-x"
	m := driftTestModel(t, model, 5_000) // estimate ~15K > window 5K
	m.sess.Messages = []adapter.Message{{Role: adapter.RoleUser, Content: strings.Repeat("x", 60_000)}}

	m.noteWindowOverflow(errors.New("context_length_exceeded: too big"))

	if got := catalog.ResolveWindowForProvider("testkind", model, 0, 5_000); got != 5_000 {
		t.Errorf("window moved to %d on a foreseen overflow", got)
	}
}

// TestWindowDrift_RaiseOnProvenUsage: a completed turn whose exact
// provider-reported input exceeds the resolved window raises the pin
// to the proven value — no margin, the provider's count is exact.
func TestWindowDrift_RaiseOnProvenUsage(t *testing.T) {
	const model = "drift-raise-x"
	m := driftTestModel(t, model, 100_000)

	m.noteWindowUsage(&adapter.Usage{InputTokens: 120_000, CacheReadTokens: 30_000})

	if got := catalog.ResolveWindowForProvider("testkind", model, 0, 100_000); got != 150_000 {
		t.Errorf("raised window = %d, want proven 150000", got)
	}
	if !strings.Contains(m.transcript.String(), "raising") {
		t.Errorf("expected raise notice; transcript: %q", m.transcript.String())
	}

	// Below-window usage is a no-op: the pin stays at the proven value.
	m.noteWindowUsage(&adapter.Usage{InputTokens: 50_000})
	if got := catalog.ResolveWindowForProvider("testkind", model, 0, 100_000); got != 150_000 {
		t.Errorf("pin moved to %d on below-window usage", got)
	}
}
