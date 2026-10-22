package agent

import (
	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// The compaction trigger compares an estimate (chars/4) against the window.
// That heuristic is routinely off by tens of percent (code and JSON tokenize
// denser than prose). Providers report the real prompt size after every
// response, so the loop learns a correction ratio = reported / estimated and
// scales later estimates by it.
const (
	// calibMinEstimate: ignore tiny prompts, where fixed provider overhead
	// dominates and the ratio says nothing about the history.
	calibMinEstimate = 4000
	calibMinRatio    = 0.5
	calibMaxRatio    = 2.0
	// calibAlpha weights the newest observation in the moving average.
	calibAlpha = 0.5
)

// tokenCalibrator is safe for concurrent use (the config is shared by value
// copies of LoopConfig).
type tokenCalibrator struct {
	mu    syncutil.Mutex
	ratio float64 // 0 = no observation yet
	// key is the provider/model the ratio was learned on; a different model
	// tokenizes differently, so the ratio restarts.
	key string
}

// observe folds one (estimated, reported) pair into the ratio.
func (c *tokenCalibrator) observe(key string, estimated int, reported int64) {
	if c == nil || estimated < calibMinEstimate || reported <= 0 {
		return
	}
	r := float64(reported) / float64(estimated)
	r = min(max(r, calibMinRatio), calibMaxRatio)
	c.mu.Lock()
	defer c.mu.Unlock()
	if key != c.key {
		c.key, c.ratio = key, 0
	}
	if c.ratio == 0 {
		c.ratio = r
		return
	}
	c.ratio = calibAlpha*r + (1-calibAlpha)*c.ratio
}

func (c *tokenCalibrator) get() float64 {
	if c == nil {
		return 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ratio == 0 {
		return 1
	}
	return c.ratio
}

// scale applies the learned ratio to an estimate.
func (c *tokenCalibrator) scale(tokens int) int {
	return int(float64(tokens) * c.get())
}

// reportedPromptTokens is the full prompt size a response reports: fresh
// input plus cache reads/writes. Every adapter normalizes to this split —
// Anthropic reports the three fields natively, and the OpenAI-style
// (chat completions, Responses, Codex, Copilot, Ollama) and Gemini adapters
// subtract the cached subset out of the provider's total before filling
// InputTokens — so the sum is the provider's total prompt size.
func reportedPromptTokens(u *adapter.Usage) int64 {
	if u == nil {
		return 0
	}
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}
