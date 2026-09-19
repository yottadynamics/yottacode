package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoad_ParsesBrowserConfigAndSurvivesARender(t *testing.T) {
	src := `[browser]
provider = "browserbase"
idle_timeout_minutes = 30

[browser.browserbase]
api_key_env = "MY_BB_KEY"
project_id = "proj_123"
no_proxies = true
advanced_stealth = true
session_timeout_seconds = 900

[browser.camofox]
url = "http://localhost:9377"
api_key_env = "CF_TOKEN"
`
	cfg, err := Load(writeFile(t, src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := BrowserConfig{
		Provider:           "browserbase",
		IdleTimeoutMinutes: 30,
		Browserbase: BrowserbaseConfig{
			APIKeyEnv: "MY_BB_KEY", ProjectID: "proj_123", NoProxies: true, AdvancedStealth: true, SessionTimeoutSeconds: 900,
		},
		Camofox: CamofoxConfig{URL: "http://localhost:9377", APIKeyEnv: "CF_TOKEN"},
	}
	if !reflect.DeepEqual(cfg.Browser, want) {
		t.Fatalf("Browser = %+v, want %+v", cfg.Browser, want)
	}

	// A wizard save rebuilds the file from the struct: the block must survive.
	body := Render(cfg)
	for _, s := range []string{"[browser]", `provider = "browserbase"`, "[browser.browserbase]", `project_id = "proj_123"`, "no_proxies = true", "[browser.camofox]"} {
		if !strings.Contains(body, s) {
			t.Errorf("Render dropped %q from the [browser] block:\n%s", s, body)
		}
	}
	again, err := Load(writeFile(t, body))
	if err != nil {
		t.Fatalf("re-Load(Render(cfg)): %v", err)
	}
	if !reflect.DeepEqual(again.Browser, want) {
		t.Errorf("Browser after a render round-trip = %+v, want %+v", again.Browser, want)
	}
}

func TestRender_OmitsAnUntouchedBrowserBlock(t *testing.T) {
	if body := Render(Default()); strings.Contains(body, "[browser") {
		t.Errorf("a default config should not grow a [browser] block:\n%s", body)
	}
}

func TestLoad_LocalBrowserOptionsSurviveARender(t *testing.T) {
	cfg, err := Load(writeFile(t, "[browser]\nstealth = true\nproxy_url = \"http://user:pw@proxy.example:3128\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Browser.Stealth || cfg.Browser.ProxyURL != "http://user:pw@proxy.example:3128" {
		t.Fatalf("Browser = %+v", cfg.Browser)
	}
	body := Render(cfg)
	if !strings.Contains(body, "stealth = true") || !strings.Contains(body, "proxy_url =") {
		t.Errorf("Render dropped the local browser options:\n%s", body)
	}
}

func TestValidateBrowser(t *testing.T) {
	ok := []BrowserConfig{
		{},
		{Provider: "local"},
		{Provider: "local", Stealth: true, ProxyURL: "http://proxy.example:8080"},
		{ProxyURL: "https://user:pw@proxy.example"},
		{ProxyURL: "socks5://127.0.0.1:1080"},
		{Provider: "browserbase"},
		{Provider: "browserbase", Browserbase: BrowserbaseConfig{SessionTimeoutSeconds: MaxBrowserbaseSessionTimeoutSeconds, BaseURL: "https://gw.example"}},
		{Provider: "camofox", Camofox: CamofoxConfig{URL: "http://localhost:9377"}},
		{Provider: "camofox"}, // the URL may come from $CAMOFOX_URL
		{IdleTimeoutMinutes: -1},
	}
	for _, b := range ok {
		if err := validateBrowser(b); err != nil {
			t.Errorf("validateBrowser(%+v) = %v, want nil", b, err)
		}
	}

	bad := []struct {
		name string
		cfg  BrowserConfig
		want string
	}{
		{"unknown provider", BrowserConfig{Provider: "selenium"}, "browser.provider"},
		{"stealth on a cloud provider", BrowserConfig{Provider: "browserbase", Stealth: true}, "browser.stealth"},
		{"stealth on camofox", BrowserConfig{Provider: "camofox", Stealth: true}, "browser.stealth"},
		{"proxy on a cloud provider", BrowserConfig{Provider: "browserbase", ProxyURL: "http://p:1"}, "browser.proxy_url"},
		{"proxy on camofox", BrowserConfig{Provider: "camofox", ProxyURL: "http://p:1"}, "browser.proxy_url"},
		{"proxy without host", BrowserConfig{ProxyURL: "http://"}, "browser.proxy_url"},
		{"proxy with a bad scheme", BrowserConfig{ProxyURL: "ftp://proxy.example"}, "scheme"},
		{"socks5 with credentials", BrowserConfig{ProxyURL: "socks5://u:p@proxy.example:1080"}, "SOCKS"},
		{"negative session timeout", BrowserConfig{Browserbase: BrowserbaseConfig{SessionTimeoutSeconds: -1}}, "session_timeout_seconds"},
		{"session timeout too long", BrowserConfig{Browserbase: BrowserbaseConfig{SessionTimeoutSeconds: MaxBrowserbaseSessionTimeoutSeconds + 1}}, "session_timeout_seconds"},
		{"non-http base url", BrowserConfig{Browserbase: BrowserbaseConfig{BaseURL: "ftp://x"}}, "browser.browserbase.base_url"},
		{"camofox url without scheme", BrowserConfig{Camofox: CamofoxConfig{URL: "localhost:9377"}}, "browser.camofox.url"},
	}
	for _, c := range bad {
		err := validateBrowser(c.cfg)
		if err == nil {
			t.Errorf("%s: validateBrowser(%+v) = nil, want an error", c.name, c.cfg)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q should mention %q", c.name, err, c.want)
		}
	}
}

func TestValidateBrowser_ErrorsNeverEchoCredentials(t *testing.T) {
	for _, cfg := range []BrowserConfig{
		{ProxyURL: "socks5://ada:hunter2@proxy.example:1080"},
		{ProxyURL: "ftp://ada:hunter2@proxy.example"},
		{Camofox: CamofoxConfig{URL: "ftp://ada:hunter2@cam.example"}},
		{Browserbase: BrowserbaseConfig{BaseURL: "ftp://ada:hunter2@bb.example"}},
	} {
		err := validateBrowser(cfg)
		if err == nil {
			t.Errorf("%+v should be rejected", cfg)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("error leaks the credentials: %v", err)
		}
	}
}

func TestLoad_RejectsInvalidBrowserConfig(t *testing.T) {
	_, err := Load(writeFile(t, "[browser]\nprovider = \"browserbase\"\nstealth = true\n"))
	if err == nil || !strings.Contains(err.Error(), "browser.stealth") {
		t.Fatalf("Load = %v, want the browser.stealth complaint", err)
	}
}
