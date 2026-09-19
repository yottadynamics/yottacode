package agentruntime

import (
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/browser"
	"github.com/yottadynamics/yottacode/internal/config"
)

// env is a fake environment lookup that records which variables were read.
type env struct {
	vals map[string]string
	read []string
}

func (e *env) get(k string) string {
	e.read = append(e.read, k)
	return e.vals[k]
}

func (e *env) reads(k string) bool {
	for _, r := range e.read {
		if r == k {
			return true
		}
	}
	return false
}

func TestBrowserOptions_DefaultsToTheLocalBrowserAndReadsNoSecrets(t *testing.T) {
	e := &env{vals: map[string]string{"BROWSERBASE_API_KEY": "leak", "CAMOFOX_API_KEY": "leak"}}
	got := browserOptions(config.BrowserConfig{}, e.get)
	if got.Provider != "" || got.Stealth || got.ProxyURL != "" || got.IdleTimeout != 0 {
		t.Errorf("zero config produced %+v", got)
	}
	if got.Browserbase.APIKey != "" || got.Camofox.APIKey != "" {
		t.Errorf("an unselected provider's secret was picked up: %+v", got)
	}
	if len(e.read) != 0 {
		t.Errorf("the environment was consulted for a local browser: %v", e.read)
	}
}

func TestBrowserOptions_LocalCarriesStealthProxyAndIdle(t *testing.T) {
	got := browserOptions(config.BrowserConfig{
		Provider: "local", Stealth: true, ProxyURL: "http://u:p@proxy:8080", IdleTimeoutMinutes: 5,
	}, (&env{}).get)
	if got.Provider != "local" || !got.Stealth || got.ProxyURL != "http://u:p@proxy:8080" || got.IdleTimeout != 5*time.Minute {
		t.Errorf("got %+v", got)
	}
}

func TestBrowserIdleTimeout(t *testing.T) {
	for in, want := range map[int]time.Duration{0: 0, -1: -1, -30: -1, 1: time.Minute, 90: 90 * time.Minute} {
		if got := browserIdleTimeout(in); got != want {
			t.Errorf("browserIdleTimeout(%d) = %s, want %s", in, got, want)
		}
	}
	// The mapping composes with the browser package's own zero/negative rules.
	if _, err := browser.NewManagerWithOptions(browser.Options{IdleTimeout: browserIdleTimeout(-1)}); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserOptions_BrowserbaseResolvesSecretsFromTheEnvironment(t *testing.T) {
	e := &env{vals: map[string]string{
		"BROWSERBASE_API_KEY":    "  key-from-default-env \n",
		"BROWSERBASE_PROJECT_ID": "proj-from-env",
		"CAMOFOX_API_KEY":        "must-not-be-read",
	}}
	got := browserOptions(config.BrowserConfig{
		Provider: "browserbase",
		Browserbase: config.BrowserbaseConfig{
			NoProxies: true, AdvancedStealth: true, SessionTimeoutSeconds: 900, BaseURL: "https://bb.example",
		},
	}, e.get)
	bb := got.Browserbase
	if bb.APIKey != "key-from-default-env" || bb.APIKeyEnv != "BROWSERBASE_API_KEY" || bb.ProjectID != "proj-from-env" {
		t.Errorf("credentials = %+v", bb)
	}
	if !bb.NoProxies || bb.NoKeepAlive || !bb.AdvancedStealth || bb.SessionTimeoutSeconds != 900 || bb.BaseURL != "https://bb.example" {
		t.Errorf("features = %+v", bb)
	}
	if e.reads("CAMOFOX_API_KEY") || got.Camofox.APIKey != "" {
		t.Error("the other provider's secret must not be read")
	}
}

func TestBrowserOptions_BrowserbaseConfigOverridesEnvDefaults(t *testing.T) {
	e := &env{vals: map[string]string{
		"MY_BB_KEY":              "custom-key",
		"BROWSERBASE_PROJECT_ID": "env-project",
	}}
	got := browserOptions(config.BrowserConfig{
		Provider:    "browserbase",
		Browserbase: config.BrowserbaseConfig{APIKeyEnv: "MY_BB_KEY", ProjectID: "file-project"},
	}, e.get)
	if got.Browserbase.APIKey != "custom-key" || got.Browserbase.APIKeyEnv != "MY_BB_KEY" {
		t.Errorf("api_key_env not honored: %+v", got.Browserbase)
	}
	if got.Browserbase.ProjectID != "file-project" {
		t.Errorf("an explicit project_id should beat the environment: %q", got.Browserbase.ProjectID)
	}
}

func TestBrowserOptions_CamofoxURLAndKey(t *testing.T) {
	e := &env{vals: map[string]string{"CAMOFOX_URL": " http://cam.example:9377 ", "CAMOFOX_API_KEY": "tok", "BROWSERBASE_API_KEY": "leak"}}
	got := browserOptions(config.BrowserConfig{Provider: "camofox"}, e.get)
	if got.Camofox.URL != "http://cam.example:9377" || got.Camofox.APIKey != "tok" {
		t.Errorf("camofox = %+v", got.Camofox)
	}
	if e.reads("BROWSERBASE_API_KEY") {
		t.Error("the other provider's secret must not be read")
	}

	got = browserOptions(config.BrowserConfig{
		Provider: "camofox",
		Camofox:  config.CamofoxConfig{URL: "http://file.example", APIKeyEnv: "CF_TOKEN"},
	}, (&env{vals: map[string]string{"CF_TOKEN": "t2", "CAMOFOX_URL": "http://env.example"}}).get)
	if got.Camofox.URL != "http://file.example" || got.Camofox.APIKey != "t2" {
		t.Errorf("file config should beat env defaults: %+v", got.Camofox)
	}
}

func TestBrowserOptions_ProducesAManagerForEveryProvider(t *testing.T) {
	for _, cfg := range []config.BrowserConfig{
		{},
		{Provider: "local", Stealth: true, ProxyURL: "socks5://127.0.0.1:1080"},
		{Provider: "browserbase"}, // missing credentials surface at launch, not here
		{Provider: "camofox"},
	} {
		if _, err := browser.NewManagerWithOptions(browserOptions(cfg, (&env{}).get)); err != nil {
			t.Errorf("%+v: %v", cfg, err)
		}
	}
}
