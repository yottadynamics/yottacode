package agentruntime

import (
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/browser"
	"github.com/yottadynamics/yottacode/internal/config"
)

// browserOptions turns the `[browser]` config block into browser.Options.
// Secrets are resolved here, from the environment variable each provider's
// block names — the config file never holds one — and only for the provider
// actually selected, so an unrelated provider's key is never handed around.
func browserOptions(cfg config.BrowserConfig, getenv func(string) string) browser.Options {
	o := browser.Options{
		Provider:    cfg.Provider,
		Stealth:     cfg.Stealth,
		ProxyURL:    cfg.ProxyURL,
		IdleTimeout: browserIdleTimeout(cfg.IdleTimeoutMinutes),
	}
	switch cfg.Provider {
	case config.BrowserProviderBrowserbase:
		bb := cfg.Browserbase
		keyEnv := firstNonEmpty(bb.APIKeyEnv, config.DefaultBrowserbaseAPIKeyEnv)
		o.Browserbase = browser.BrowserbaseOptions{
			APIKey:                strings.TrimSpace(getenv(keyEnv)),
			APIKeyEnv:             keyEnv,
			ProjectID:             firstNonEmpty(bb.ProjectID, strings.TrimSpace(getenv("BROWSERBASE_PROJECT_ID"))),
			BaseURL:               bb.BaseURL,
			NoProxies:             bb.NoProxies,
			NoKeepAlive:           bb.NoKeepAlive,
			AdvancedStealth:       bb.AdvancedStealth,
			SessionTimeoutSeconds: bb.SessionTimeoutSeconds,
		}
	case config.BrowserProviderCamofox:
		cf := cfg.Camofox
		o.Camofox = browser.CamofoxOptions{
			URL:    firstNonEmpty(cf.URL, strings.TrimSpace(getenv("CAMOFOX_URL"))),
			APIKey: strings.TrimSpace(getenv(firstNonEmpty(cf.APIKeyEnv, config.DefaultCamofoxAPIKeyEnv))),
		}
	}
	return o
}

// browserIdleTimeout maps the config's minutes onto browser.Options'
// duration: 0 keeps the default, negative disables reaping.
func browserIdleTimeout(minutes int) time.Duration {
	switch {
	case minutes == 0:
		return 0
	case minutes < 0:
		return -1
	}
	return time.Duration(minutes) * time.Minute
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
