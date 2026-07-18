package cost

import "github.com/yottadynamics/yottacode/internal/adapter"

// BillingDashboardURL returns the public URL where the user can
// inspect their billing / quota for the given provider, or empty
// string if no canonical surface exists.
//
// /usage surfaces this as a footer so the dollar estimate has an
// authoritative escape hatch — the catalog drifts, the invoice
// doesn't.
//
// URLs are stable hostnames; we point at the dashboards' index
// rather than deep links because the deep paths churn more often
// (OpenAI's "Usage" tab has been renamed three times in two years,
// for example).
func BillingDashboardURL(p adapter.Provider) string {
	switch p {
	case adapter.ProviderAnthropic:
		return "https://console.anthropic.com/settings/billing"
	case adapter.ProviderOpenAI:
		return "https://platform.openai.com/usage"
	case adapter.ProviderOpenAIAuth:
		return "https://chatgpt.com/account"
	case adapter.ProviderCopilot:
		return "https://github.com/settings/billing/summary"
	case adapter.ProviderGemini:
		return "https://aistudio.google.com/app/billing"
	case adapter.ProviderXAI:
		return "https://console.x.ai/team"
	case adapter.ProviderVertex, adapter.ProviderVertexAnthropic:
		// Vertex bills to the user's own GCP project, so this is the
		// generic console entry point rather than a per-vendor page —
		// we don't know their billing account from here.
		return "https://console.cloud.google.com/billing"
	}
	return ""
}
