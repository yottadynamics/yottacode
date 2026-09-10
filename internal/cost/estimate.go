package cost

import (
	"math"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/catalog"
)

// Estimate is a public-list-price estimate in USD. It is not an invoice and
// is unavailable when the catalog cannot price the complete usage record.
type Estimate struct {
	USD       float64
	Available bool
}

// ForUsage calculates an estimated USD amount for provider-reported usage.
// Prices are expressed by models.dev as USD per one million tokens. Cache
// creation is included only when models.dev publishes a cache-write rate;
// unknown dimensions remain unavailable rather than being guessed.
func ForUsage(baseURL, provider string, model string, u adapter.Usage) Estimate {
	in, out, read, write, ok := catalog.ModelsDevPricing(baseURL, provider, model)
	if !ok {
		return Estimate{}
	}
	if read == 0 && u.CacheReadTokens > 0 {
		return Estimate{}
	}
	if write == 0 && u.CacheCreationTokens > 0 {
		return Estimate{}
	}
	usd := float64(u.InputTokens)*in/1e6 +
		float64(u.OutputTokens)*out/1e6 +
		float64(u.CacheReadTokens)*read/1e6 +
		float64(u.CacheCreationTokens)*write/1e6
	return Estimate{USD: math.Round(usd*1e8) / 1e8, Available: true}
}
