package acp

import (
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/agentruntime"
	"github.com/yottadynamics/yottacode/internal/catalog"
)

// correctWindow records provider-specific evidence without changing user
// configuration. Empty provider kinds are deliberately ignored.
func correctWindow(rt *agentruntime.Runtime, usage *adapter.Usage, overflow bool) string {
	if rt == nil || rt.Model == "" {
		return ""
	}
	kind := strings.TrimSpace(rt.FileCfg.ProviderKindForModel(rt.Model))
	if kind == "" {
		return ""
	}
	key := kind + "/" + rt.Model
	window := catalog.ResolveWindowForProvider(kind, rt.Model, rt.FileCfg.ContextWindowOverride(rt.Model), rt.FileCfg.Context.DefaultWindow)
	pin := 0
	if usage != nil {
		total := usage.InputTokens + usage.CacheReadTokens + usage.CacheCreationTokens
		if total > int64(window) {
			pin = int(total)
		}
	}
	if overflow && window > 8192 {
		pin = int(float64(window) * .9)
		if pin < 8192 {
			pin = 8192
		}
	}
	if pin <= 0 || pin == window {
		return ""
	}
	changed, err := catalog.UpsertWindow(key, pin)
	if err != nil {
		return fmt.Sprintf("[context] window pin failed for %s: %v\n", key, err)
	}
	if !changed {
		return ""
	}
	if overflow {
		return fmt.Sprintf("[context] provider window adjusted: %s → %d tokens\n", key, pin)
	}
	return fmt.Sprintf("[context] provider usage raised window: %s → %d tokens\n", key, pin)
}
