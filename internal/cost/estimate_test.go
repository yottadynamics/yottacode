package cost

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestForUsageUnavailableCacheDimensions(t *testing.T) {
	got := ForUsage("https://api.anthropic.com/v1", "anthropic", "claude-sonnet-4-5", adapter.Usage{InputTokens: 1, CacheReadTokens: 1})
	if got.Available {
		t.Fatalf("missing cache price should remain unavailable: %+v", got)
	}
}

func TestForUsageUnavailable(t *testing.T) {
	got := ForUsage("https://example.invalid/v1", "missing", "missing", adapter.Usage{InputTokens: 1})
	if got.Available {
		t.Fatalf("unknown model should not be priced: %+v", got)
	}
}
