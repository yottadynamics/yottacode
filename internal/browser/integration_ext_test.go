//go:build integration

// Real-Chrome tests for the element-ref, eval, dialog-policy, navigation
// floor, back and snapshot-spill features. Same skip-when-no-browser
// convention as integration_test.go; run with
// `go test -tags integration ./internal/browser/...`.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const refsFixturePage = `<!DOCTYPE html>
<html><head><title>Refs Fixture</title></head>
<body>
  <h1>Sign in</h1>
  <label>Email <input id="email" type="text"></label>
  <label><input id="agree" type="checkbox"> I agree</label>
  <button id="go" onclick="document.getElementById('out').textContent = 'submitted:' + document.getElementById('email').value + ':' + document.getElementById('agree').checked">Submit</button>
  <button id="off" disabled>Disabled one</button>
  <a href="/other">Other page</a>
  <div id="out"></div>
</body></html>`

const otherFixturePage = `<!DOCTYPE html>
<html><head><title>Other Fixture</title></head><body><p>other</p><button>Elsewhere</button></body></html>`

const confirmFixturePage = `<!DOCTYPE html>
<html><head><title>Confirm Fixture</title></head>
<body>
  <button id="ask" onclick="document.getElementById('out').textContent = confirm('really?') ? 'yes' : 'no'">Ask</button>
  <button id="prompt" onclick="document.getElementById('out').textContent = 'name:' + prompt('who?', 'dflt')">Prompt</button>
  <div id="out"></div>
</body></html>`

const manyLinksFixturePage = `<!DOCTYPE html>
<html><head><title>Many Links</title></head><body>%s<div id="out"></div></body></html>`

// newExtFixtureServer serves this file's fixtures.
func newExtFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redir-metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
			return
		case "/blocked/data":
			_, _ = w.Write([]byte("should never be fetched"))
			return
		case "/ok/data":
			_, _ = w.Write([]byte("fine"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/other":
			_, _ = w.Write([]byte(otherFixturePage))
		case "/confirm":
			_, _ = w.Write([]byte(confirmFixturePage))
		case "/many":
			var b strings.Builder
			for i := 1; i <= 40; i++ {
				fmt.Fprintf(&b, `<a href="#l%d" onclick="document.getElementById('out').textContent='clicked-%d'">Link number %d</a> `, i, i, i)
			}
			_, _ = fmt.Fprintf(w, manyLinksFixturePage, b.String())
		case "/fetches":
			_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>Fetches</title></head><body><div id="out"></div><script>
Promise.allSettled([fetch('/blocked/data'), fetch('/ok/data')]).then(function(rs){document.getElementById('out').textContent = rs.map(function(r){return r.status}).join(',');});
</script></body></html>`))
		default:
			_, _ = w.Write([]byte(refsFixturePage))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestIntegration_RefsDriveClickTypeAndGoStale(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newExtFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	out, err := m.Inspect(ctx, "", InspectOptions{InteractiveOnly: true})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	// Compact view: only controls, each with a ref; the heading is not listed.
	for _, want := range []string{`textbox "Email"`, `checkbox "I agree"`, `button "Submit"`, `[disabled]`, `link "Other page"`} {
		if !strings.Contains(out, want) {
			t.Errorf("interactive snapshot missing %q, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Sign in") {
		t.Errorf("interactive_only should drop the heading, got:\n%s", out)
	}

	ref := func(needle string) string {
		t.Helper()
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, needle) {
				return strings.Fields(line)[0]
			}
		}
		t.Fatalf("no line containing %q in:\n%s", needle, out)
		return ""
	}
	email, agree, submit := ref(`textbox "Email"`), ref(`checkbox "I agree"`), ref(`button "Submit"`)
	if !isRef(email) || !isRef(agree) || !isRef(submit) {
		t.Fatalf("expected @eN refs, got %q %q %q", email, agree, submit)
	}

	if err := m.Type(ctx, email, "a@b.c", false); err != nil {
		t.Fatalf("Type by ref: %v", err)
	}
	if err := m.Click(ctx, agree); err != nil {
		t.Fatalf("Click checkbox by ref: %v", err)
	}
	if err := m.Click(ctx, submit); err != nil {
		t.Fatalf("Click submit by ref: %v", err)
	}
	if err := m.Wait(ctx, "#out", "submitted:a@b.c:true", false, 5*time.Second); err != nil {
		t.Fatalf("form did not submit through refs: %v", err)
	}

	// A ref that was never issued is stale, not "selector not found".
	if err := m.Click(ctx, "@e999"); !errors.Is(err, ErrStaleRef) {
		t.Errorf("unknown ref: err = %v, want ErrStaleRef", err)
	}

	// After navigating away, the old page's nodes are gone: the ref is stale.
	if _, err := m.Navigate(ctx, srv.URL+"/other", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.Click(ctx, submit); !errors.Is(err, ErrStaleRef) {
		t.Errorf("ref after navigation: err = %v, want ErrStaleRef", err)
	}
}

func TestIntegration_ScopedInspectKeepsEarlierRefs(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newExtFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	full, err := m.Inspect(ctx, "", InspectOptions{InteractiveOnly: true})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var submitRef string
	for _, line := range strings.Split(full, "\n") {
		if strings.Contains(line, `button "Submit"`) {
			submitRef = strings.Fields(line)[0]
		}
	}
	if submitRef == "" {
		t.Fatalf("no submit ref in:\n%s", full)
	}
	// A scoped inspect adds to the registry; it must not invalidate earlier refs.
	if _, err := m.Inspect(ctx, "#go", InspectOptions{}); err != nil {
		t.Fatalf("scoped Inspect: %v", err)
	}
	if err := m.Click(ctx, submitRef); err != nil {
		t.Errorf("earlier ref invalid after a scoped inspect: %v", err)
	}
}

func TestIntegration_EvalResults(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newExtFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	cases := []struct{ expr, want string }{
		{`1 + 1`, `2`},
		{`document.title`, `"Refs Fixture"`},
		{`({a: 1, b: [true, null]})`, `{"a":1,"b":[true,null]}`},
		{`undefined`, `undefined`},
		{`null`, `null`},
		{`NaN`, `NaN`},
		{`Promise.resolve('later')`, `"later"`},
		{`document.querySelectorAll('button').length`, `2`},
	}
	for _, c := range cases {
		got, err := m.Eval(ctx, c.expr)
		if err != nil {
			t.Errorf("Eval(%q): %v", c.expr, err)
			continue
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %q, want %q", c.expr, got, c.want)
		}
	}

	// A DOM node can't be serialized by value: it comes back described.
	got, err := m.Eval(ctx, `document.body`)
	if err != nil {
		t.Fatalf("Eval(document.body): %v", err)
	}
	if got == "" {
		t.Errorf("Eval(document.body) returned nothing")
	}

	// A throw is an ErrEvalFailed carrying the message, not a hang or a panic.
	_, err = m.Eval(ctx, `nope.missing`)
	if !errors.Is(err, ErrEvalFailed) || !strings.Contains(err.Error(), "nope") {
		t.Errorf("throwing expression: err = %v, want ErrEvalFailed naming the ReferenceError", err)
	}

	// A runaway script is cut off by the script timeout, and the session
	// stays usable afterward.
	start := time.Now()
	_, err = m.Eval(ctx, `(() => { const t = Date.now(); while (Date.now() - t < 60000) {} })()`)
	if !errors.Is(err, ErrEvalFailed) {
		t.Errorf("runaway script: err = %v, want ErrEvalFailed", err)
	}
	if d := time.Since(start); d > 40*time.Second {
		t.Errorf("runaway script held the session for %s", d)
	}
	if got, err := m.Eval(ctx, `2 * 21`); err != nil || got != "42" {
		t.Errorf("session unusable after runaway script: %q, %v", got, err)
	}
}

func TestIntegration_DialogPolicy(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newExtFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	// The policy can be set before anything is launched, and survives into
	// the session that launches afterward.
	if err := m.SetDialogPolicy(false, ""); err != nil {
		t.Fatalf("SetDialogPolicy: %v", err)
	}
	if _, err := m.Navigate(ctx, srv.URL+"/confirm", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// Default: dismiss, so confirm() is false.
	if err := m.Click(ctx, "#ask"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if err := m.Wait(ctx, "", "no", false, 5*time.Second); err != nil {
		t.Fatalf("dismissed confirm did not yield 'no': %v", err)
	}

	// Accept: confirm() is true.
	if err := m.SetDialogPolicy(true, ""); err != nil {
		t.Fatalf("SetDialogPolicy: %v", err)
	}
	if err := m.Click(ctx, "#ask"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if err := m.Wait(ctx, "", "yes", false, 5*time.Second); err != nil {
		t.Fatalf("accepted confirm did not yield 'yes': %v", err)
	}

	// prompt(): accepted with explicit text, then with its own default.
	if err := m.SetDialogPolicy(true, "Ada"); err != nil {
		t.Fatalf("SetDialogPolicy: %v", err)
	}
	if err := m.Click(ctx, "#prompt"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if err := m.Wait(ctx, "", "name:Ada", false, 5*time.Second); err != nil {
		t.Fatalf("prompt did not receive the supplied text: %v", err)
	}
	if err := m.SetDialogPolicy(true, ""); err != nil {
		t.Fatalf("SetDialogPolicy: %v", err)
	}
	if err := m.Click(ctx, "#prompt"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	if err := m.Wait(ctx, "", "name:dflt", false, 5*time.Second); err != nil {
		t.Fatalf("prompt did not fall back to its default: %v", err)
	}

	// Every dialog is recorded with how it was answered.
	logs, err := m.ConsoleLogs(ctx, 0)
	if err != nil {
		t.Fatalf("ConsoleLogs: %v", err)
	}
	var dialogs []string
	for _, e := range logs {
		if e.Level == "dialog" {
			dialogs = append(dialogs, e.Text)
		}
	}
	want := []string{`confirm "really?" -> dismissed`, `confirm "really?" -> accepted`, `prompt "who?" -> accepted`, `prompt "who?" -> accepted`}
	if len(dialogs) != len(want) {
		t.Fatalf("recorded dialogs = %q, want %q", dialogs, want)
	}
	for i := range want {
		if dialogs[i] != want[i] {
			t.Errorf("dialog[%d] = %q, want %q", i, dialogs[i], want[i])
		}
	}
}

func TestIntegration_MetadataFloor(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newExtFixtureServer(t)

	// A direct navigation is refused before any browser is launched.
	fresh := NewManager()
	launched := false
	realNew := fresh.newSession
	fresh.newSession = func(ctx context.Context, bin, dir string) (pageSession, error) {
		launched = true
		return realNew(ctx, bin, dir)
	}
	if _, err := fresh.Navigate(context.Background(), "http://169.254.169.254/latest/meta-data/", "load"); !errors.Is(err, ErrBlockedURL) {
		t.Errorf("direct metadata navigation: err = %v, want ErrBlockedURL", err)
	}
	if launched {
		t.Error("refusing a blocked URL must not launch a browser")
	}
	_ = fresh.Close(context.Background())

	// The request-level block is armed on every page: a redirect into the
	// metadata endpoint and a fetch() to a blocked pattern are both stopped.
	orig := blockedURLPatterns
	blockedURLPatterns = append(append([]string(nil), orig...), "*://127.0.0.1:*/blocked/*")
	defer func() { blockedURLPatterns = orig }()

	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL+"/redir-metadata", "load"); !errors.Is(err, ErrBlockedURL) {
		t.Errorf("redirect into metadata endpoint: err = %v, want ErrBlockedURL", err)
	}

	if _, err := m.Navigate(ctx, srv.URL+"/fetches", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := m.Wait(ctx, "#out", "fulfilled", false, 5*time.Second); err != nil {
		// allSettled: blocked → "rejected", ok → "fulfilled"
		t.Fatalf("fetches did not settle: %v", err)
	}
	got, err := m.Eval(ctx, `document.getElementById('out').textContent`)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if got != `"rejected,fulfilled"` {
		t.Errorf("fetch outcomes = %s, want the blocked one rejected and the other fulfilled", got)
	}
	reqs, err := m.NetworkRequests(ctx, 0)
	if err != nil {
		t.Fatalf("NetworkRequests: %v", err)
	}
	var sawBlocked bool
	for _, r := range reqs {
		if strings.Contains(r.URL, "/blocked/data") {
			sawBlocked = r.Failed
		}
	}
	if !sawBlocked {
		t.Errorf("blocked request not recorded as failed: %+v", reqs)
	}
}

func TestIntegration_Back(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newExtFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := m.Back(ctx); !errors.Is(err, ErrNoHistory) {
		t.Errorf("Back on the first page: err = %v, want ErrNoHistory", err)
	}
	if _, err := m.Navigate(ctx, srv.URL+"/other", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	res, err := m.Back(ctx)
	if err != nil {
		t.Fatalf("Back: %v", err)
	}
	if res.URL != srv.URL+"/" && res.URL != srv.URL {
		t.Errorf("Back landed on %q, want %q", res.URL, srv.URL)
	}
	if res.Title != "Refs Fixture" {
		t.Errorf("Back title = %q, want %q", res.Title, "Refs Fixture")
	}
}

func TestIntegration_OversizedSnapshotSpillsAndRefsSurvive(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newExtFixtureServer(t)
	m := NewManager()
	ctx := context.Background()

	origNodes := maxAXNodes
	maxAXNodes = 6
	defer func() { maxAXNodes = origNodes }()

	if _, err := m.Navigate(ctx, srv.URL+"/many", "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	out, err := m.Inspect(ctx, "", InspectOptions{InteractiveOnly: true})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !strings.Contains(out, "…[truncated]") {
		t.Fatalf("expected the shown snapshot to be truncated, got:\n%s", out)
	}
	const marker = "full snapshot saved to "
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("truncated snapshot did not say where the full one went:\n%s", out)
	}
	path := strings.Fields(out[i+len(marker):])[0]

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("spill file unreadable: %v", err)
	}
	full := string(data)
	if !strings.Contains(full, `Link number 40`) {
		t.Errorf("spill file is missing the last link:\n%s", full)
	}
	if strings.Contains(out, `Link number 40`) {
		t.Errorf("the shown text should have been cut before the last link")
	}

	// A ref that only exists past the cut still resolves.
	var lastRef string
	for _, line := range strings.Split(full, "\n") {
		if strings.Contains(line, `Link number 40`) {
			lastRef = strings.Fields(line)[0]
		}
	}
	if err := m.Click(ctx, lastRef); err != nil {
		t.Fatalf("Click past-the-cut ref %q: %v", lastRef, err)
	}
	if err := m.Wait(ctx, "#out", "clicked-40", false, 5*time.Second); err != nil {
		t.Fatalf("past-the-cut ref did not click the right link: %v", err)
	}

	// The scratch file is session-scoped: gone once the browser closes.
	if err := m.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("spill file %s survived browser close (stat err = %v)", path, err)
	}
}

func TestIntegration_AnnotatedScreenshot(t *testing.T) {
	skipIfNoBrowser(t)
	srv := newExtFixtureServer(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, srv.URL, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	plain, err := m.Screenshot(ctx, "", false)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	shot, err := m.ScreenshotAnnotated(ctx, false)
	if err != nil {
		t.Fatalf("ScreenshotAnnotated: %v", err)
	}
	if len(shot.PNG) < 8 || string(shot.PNG[1:4]) != "PNG" {
		t.Fatalf("not a PNG (len=%d)", len(shot.PNG))
	}
	// The fixture has 5 controls: textbox, checkbox, 2 buttons, 1 link.
	if shot.Labeled != 5 || shot.Skipped != 0 {
		t.Errorf("Labeled/Skipped = %d/%d, want 5/0", shot.Labeled, shot.Skipped)
	}
	if string(shot.PNG) == string(plain) {
		t.Error("the annotated capture is byte-identical to the plain one — no boxes were drawn")
	}
	if dir := os.Getenv("YC_KEEP_SHOTS"); dir != "" {
		_ = os.WriteFile(dir+"/annotated.png", shot.PNG, 0o644)
		_ = os.WriteFile(dir+"/plain.png", plain, 0o644)
	}

	// The overlay must be gone again, and the numbers on the image must be
	// live refs: box 3 is @e3, the Submit button.
	if got, err := m.Eval(ctx, "document.getElementById('"+annotationOverlayID+"') === null"); err != nil || got != "true" {
		t.Errorf("annotation overlay left behind in the page: %q, %v", got, err)
	}
	if err := m.Type(ctx, "@e1", "x@y.z", false); err != nil {
		t.Fatalf("Type @e1: %v", err)
	}
	if err := m.Click(ctx, "@e3"); err != nil {
		t.Fatalf("Click @e3: %v", err)
	}
	if err := m.Wait(ctx, "#out", "submitted:x@y.z:false", false, 5*time.Second); err != nil {
		t.Fatalf("@e3 is not the Submit button the annotated image numbered 3: %v", err)
	}
}
