package tui

import (
	"fmt"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
)

// Passive context-window drift correction. Providers advertise one
// limit and sometimes enforce another — the ChatGPT Codex backend
// enforces ~272K input on gpt-5.5 while the API metadata says 1.05M.
// Live traffic carries the evidence for free, in both directions:
//
//   - a turn REJECTED for length while the estimator believed the
//     request fit the resolved window → the window is too big. Shrink.
//   - a turn ACCEPTED whose exact provider-reported input exceeds the
//     resolved window → the window is too small. Raise.
//
// Corrections persist as provider-qualified pins ("<kind>/<model id>")
// in the runtime overlay (~/.yottacode/context-windows.json), which
// ResolveWindowForProvider consults ahead of every source except the
// user's explicit config override. config.toml is never touched.
//
// Recovery after a shrink is the caller's job and is wired into the
// turn-end path: noteWindowOverflow runs immediately BEFORE the
// watermark check, so the check re-resolves the corrected window and
// fires auto-summarize in the same tick — the session heals before the
// user retries, instead of failing again.

// driftShrinkFactor is the margin applied to the ESTIMATED size when
// shrinking: the local estimator runs ~10-15% under some providers'
// real tokenizers, so pinning straight to the estimate could still
// exceed the enforced limit. Repeated rejections shrink geometrically
// until requests fit; successful turns re-prove the floor upward.
const driftShrinkFactor = 0.9

// driftWindowFloor is the lowest a shrink may pin. Below this the
// signal is almost certainly a mis-classified error rather than a real
// window; correction stops instead of wedging the session shut.
const driftWindowFloor = 8192

// driftKey returns the provider-qualified window-store key for the
// active model, or "" when the provider kind can't be determined —
// drift facts are per-backend, so an unqualified pin (which would leak
// into every provider serving a namesake model) is never written.
func (m *Model) driftKey() string {
	if m.modelName == "" {
		return ""
	}
	kind := m.fileCfg.ProviderKindForModel(m.modelName)
	if kind == "" {
		return ""
	}
	return kind + "/" + m.modelName
}

// noteWindowOverflow is the shrink direction. Returns true when a pin
// was written — the caller's subsequent watermark check then resolves
// the corrected window and triggers compression (the recovery).
//
// It only treats a rejection as drift evidence when the estimator
// believed the request fit (estimate <= resolved window): an overflow
// the system already saw coming (estimate over the window with
// auto-summarize disabled, or a single turn ballooning past the limit)
// proves nothing about the window itself and is left to the watermark
// machinery.
func (m *Model) noteWindowOverflow(err error) bool {
	if !adapter.IsContextOverflow(err) {
		return false
	}
	key := m.driftKey()
	if key == "" {
		return false
	}
	window := m.contextWindow()
	m.refreshContextTokens()
	estimate := m.contextTokens
	if window <= 0 || estimate <= 0 || estimate > window {
		return false
	}
	pinned := max(int(float64(estimate)*driftShrinkFactor), driftWindowFloor)
	if pinned >= window {
		return false
	}
	changed, uerr := catalog.UpsertWindow(key, pinned)
	if uerr != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("⚠ window drift: pin %s: %v", key, uerr)))
		return false
	}
	if !changed {
		return false
	}
	m.appendLine(renderWindowNoticeCard([]string{
		fmt.Sprintf("⚠ provider rejected ~%s tokens but the resolved window was %s", formatTokens(estimate), formatTokens(window)),
		fmt.Sprintf("  pinning %s to %s in the runtime overlay and re-checking context", key, formatTokens(pinned)),
	}, "auto calibration", m.width))
	return true
}

// noteWindowUsage is the raise direction: a completed turn whose exact
// provider-reported input exceeds the resolved window proves the real
// window is at least that large. Raising the pin stops premature
// compaction (and walks back any over-shrunk pin from the rejection
// path). Usage is the provider's own count, so no margin is applied.
func (m *Model) noteWindowUsage(u *adapter.Usage) {
	if u == nil {
		return
	}
	key := m.driftKey()
	if key == "" {
		return
	}
	window := m.contextWindow()
	if window <= 0 {
		return
	}
	totalIn := int(u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens)
	if totalIn <= window {
		return
	}
	changed, uerr := catalog.UpsertWindow(key, totalIn)
	if uerr != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("⚠ window drift: pin %s: %v", key, uerr)))
		return
	}
	if !changed {
		return
	}
	m.appendLine(renderWindowNoticeCard([]string{
		fmt.Sprintf("✓ provider accepted %s input tokens against a resolved window of %s", formatTokens(totalIn), formatTokens(window)),
		fmt.Sprintf("  raised %s to the proven %s", key, formatTokens(totalIn)),
	}, "auto calibration", m.width))
}
