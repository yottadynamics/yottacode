//go:build integration

// Real-Chrome tests for the backend options: stealth identity, an
// authenticating proxy, and a remote (CDP-URL) session driven through the
// Browserbase client against a fake Browserbase API.
package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

const signalsJS = `(async () => {
  let permQuery = '';
  try { permQuery = (await navigator.permissions.query({name: 'notifications'})).state; } catch (e) { permQuery = 'ERR:' + e.message; }
  let webgl = '';
  try {
    const g = document.createElement('canvas').getContext('webgl');
    const x = g.getExtension('WEBGL_debug_renderer_info');
    webgl = g.getParameter(x.UNMASKED_VENDOR_WEBGL) + ' | ' + g.getParameter(x.UNMASKED_RENDERER_WEBGL);
  } catch (e) { webgl = 'ERR:' + e.message; }
  return JSON.stringify({
    webdriver: navigator.webdriver,
    ua: navigator.userAgent,
    languages: navigator.languages,
    brands: navigator.userAgentData ? navigator.userAgentData.brands.map(b => b.brand) : [],
    uaPlatform: navigator.userAgentData ? navigator.userAgentData.platform : '',
    plugins: navigator.plugins.length,
    chrome: typeof window.chrome,
    notification: Notification.permission,
    permQuery: permQuery,
    webgl: webgl,
  });
})()`

type pageSignals struct {
	Webdriver    bool     `json:"webdriver"`
	UA           string   `json:"ua"`
	Languages    []string `json:"languages"`
	Brands       []string `json:"brands"`
	UAPlatform   string   `json:"uaPlatform"`
	Plugins      int      `json:"plugins"`
	Chrome       string   `json:"chrome"`
	Notification string   `json:"notification"`
	PermQuery    string   `json:"permQuery"`
	WebGL        string   `json:"webgl"`
}

// signals evaluates the detection-relevant navigator properties.
func signals(t *testing.T, m *Manager) pageSignals {
	t.Helper()
	raw, err := m.Eval(context.Background(), signalsJS)
	if err != nil {
		t.Fatalf("Eval signals: %v", err)
	}
	var inner string
	if err := json.Unmarshal([]byte(raw), &inner); err != nil {
		t.Fatalf("signals result %s: %v", raw, err)
	}
	var s pageSignals
	if err := json.Unmarshal([]byte(inner), &s); err != nil {
		t.Fatalf("signals JSON %s: %v", inner, err)
	}
	return s
}

func TestIntegration_StealthPresentsACoherentIdentity(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	ctx := context.Background()

	plain, err := NewManagerWithOptions(Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plain.Close(ctx) })
	if _, err := plain.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	// Baseline: proves the probe can see what stealth is meant to hide.
	if base := signals(t, plain); !base.Webdriver {
		t.Fatalf("baseline browser should report navigator.webdriver = true, got %+v", base)
	}

	m, err := NewManagerWithOptions(Options{Stealth: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	got := signals(t, m)
	if got.Webdriver {
		t.Error("stealth: navigator.webdriver is still true")
	}
	if strings.Contains(got.UA, "HeadlessChrome") {
		t.Errorf("stealth: user agent still says HeadlessChrome: %s", got.UA)
	}
	wantOS, wantHint := "X11; Linux", "Linux"
	if runtime.GOOS == "darwin" {
		wantOS, wantHint = "Macintosh", "macOS"
	}
	if !strings.Contains(got.UA, wantOS) {
		t.Errorf("stealth: user agent %q does not match this platform (%s)", got.UA, wantOS)
	}
	if got.UAPlatform != wantHint {
		t.Errorf("stealth: client-hint platform = %q, want %q", got.UAPlatform, wantHint)
	}
	for _, b := range got.Brands {
		if strings.Contains(b, "Headless") {
			t.Errorf("stealth: client-hint brand %q leaks headless mode", b)
		}
	}
	if !strings.Contains(strings.Join(got.Brands, ","), "Google Chrome") {
		t.Errorf("stealth: client-hint brands = %v, want Google Chrome among them", got.Brands)
	}
	// The list must be exactly what a real Chrome reports. (An Accept-Language
	// of "en-US,en;q=0.9" surfaces here as a bogus "en;q=0.9" entry.)
	if len(got.Languages) != 2 || got.Languages[0] != "en-US" || got.Languages[1] != "en" {
		t.Errorf("stealth: languages = %q, want [en-US en]", got.Languages)
	}
}

// TestIntegration_StealthSignalsContradictNothing pins the reason stealth mode
// injects no JavaScript: hiding a tell is only worth it if the result is still
// self-consistent. A property patched to a value that disagrees with another
// (a Mac GPU under a Linux user agent, a notifications query that contradicts
// Notification.permission) is a louder tell than the one it hid. These are the
// two contradictions the previously used evasion bundle introduced on a
// current Chrome.
func TestIntegration_StealthSignalsContradictNothing(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m, err := NewManagerWithOptions(Options{Stealth: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })
	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	got := signals(t, m)

	// Notification.permission "default" must be reported by the permissions
	// API as "prompt"; the other two states map to themselves.
	wantQuery := map[string]string{"default": "prompt", "granted": "granted", "denied": "denied"}[got.Notification]
	if got.PermQuery != wantQuery {
		t.Errorf("Notification.permission = %q but permissions.query says %q (want %q)", got.Notification, got.PermQuery, wantQuery)
	}

	// The WebGL renderer must not name hardware from another OS than the
	// user agent claims.
	if runtime.GOOS == "linux" {
		for _, mac := range []string{"Apple", "Mac", "OpenGL Engine"} {
			if strings.Contains(got.WebGL, mac) {
				t.Errorf("Linux user agent but the WebGL renderer says %q", got.WebGL)
			}
		}
	}

	// What a real Chrome has, this one still has.
	if got.Plugins == 0 || got.Chrome != "object" || got.Webdriver {
		t.Errorf("baseline signals broken: plugins=%d chrome=%q webdriver=%t", got.Plugins, got.Chrome, got.Webdriver)
	}
}

func TestIntegration_StealthAppliesToPopups(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newFixtureServer(t)
	m, err := NewManagerWithOptions(Options{Stealth: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/popup", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.Click(ctx, "#popup-link"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if err := m.Wait(ctx, "", "popup landed", false, 5*time.Second); err != nil {
		t.Fatalf("popup did not become the active page: %v", err)
	}
	got := signals(t, m)
	if got.Webdriver || strings.Contains(got.UA, "HeadlessChrome") {
		t.Errorf("stealth identity missing on the popup: %+v", got)
	}
}

// newProxyServer is a minimal HTTP forward proxy that demands Basic
// credentials and answers every proxied request with a fixed page. It counts
// authenticated and unauthenticated attempts.
func newProxyServer(t *testing.T, user, pass string) (srv *httptest.Server, authed, challenged func() int, hosts func() []string) {
	t.Helper()
	var mu syncutil.Mutex
	var nAuthed, nChallenged int
	var seen []string
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Host)
		if r.Header.Get("Proxy-Authorization") != want {
			nChallenged++
			w.Header().Set("Proxy-Authenticate", `Basic realm="yottacode-test"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		nAuthed++
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>via proxy</title></head><body>host=` + r.Host + `</body></html>`))
	}))
	t.Cleanup(srv.Close)
	return srv,
		func() int { mu.Lock(); defer mu.Unlock(); return nAuthed },
		func() int { mu.Lock(); defer mu.Unlock(); return nChallenged },
		func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), seen...) }
}

func TestIntegration_AuthenticatingProxy(t *testing.T) {
	skipIfNoBrowser(t)
	proxy, authed, challenged, hosts := newProxyServer(t, "ada", "s3cret")
	proxyURL := strings.Replace(proxy.URL, "http://", "http://ada:s3cret@", 1)

	// A site that demands its own Basic auth, on loopback (so it bypasses the
	// proxy): it must never be offered the proxy's password.
	var siteSawAuthHeader syncutil.Mutex
	siteGotCreds := false
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			siteSawAuthHeader.Lock()
			siteGotCreds = true
			siteSawAuthHeader.Unlock()
		}
		if r.URL.Path == "/basic" {
			w.Header().Set("WWW-Authenticate", `Basic realm="site"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`<html><head><title>direct</title></head></html>`))
	}))
	t.Cleanup(site.Close)

	m, err := NewManagerWithOptions(Options{ProxyURL: proxyURL})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	// A non-local host goes through the proxy, which challenges; the browser
	// answers the 407 from the configured credentials.
	res, err := m.Navigate(ctx, "http://intranet.test/page", "load")
	if err != nil {
		t.Fatalf("Navigate via proxy: %v", err)
	}
	if res.Title != "via proxy" {
		t.Errorf("Title = %q, want %q (proxy auth was not answered)", res.Title, "via proxy")
	}
	if authed() == 0 || challenged() == 0 {
		t.Errorf("proxy saw authed=%d challenged=%d, want a challenge then an authenticated retry", authed(), challenged())
	}

	// Loopback bypasses the proxy: dev servers keep working, unproxied. (Judged
	// by host, not by request count — Chrome fetches a favicon through the
	// proxy asynchronously after the page above.)
	if res, err := m.Navigate(ctx, site.URL, "load"); err != nil || res.Title != "direct" {
		t.Fatalf("loopback navigation: %+v, %v", res, err)
	}
	siteHost := strings.TrimPrefix(site.URL, "http://")
	for _, h := range hosts() {
		if h == siteHost {
			t.Errorf("a loopback request (%s) went through the proxy; proxy saw hosts %v", h, hosts())
		}
	}

	// A site's own auth challenge is not answered with the proxy password.
	_, _ = m.Navigate(ctx, site.URL+"/basic", "load")
	siteSawAuthHeader.Lock()
	leaked := siteGotCreds
	siteSawAuthHeader.Unlock()
	if leaked {
		t.Error("the proxy credentials were offered to a site's own auth challenge")
	}
}

// fakeBrowserbase is a stand-in for the Browserbase API that hands out a real
// local Chrome's CDP URL as the session's connectUrl.
type fakeBrowserbase struct {
	srv *httptest.Server

	mu       syncutil.Mutex
	created  []map[string]any
	released []string
}

func newFakeBrowserbase(t *testing.T, connectURL string) *fakeBrowserbase {
	t.Helper()
	f := &fakeBrowserbase{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-BB-API-Key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sessions":
			f.created = append(f.created, body)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "sess-1", "connectUrl": connectURL})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
			f.released = append(f.released, strings.TrimPrefix(r.URL.Path, "/v1/sessions/")+":"+body["status"].(string))
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBrowserbase) releases() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.released...)
}

func TestIntegration_RemoteSessionThroughBrowserbaseClient(t *testing.T) {
	skipIfNoBrowser(t)
	bin, err := findChromeBinary()
	if err != nil {
		t.Skip(err)
	}
	// Stand up a Chrome to play the rented cloud browser.
	l := launcher.New().Bin(bin).Headless(true).UserDataDir(t.TempDir()).Leakless(true)
	wsURL, err := l.Launch()
	if err != nil {
		t.Fatalf("launch stand-in remote Chrome: %v", err)
	}
	t.Cleanup(func() { l.Kill(); l.Cleanup() })

	api := newFakeBrowserbase(t, wsURL)
	srv := newFixtureServer(t)
	m, err := NewManagerWithOptions(Options{
		Provider: ProviderBrowserbase,
		Browserbase: BrowserbaseOptions{
			APIKey: "test-key", ProjectID: "proj-1", BaseURL: api.srv.URL,
			SessionTimeoutSeconds: 600,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if st := m.Status(); !st.Remote || st.Provider != ProviderBrowserbase || st.BinaryPath != "" {
		t.Errorf("Status before launch = %+v, want a remote browserbase provider with no local binary", st)
	}

	// The whole action surface works over the remote CDP connection.
	res, err := m.Navigate(ctx, srv.URL, "load")
	if err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if res.Title != "Fixture" {
		t.Errorf("Title = %q", res.Title)
	}
	if err := m.Type(ctx, "#q", "cloud", false); err != nil {
		t.Fatalf("Type: %v", err)
	}
	if err := m.Click(ctx, "#go"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if err := m.Wait(ctx, "#out", "clicked:cloud", false, 5*time.Second); err != nil {
		t.Fatalf("remote page did not react: %v", err)
	}
	out, err := m.Inspect(ctx, "", InspectOptions{InteractiveOnly: true})
	if err != nil || !strings.Contains(out, `@e`) {
		t.Fatalf("Inspect over a remote session: %q, %v", out, err)
	}

	st := m.Status()
	if !st.Active || !st.Remote || st.Provider != ProviderBrowserbase || st.ProfileDir != "" {
		t.Errorf("Status after launch = %+v", st)
	}

	// File transfer needs a shared filesystem: refused, clearly.
	if err := m.Upload(ctx, "#q", []string{"/tmp/x"}); !errors.Is(err, ErrUnsupportedRemote) {
		t.Errorf("Upload on a remote session: err = %v, want ErrUnsupportedRemote", err)
	}
	if _, err := m.Download(ctx, "#go", "", t.TempDir()+"/out"); !errors.Is(err, ErrUnsupportedRemote) {
		t.Errorf("Download on a remote session: err = %v, want ErrUnsupportedRemote", err)
	}

	// The session request carried the configured flags.
	api.mu.Lock()
	created := api.created
	api.mu.Unlock()
	if len(created) != 1 || created[0]["projectId"] != "proj-1" || created[0]["proxies"] != true ||
		created[0]["keepAlive"] != true || created[0]["timeout"] != float64(600) {
		t.Errorf("session create body = %v", created)
	}

	// Closing releases the rented session — it bills until released.
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if rel := api.releases(); len(rel) != 1 || rel[0] != "sess-1:REQUEST_RELEASE" {
		t.Errorf("releases = %v, want the session released once", rel)
	}
}

func TestIntegration_RemoteConnectFailureReleasesTheRentedSession(t *testing.T) {
	skipIfNoBrowser(t)
	// connectUrl points at nothing: the connection fails after the session
	// was created, so the provider-side session must be released at once.
	api := newFakeBrowserbase(t, "ws://127.0.0.1:1/devtools/browser/none")
	m, err := NewManagerWithOptions(Options{
		Provider:    ProviderBrowserbase,
		Browserbase: BrowserbaseOptions{APIKey: "test-key", ProjectID: "proj-1", BaseURL: api.srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := m.Navigate(ctx, "http://example.com", "load"); !errors.Is(err, ErrLaunchFailed) {
		t.Fatalf("Navigate: err = %v, want ErrLaunchFailed", err)
	}
	if rel := api.releases(); len(rel) != 1 || rel[0] != "sess-1:REQUEST_RELEASE" {
		t.Errorf("a failed connect leaked the rented session; releases = %v", rel)
	}
}

// With a proxy password to supply, every request is intercepted (see
// armRequestGuard). The metadata floor must hold in that mode too, and
// ordinary requests must still flow.
func TestIntegration_MetadataFloorHoldsInProxyAuthMode(t *testing.T) {
	skipIfNoBrowser(t)
	proxy, _, _, _ := newProxyServer(t, "ada", "s3cret")
	srv := newExtFixtureServer(t)

	orig := blockedURLPatterns
	blockedURLPatterns = append(append([]string(nil), orig...), "*://127.0.0.1:*/blocked/*")
	defer func() { blockedURLPatterns = orig }()

	m, err := NewManagerWithOptions(Options{ProxyURL: strings.Replace(proxy.URL, "http://", "http://ada:s3cret@", 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/redir-metadata", "load"); !errors.Is(err, ErrBlockedURL) {
		t.Errorf("redirect into metadata endpoint in proxy-auth mode: err = %v, want ErrBlockedURL", err)
	}
	if _, err := m.Navigate(ctx, srv.URL+"/fetches", "load"); err != nil {
		t.Fatalf("an ordinary page must still load in proxy-auth mode: %v", err)
	}
	if err := m.Wait(ctx, "#out", "fulfilled", false, 5*time.Second); err != nil {
		t.Fatalf("fetches did not settle: %v", err)
	}
	got, err := m.Eval(ctx, `document.getElementById('out').textContent`)
	if err != nil || got != `"rejected,fulfilled"` {
		t.Errorf("fetch outcomes = %s, %v; want the blocked one rejected and the ordinary one fulfilled", got, err)
	}
}
