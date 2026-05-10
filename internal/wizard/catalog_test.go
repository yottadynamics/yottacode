package wizard

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// TestCatalogConsistency enforces that every catalog entry's kind is
// in config.ValidKinds. A regression here would make the wizard
// write configs that config.Validate immediately rejects.
func TestCatalogConsistency(t *testing.T) {
	for _, e := range Catalog {
		if !contains(config.ValidKinds, e.Kind) {
			t.Errorf("catalog %q: kind %q not in config.ValidKinds %v",
				e.Name, e.Kind, config.ValidKinds)
		}
		if e.APIKeyEnv != "" {
			// Env var names should be POSIX-shaped.
			for _, r := range e.APIKeyEnv {
				if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
					t.Errorf("catalog %q: APIKeyEnv %q has non-POSIX char %q",
						e.Name, e.APIKeyEnv, r)
					break
				}
			}
		}
	}
}

// TestFindCatalogEntry checks that named lookup returns the right
// entry and a nil for unknowns.
func TestFindCatalogEntry(t *testing.T) {
	for _, name := range []string{"anthropic", "openai", "gemini", "ollama", "xai", "nvidia-nim", "custom"} {
		if e := FindCatalogEntry(name); e == nil {
			t.Errorf("FindCatalogEntry(%q) returned nil", name)
		}
	}
	for _, name := range []string{"openrouter", "together", "nope"} {
		if FindCatalogEntry(name) != nil {
			t.Errorf("FindCatalogEntry(%q) should be nil — it's not in the catalog", name)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// CatalogIdentity returns the catalog name for the exact-match case
// (first profile, name == catalog entry name).
func TestCatalogIdentity_ExactMatch(t *testing.T) {
	for _, name := range []string{"nvidia-nim", "xai", "anthropic", "ollama"} {
		if got := CatalogIdentity(name); got != name {
			t.Errorf("CatalogIdentity(%q) = %q, want %q", name, got, name)
		}
	}
}

// CatalogIdentity strips a trailing -<digits> suffix to recover the
// catalog identity for nth-of-kind profiles like "nvidia-nim-2",
// "nvidia-nim-12", or "xai-3". The status bar / picker should show
// "nvidia-nim" for both "nvidia-nim" and "nvidia-nim-2".
func TestCatalogIdentity_StripsNumericSuffix(t *testing.T) {
	cases := map[string]string{
		"nvidia-nim-2":  "nvidia-nim",
		"nvidia-nim-12": "nvidia-nim",
		"xai-3":         "xai",
		"anthropic-99":  "anthropic",
	}
	for in, want := range cases {
		if got := CatalogIdentity(in); got != want {
			t.Errorf("CatalogIdentity(%q) = %q, want %q", in, got, want)
		}
	}
}

// Profile names that don't trace back to any catalog entry return
// "" — renderers fall back to Kind in that case.
func TestCatalogIdentity_NoMatch(t *testing.T) {
	for _, name := range []string{"", "my-bedrock", "openrouter-pinned", "something-2"} {
		if got := CatalogIdentity(name); got != "" {
			t.Errorf("CatalogIdentity(%q) = %q, want empty", name, got)
		}
	}
}

// "foo-bar" is not "foo" with a numeric suffix — the suffix stripper
// must only fire for trailing -<digits> runs. Without this guard,
// "openrouter-pinned" might wrongly resolve to "openrouter".
func TestCatalogIdentity_NonNumericSuffixNotStripped(t *testing.T) {
	if got := CatalogIdentity("nvidia-nim-pinned"); got != "" {
		t.Errorf("CatalogIdentity(nvidia-nim-pinned) = %q, want empty (only -<digits> strips)", got)
	}
}
