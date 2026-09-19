package browser

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// ProviderCamofox drives a self-hosted Camofox server
// (https://github.com/jo-inc/camofox-browser): a Firefox fork with
// fingerprint spoofing built in, wrapped in a small REST API. Unlike the
// other providers it is not CDP — there is no DevTools protocol to speak —
// so it is a separate session type behind the same interface, implementing
// what the REST API offers and refusing the rest with ErrUnsupportedRemote.
const ProviderCamofox = "camofox"

// CamofoxOptions configures ProviderCamofox.
type CamofoxOptions struct {
	// URL is the server's base URL, e.g. http://localhost:9377.
	URL string
	// APIKey is sent as a bearer token when set; the caller resolves it from
	// the environment (never a config file), and it is never logged.
	APIKey string
	// HTTPClient overrides the client (tests).
	HTTPClient *http.Client
}

// camofoxTimeout bounds every REST call to the Camofox server. Navigation is
// the slow one; it gets the manager's own action deadline via ctx as well.
const camofoxTimeout = 60 * time.Second

// camofoxClient is the thin REST client: JSON in, JSON (or PNG) out.
type camofoxClient struct {
	base string
	key  string
	http *http.Client
}

func newCamofoxClient(o CamofoxOptions) *camofoxClient {
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: camofoxTimeout}
	}
	return &camofoxClient{base: strings.TrimRight(strings.TrimSpace(o.URL), "/"), key: o.APIKey, http: hc}
}

// camofoxHTTPError is a non-2xx answer.
type camofoxHTTPError struct {
	Status int
	Body   string
}

func (e *camofoxHTTPError) Error() string {
	return fmt.Sprintf("camofox returned HTTP %d: %s", e.Status, e.Body)
}

// do makes one request. body (if non-nil) is JSON-encoded; a JSON response is
// decoded into out (if non-nil), and rawOut receives the bytes for binary
// endpoints instead.
func (c *camofoxClient) do(ctx context.Context, method, path string, query url.Values, body, out any, rawOut *[]byte) error {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &camofoxHTTPError{Status: resp.StatusCode, Body: excerpt(data)}
	}
	if rawOut != nil {
		*rawOut = data
		return nil
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("camofox sent an unreadable response: %w", err)
		}
	}
	return nil
}

// camofoxSession is one Camofox identity with (at most) one tab. Camofox
// keeps the browser state server-side keyed by userId; everything here is
// bookkeeping over that.
type camofoxSession struct {
	c          *camofoxClient
	userID     string
	listItemID string

	mu    syncutil.Mutex
	tabID string
	url   string

	spills spillStore
}

// launchCamofoxSession checks the server is up and opens a tab on a fresh,
// random identity — the same isolated-session model as the local provider: no
// state from a previous run, torn down when the session closes.
func launchCamofoxSession(ctx context.Context, c *camofoxClient) (pageSession, error) {
	if c.base == "" {
		return nil, fmt.Errorf("%w: Camofox needs the server URL ([browser.camofox] url, or $CAMOFOX_URL)", ErrProviderNotConfigured)
	}
	if err := c.do(ctx, http.MethodGet, "/health", nil, nil, nil, nil); err != nil {
		return nil, fmt.Errorf("%w: Camofox server at %s is not reachable: %v", ErrLaunchFailed, c.base, err)
	}
	s := &camofoxSession{c: c, userID: "yottacode_" + randHex(5), listItemID: "task_" + randHex(6)}
	if err := s.ensureTab(ctx, "about:blank"); err != nil {
		return nil, fmt.Errorf("%w: open a Camofox tab: %v", ErrLaunchFailed, err)
	}
	return s, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// unsupported is the error for an action Camofox's REST API has no verb for.
func camofoxUnsupported(what string) error {
	return fmt.Errorf("%w: %s (the Camofox backend has a smaller surface than the local browser — see docs/browser.md)", ErrUnsupportedRemote, what)
}

// tab returns the current tab id, or an error before one exists.
func (s *camofoxSession) tab() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tabID == "" {
		return "", errors.New("no Camofox tab is open; call browser_navigate first")
	}
	return s.tabID, nil
}

func (s *camofoxSession) setURL(u string) {
	if u == "" {
		return
	}
	s.mu.Lock()
	s.url = u
	s.mu.Unlock()
}

// ensureTab opens a tab on url if this identity has none yet.
func (s *camofoxSession) ensureTab(ctx context.Context, url string) error {
	s.mu.Lock()
	if s.tabID != "" {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	var out struct {
		TabID string `json:"tabId"`
	}
	err := s.c.do(ctx, http.MethodPost, "/tabs", nil, map[string]any{
		"userId": s.userID, "listItemId": s.listItemID, "url": url,
	}, &out, nil)
	if err != nil {
		return err
	}
	if out.TabID == "" {
		return errors.New("Camofox did not return a tab id")
	}
	s.mu.Lock()
	s.tabID = out.TabID
	s.url = url
	s.mu.Unlock()
	return nil
}

// tabCall POSTs to a verb of the current tab, adding the identity, and
// decodes the JSON reply into out.
func (s *camofoxSession) tabCall(ctx context.Context, verb string, body map[string]any, out any) error {
	tab, err := s.tab()
	if err != nil {
		return err
	}
	body["userId"] = s.userID
	return s.c.do(ctx, http.MethodPost, "/tabs/"+url.PathEscape(tab)+"/"+verb, nil, body, out, nil)
}

// camofoxRef turns an element target into the bare ref Camofox expects. The
// server addresses elements only by the refs its own snapshot hands out, so a
// CSS selector has nothing to resolve against.
func camofoxRef(selector string) (string, error) {
	if !isRef(selector) {
		return "", camofoxUnsupported("Camofox targets elements by ref only — run browser_inspect and pass an @eN ref, not a CSS selector")
	}
	return strings.TrimPrefix(strings.TrimSpace(selector), "@"), nil
}

func (s *camofoxSession) navigate(ctx context.Context, target, _ string) (NavigateResult, error) {
	var out struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	err := s.tabCall(ctx, "navigate", map[string]any{"url": target}, &out)
	var he *camofoxHTTPError
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		// The server garbage-collected the idle tab: open a fresh one on the
		// target instead of failing the navigation.
		s.mu.Lock()
		s.tabID = ""
		s.mu.Unlock()
		if err := s.ensureTab(ctx, target); err != nil {
			return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
		}
		return NavigateResult{URL: target}, nil
	}
	if err != nil {
		return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
	}
	if out.URL == "" {
		out.URL = target
	}
	s.setURL(out.URL)
	return NavigateResult{URL: out.URL, Title: out.Title}, nil
}

func (s *camofoxSession) screenshot(ctx context.Context, selector string, _ bool) ([]byte, error) {
	if strings.TrimSpace(selector) != "" {
		return nil, camofoxUnsupported("element screenshots")
	}
	tab, err := s.tab()
	if err != nil {
		return nil, err
	}
	var png []byte
	if err := s.c.do(ctx, http.MethodGet, "/tabs/"+url.PathEscape(tab)+"/screenshot", url.Values{"userId": {s.userID}}, nil, nil, &png); err != nil {
		return nil, fmt.Errorf("browser screenshot: %w", err)
	}
	return png, nil
}

func (s *camofoxSession) snapshotText(ctx context.Context) (string, error) {
	tab, err := s.tab()
	if err != nil {
		return "", err
	}
	var out struct {
		Snapshot string `json:"snapshot"`
	}
	if err := s.c.do(ctx, http.MethodGet, "/tabs/"+url.PathEscape(tab)+"/snapshot", url.Values{"userId": {s.userID}}, nil, &out, nil); err != nil {
		return "", fmt.Errorf("browser inspect: %w", err)
	}
	return out.Snapshot, nil
}

// inspect returns the server's own snapshot, bounded like the CDP one: over
// the display budget the full text is spilled to a file and the path given.
// Camofox snapshots are whole-page, so a selector or interactive_only has
// nothing to narrow.
func (s *camofoxSession) inspect(ctx context.Context, selector string, opts InspectOptions) (string, error) {
	if strings.TrimSpace(selector) != "" {
		return "", camofoxUnsupported("scoped snapshots")
	}
	text, err := s.snapshotText(ctx)
	if err != nil {
		return "", err
	}
	out := text
	if len(out) > maxAXChars {
		cut := maxAXChars
		for cut > 0 && out[cut]&0xC0 == 0x80 {
			cut--
		}
		out = out[:cut] + "…[truncated]"
		if path, err := s.spills.spill(text); err == nil {
			out += fmt.Sprintf("\n[full snapshot saved to %s — read_file it for the rest]", path)
		}
	}
	if opts.InteractiveOnly {
		out = "(interactive_only is not available on Camofox; showing the full snapshot)\n" + out
	}
	return out, nil
}

func (s *camofoxSession) click(ctx context.Context, selector string) error {
	ref, err := camofoxRef(selector)
	if err != nil {
		return err
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := s.tabCall(ctx, "click", map[string]any{"ref": ref}, &out); err != nil {
		return fmt.Errorf("browser click: %w", err)
	}
	s.setURL(out.URL)
	return nil
}

func (s *camofoxSession) typeText(ctx context.Context, selector, text string, submit bool) error {
	ref, err := camofoxRef(selector)
	if err != nil {
		return err
	}
	if err := s.tabCall(ctx, "type", map[string]any{"ref": ref, "text": text}, nil); err != nil {
		return fmt.Errorf("browser type: %w", err)
	}
	if submit {
		if err := s.tabCall(ctx, "press", map[string]any{"key": "Enter"}, nil); err != nil {
			return fmt.Errorf("browser type: submit: %w", err)
		}
	}
	return nil
}

func (s *camofoxSession) hotkey(ctx context.Context, keys string) error {
	if err := s.tabCall(ctx, "press", map[string]any{"key": keys}, nil); err != nil {
		return fmt.Errorf("browser hotkey %q: %w", keys, err)
	}
	return nil
}

// scroll maps to Camofox's up/down verbs; anything finer has no equivalent.
func (s *camofoxSession) scroll(ctx context.Context, selector string, deltaX, deltaY float64) error {
	if strings.TrimSpace(selector) != "" {
		return camofoxUnsupported("scrolling an element into view")
	}
	if deltaX != 0 || deltaY == 0 {
		return camofoxUnsupported("horizontal scrolling (Camofox scrolls up or down only)")
	}
	dir := "down"
	if deltaY < 0 {
		dir = "up"
	}
	if err := s.tabCall(ctx, "scroll", map[string]any{"direction": dir}, nil); err != nil {
		return fmt.Errorf("browser scroll: %w", err)
	}
	return nil
}

// camofoxPollInterval is how often wait re-reads the snapshot.
const camofoxPollInterval = 500 * time.Millisecond

// wait supports one condition — text appearing — by polling the snapshot,
// since Camofox has no wait verb; selectors and network-idle need a browser
// engine this API doesn't expose.
func (s *camofoxSession) wait(ctx context.Context, selector, text string, networkIdle bool, timeout time.Duration) error {
	if strings.TrimSpace(selector) != "" {
		return camofoxUnsupported("waiting on a selector")
	}
	if networkIdle {
		return camofoxUnsupported("waiting for network idle")
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(camofoxPollInterval)
	defer tick.Stop()
	for {
		snap, err := s.snapshotText(ctx)
		if err == nil && strings.Contains(snap, text) {
			return nil
		}
		select {
		case <-ctx.Done():
			return classifyErr(ctx.Err(), ErrSelectorNotFound)
		case <-tick.C:
		}
	}
}

func (s *camofoxSession) snapshot() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tabID == "" {
		return s.url, 0
	}
	return s.url, 1
}

// close deletes the server-side identity — its cookies, tabs and profile —
// which is what makes the session isolated.
func (s *camofoxSession) close() error {
	defer s.spills.remove()
	ctx, cancel := context.WithTimeout(context.Background(), remoteReleaseTimeout)
	defer cancel()
	return s.c.do(ctx, http.MethodDelete, "/sessions/"+url.PathEscape(s.userID), nil, nil, nil, nil)
}

func (s *camofoxSession) alive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), remoteProbeTimeout)
	defer cancel()
	return s.c.do(ctx, http.MethodGet, "/health", nil, nil, nil, nil) == nil
}

func (s *camofoxSession) forceCleanup() { _ = s.close() }

func (s *camofoxSession) tabs(context.Context) []TabInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tabID == "" {
		return nil
	}
	return []TabInfo{{Index: 0, ID: s.tabID, URL: s.url, Active: true}}
}

func (s *camofoxSession) switchTab(index int) error {
	if index != 0 {
		return fmt.Errorf("%w: index %d — Camofox sessions have a single tab", ErrTabNotFound, index)
	}
	return nil
}

func (s *camofoxSession) closeTab(context.Context, int) error {
	return errors.New("cannot close the only remaining tab; use browser_close to end the session instead")
}

func (s *camofoxSession) setFiles(context.Context, string, []string) error {
	return remoteFileErr("upload")
}

func (s *camofoxSession) downloadViaClick(context.Context, string, string) (*proto.PageDownloadWillBegin, error) {
	return nil, remoteFileErr("download")
}

func (s *camofoxSession) downloadViaURL(context.Context, string, string) (*proto.PageDownloadWillBegin, error) {
	return nil, remoteFileErr("download")
}

// consoleLogs/networkRequests: Camofox exposes neither, so a single note entry
// says so — an empty result would read as "the page logged nothing".
func (s *camofoxSession) consoleLogs(int) []ConsoleEntry {
	return []ConsoleEntry{{Level: "note", Text: "console capture is not available on the Camofox backend", At: time.Now()}}
}

func (s *camofoxSession) networkRequests(int) []NetworkEntry {
	return []NetworkEntry{{Method: "NOTE", URL: "network capture is not available on the Camofox backend", At: time.Now()}}
}

func (s *camofoxSession) eval(context.Context, string) (string, error) {
	return "", camofoxUnsupported("evaluating JavaScript")
}

func (s *camofoxSession) back(ctx context.Context) (NavigateResult, error) {
	var out struct {
		URL string `json:"url"`
	}
	if err := s.tabCall(ctx, "back", map[string]any{}, &out); err != nil {
		return NavigateResult{}, fmt.Errorf("browser back: %w", err)
	}
	s.setURL(out.URL)
	return NavigateResult{URL: out.URL}, nil
}

func (s *camofoxSession) screenshotAnnotated(context.Context, bool) (AnnotatedShot, error) {
	return AnnotatedShot{}, camofoxUnsupported("annotated screenshots")
}

// setDialogPolicy is a no-op: Camofox's API has no dialog handling.
func (s *camofoxSession) setDialogPolicy(bool, string) {}
