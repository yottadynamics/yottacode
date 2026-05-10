package wizard

import (
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/config"
)

// mergeConfig combines the Plan's content with the user's existing
// config.toml. The merge rules are:
//
//   - [context], [retrieval] tunables are preserved from disk if
//     they're present (and non-zero), otherwise the wizard's
//     defaults apply.
//   - [[providers]] entries are union'd by name. Plan entries
//     overwrite same-named existing ones (the wizard just collected
//     fresh config for this provider). Existing entries the wizard
//     didn't touch are preserved verbatim.
//   - [active] is overwritten from the Plan when ActiveProvider is
//     non-empty; otherwise existing value is kept.
//   - [router] is overwritten from the Plan when EnableRouter is
//     true; otherwise the existing block is kept (so a user who
//     said no to router in this run still keeps the one they had).
//
// The output is a full TOML file body, ready to write.
func mergeConfig(plan Plan, existingPath string) (string, error) {
	existing, err := config.Load(existingPath)
	if err != nil {
		// Don't fall back to a fresh write — corrupt config means we
		// should refuse before destroying the user's work.
		return "", fmt.Errorf("merge: existing config invalid: %w (rerun with --force to overwrite)", err)
	}

	// Start from the Plan's view, then graft preserved tunables.
	merged := plan.ToConfig()

	// Tunables: preserve the on-disk numeric tunables — the user may
	// have hand-tuned context-window watermarks or retrieval top_k.
	merged.Context = existing.Context
	merged.Retrieval = existing.Retrieval

	// Providers: union by name with Plan winning on collision.
	merged.Providers = mergeProviders(existing.Providers, plan.Providers)

	// Active: Plan wins if set, otherwise keep what was on disk.
	if plan.ActiveProvider == "" {
		merged.Active = existing.Active
	}

	// Router: keep existing block if Plan didn't enable router.
	if !plan.EnableRouter {
		merged.Router = existing.Router
	}

	// Validate the merged config end-to-end before rendering — this
	// catches things like "active.provider names a provider we just
	// deleted via merge" early.
	if err := config.Validate(merged); err != nil {
		return "", fmt.Errorf("merge: result invalid: %w", err)
	}

	return renderMerged(merged, plan), nil
}

// mergeProviders unions existing and planned provider lists by name.
// Plan entries replace same-named existing ones. Order: existing
// providers first (preserved), then any new ones from the Plan
// appended. This stops a rerun from silently reordering the user's
// list.
func mergeProviders(existing []config.Provider, plan []PlanProvider) []config.Provider {
	planByName := make(map[string]PlanProvider, len(plan))
	for _, pp := range plan {
		planByName[pp.Name] = pp
	}
	planSeen := make(map[string]bool, len(plan))

	out := make([]config.Provider, 0, len(existing)+len(plan))
	for _, e := range existing {
		if pp, hit := planByName[e.Name]; hit {
			out = append(out, planProviderToConfig(pp))
			planSeen[e.Name] = true
		} else {
			out = append(out, e)
		}
	}
	for _, pp := range plan {
		if planSeen[pp.Name] {
			continue
		}
		out = append(out, planProviderToConfig(pp))
	}
	return out
}

func planProviderToConfig(pp PlanProvider) config.Provider {
	// pp.Models intentionally dropped; see plan.go::Plan.ToConfig
	// for why (catalog.gen.json owns the curated list now).
	return config.Provider{
		Name:         pp.Name,
		Kind:         pp.Kind,
		BaseURL:      pp.BaseURL,
		APIKeyEnv:    pp.APIKeyEnv,
		DefaultModel: pp.DefaultModel,
	}
}

// renderMerged serializes the merged config into TOML, calling
// config.Render for the bulk of the work and tweaking only the
// header so the file is clear it came from `yottacode setup`.
// Future per-plan footnotes (e.g. "look in <env-path> for keys")
// could be appended here.
func renderMerged(cfg config.Config, plan Plan) string {
	body := config.Render(cfg)
	// Replace the standard header with the wizard-flavored one.
	standard := "# yottacode configuration\n# API keys live in ~/.yottacode/.env, not here.\n\n"
	wizardHdr := "# yottacode configuration — last updated by `yottacode setup`\n# API keys live in ~/.yottacode/.env, not here.\n\n"
	body = strings.Replace(body, standard, wizardHdr, 1)
	_ = plan // reserved for future per-plan footnotes
	return body
}

// encodeTunables is gone: rendering of the [context] / [retrieval]
// sections lives in config.Render now so wizard and TUI use the same
// code path.
