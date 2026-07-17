// Package wizard implements `yottacode setup` — the first-run setup
// flow. It collects provider profiles, default models, API keys, and a
// few session-stable knobs (router), then writes
// ~/.yottacode/config.toml plus ~/.yottacode/.env (mode 0600).
//
// The wizard is a standalone Bubbletea program. It is independent of
// the chat TUI and can be invoked from a fresh terminal (`yottacode
// setup`), from inside the chat TUI (`/setup`, suspends), or
// auto-triggered when no config.toml exists at startup.
//
// API keys never live in config.toml. Every Provider entry names an
// env-var via api_key_env; values land in ~/.yottacode/.env (chmod
// 0600) when the user enters them interactively. Already-set
// environment values are detected and not duplicated to disk.
package wizard

// CatalogEntry describes one provider yottacode knows about
// out-of-the-box. It maps 1:1 onto a future config.Provider — name +
// kind + base URL + the env var to read the API key from. The
// curated *model* list (which models exist for this provider) used
// to live here too; it now lives in internal/catalog/catalog.gen.json
// and is consumed lazily by the wizard's stepConfigure picker and the
// /model TUI overlay. CatalogEntry is just the provider directory.
//
// The catalog is a static Go map: changes ship with the binary.
// Network-fetched catalogs are tempting (always up-to-date!) but bring
// "first-run requires internet" failure modes, license/ToS questions
// per provider, and a freshness cliff when our own URL goes down. A
// few lines of Go is the right answer.
type CatalogEntry struct {
	// Name is the user-facing label that appears in the wizard and
	// becomes [[providers]].name in config.toml. Used by /provider use.
	Name string

	// Kind is the adapter family. Must be one of
	// internal/config.ValidKinds (anthropic | openai | gemini | xai | ollama
	// | openai-compatible). Drives both adapter dispatch and whether
	// the wizard's stepConfigure pulls a curated model list from
	// internal/catalog (anthropic/openai/gemini/xai) or falls back to
	// free-form text input (ollama, openai-compatible).
	Kind string

	// BaseURL is the API endpoint. For Anthropic / Gemini we route by
	// model prefix as well, so the base URL is informational; for
	// OpenAI-compatible endpoints the URL is the routing decision.
	BaseURL string

	// APIKeyEnv is the env-var name that holds the bearer token.
	// Empty for Ollama (local, no key). The wizard prompts for the
	// value when the variable is not already set in the environment.
	APIKeyEnv string

	// Note is a one-line hint shown next to the provider in the
	// multi-select. Keep short — anything longer than 50 chars wraps
	// awkwardly on narrow terminals.
	Note string
}

// Catalog is the ordered list of providers offered in the wizard.
// Order matters: it's the screen order in the multi-select. Most-loved
// providers go first; "custom" lands at the bottom for power users.
var Catalog = []CatalogEntry{
	{Name: "openai-auth", Kind: "openai-auth", BaseURL: "https://chatgpt.com/backend-api/codex", APIKeyEnv: "", Note: "OpenAI via ChatGPT login (browser OAuth)"},
	{Name: "copilot-auth", Kind: "copilot", BaseURL: "https://api.githubcopilot.com", APIKeyEnv: "", Note: "GitHub Copilot (device code OAuth)"},
	{Name: "anthropic", Kind: "anthropic", BaseURL: "https://api.anthropic.com", APIKeyEnv: "ANTHROPIC_API_KEY", Note: "Claude (Anthropic) — model list from internal/catalog (refresh via cmd/yotta-models)"},
	{Name: "openai", Kind: "openai", BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", Note: "GPT + o-series (OpenAI) — model list from internal/catalog"},
	{Name: "gemini", Kind: "gemini", BaseURL: "https://generativelanguage.googleapis.com", APIKeyEnv: "GEMINI_API_KEY", Note: "Gemini (Google) — model list from internal/catalog"},
	{Name: "xai", Kind: "xai", BaseURL: "https://api.x.ai/v1", APIKeyEnv: "XAI_API_KEY", Note: "Grok (xAI) — model list from internal/catalog"},
	// Google Vertex AI is one UX row. The configure screen asks for the
	// family and then writes one of the protocol-specific provider kinds.
	{Name: "google-vertex", Kind: "vertex", BaseURL: "https://us-central1-aiplatform.googleapis.com/v1/projects/PROJECT/locations/us-central1/endpoints/openapi", APIKeyEnv: "", Note: "Google Vertex AI (choose Gemini or Claude; gcloud ADC)"},
	{Name: "nvidia-nim", Kind: "openai-compatible", BaseURL: "https://integrate.api.nvidia.com/v1", APIKeyEnv: "NVIDIA_API_KEY", Note: "NVIDIA NIM — OpenAI-compatible endpoint at build.nvidia.com"},
	{Name: "ollama", Kind: "ollama", BaseURL: "http://localhost:11434/v1", APIKeyEnv: "", Note: "local models via Ollama (auto-probed on localhost:11434)"},
	{Name: "custom", Kind: "openai-compatible", BaseURL: "", APIKeyEnv: "", Note: "Custom OpenAI-compatible endpoint (vLLM, Llama Stack, Groq, Fireworks, OpenRouter, Together, ...)"},
}

// VertexFamily identifies which concrete Vertex API surface the combined
// Google Vertex AI picker row should configure. The public UX is one
// provider choice; the config still writes a protocol-specific kind.
type VertexFamily int

const (
	VertexFamilyGemini VertexFamily = iota
	VertexFamilyClaude
)

func (f VertexFamily) Label() string {
	if f == VertexFamilyClaude {
		return "Claude"
	}
	return "Gemini"
}

func (f VertexFamily) CatalogEntry() CatalogEntry {
	if f == VertexFamilyClaude {
		return CatalogEntry{Name: "vertex-claude", Kind: "vertex-anthropic", BaseURL: VertexBaseURL(f, "PROJECT"), APIKeyEnv: "", Note: "Claude on Google Vertex AI (gcloud ADC — set your GCP project in the URL)"}
	}
	return CatalogEntry{Name: "vertex-gemini", Kind: "vertex", BaseURL: VertexBaseURL(f, "PROJECT"), APIKeyEnv: "", Note: "Gemini on Google Vertex AI (gcloud ADC — set your GCP project in the URL)"}
}

func VertexBaseURL(f VertexFamily, project string) string {
	if project == "" {
		project = "PROJECT"
	}
	if f == VertexFamilyClaude {
		return "https://aiplatform.googleapis.com/v1/projects/" + project + "/locations/global"
	}
	return "https://us-central1-aiplatform.googleapis.com/v1/projects/" + project + "/locations/us-central1/endpoints/openapi"
}

// FindCatalogEntry returns the catalog entry with the given name, or
// nil. Used by flag-driven non-interactive paths to translate
// `--provider anthropic` into a CatalogEntry.
func FindCatalogEntry(name string) *CatalogEntry {
	for i := range Catalog {
		if Catalog[i].Name == name {
			return &Catalog[i]
		}
	}
	for _, f := range []VertexFamily{VertexFamilyGemini, VertexFamilyClaude} {
		e := f.CatalogEntry()
		if e.Name == name {
			return &e
		}
	}
	return nil
}

// CatalogIdentity returns the catalog entry name a profile was added
// from, or "" if no match. Renderers use this to display "nvidia-nim"
// for both "nvidia-nim" and "nvidia-nim-2" profiles instead of the
// generic "openai-compatible" kind. The lookup is:
//
//  1. Exact match on the profile name (covers the first-add case
//     where Name == catalog entry name verbatim).
//  2. Strip a trailing "-<digits>" suffix (the uniqueProviderName
//     output, e.g. "nvidia-nim-2" → "nvidia-nim") and re-look-up.
//
// Returns "" when the profile name doesn't trace back to any catalog
// entry — typically a user-renamed profile or a free-form name that
// never came through the picker. Callers fall back to the kind in
// that case.
func CatalogIdentity(profileName string) string {
	if profileName == "" {
		return ""
	}
	if profileName == "vertex-gemini" || profileName == "vertex-claude" {
		return "google-vertex"
	}
	if FindCatalogEntry(profileName) != nil {
		return profileName
	}
	// Strip a trailing -<digits> suffix and retry. Bounded: only
	// strip the last numeric block, not arbitrary nested suffixes,
	// so "nvidia-nim-12-3" still walks back to "nvidia-nim-12" if
	// that's what the catalog had — closest-match wins.
	if i := lastDashDigits(profileName); i > 0 {
		base := profileName[:i]
		if FindCatalogEntry(base) != nil {
			return base
		}
	}
	return ""
}

// lastDashDigits returns the index of the '-' that begins a trailing
// "-<digits>" run in s, or -1 if no such run exists. Examples:
// "foo-2" → 3, "foo-12" → 3, "foo" → -1, "foo-bar" → -1.
func lastDashDigits(s string) int {
	end := len(s)
	i := end - 1
	for i >= 0 && s[i] >= '0' && s[i] <= '9' {
		i--
	}
	if i == end-1 || i < 0 || s[i] != '-' {
		return -1
	}
	return i
}
