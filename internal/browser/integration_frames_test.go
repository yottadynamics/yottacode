//go:build integration

// Real-Chrome tests for iframe support: refs issued for controls inside
// same-site and cross-site (out-of-process) frames must be clickable and
// typable like top-level ones.
package browser

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

const frameInnerPage = `<!DOCTYPE html>
<html><head><title>Inner</title></head>
<body>
  <label>Inner name <input id="name" type="text"></label>
  <button id="say" onclick="parent.postMessage('%s:' + document.getElementById('name').value, '*')">Say hi</button>
</body></html>`

// newFramesFixture serves a parent page at http://127.0.0.1:P embedding two
// frames: a same-origin one and one on http://localhost:P — a different
// *site*, which Chrome isolates into its own process (an OOPIF).
func newFramesFixture(t *testing.T) (parentURL string) {
	t.Helper()
	parentURL, _ = newGuardedFramesFixture(t)
	return parentURL
}

// newGuardedFramesFixture is newFramesFixture plus routes for the request
// guard: /guard embeds a cross-site frame that fetch()es a "blocked" path and
// another whose *document* is a blocked path. blockedHits counts requests that
// actually reached a /blocked/ path on the server — the guard must keep it 0.
func newGuardedFramesFixture(t *testing.T) (parentURL string, blockedHits func() int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	var hits atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if strings.HasPrefix(r.URL.Path, "/blocked/") {
			hits.Add(1)
			_, _ = w.Write([]byte(`<html><body><script>parent.postMessage('blocked-page-loaded','*')</script></body></html>`))
			return
		}
		switch r.URL.Path {
		case "/ok/data":
			_, _ = w.Write([]byte("fine"))
		case "/inner-fetch":
			_, _ = w.Write([]byte(`<html><body><script>
Promise.allSettled([fetch('/blocked/data'), fetch('/ok/data')]).then(function(rs){
  parent.postMessage('fetch:' + rs.map(function(r){return r.status}).join(','), '*');
});</script></body></html>`))
		case "/guard":
			fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Guard</title></head><body>
<iframe id="g1" src="http://localhost:%[1]d/inner-fetch" width="200" height="50"></iframe>
<iframe id="g2" src="http://localhost:%[1]d/blocked/page" width="200" height="50"></iframe>
<div id="out"></div>
<script>window.addEventListener('message', function(e){ document.getElementById('out').textContent += '[' + e.data + ']'; });</script>
</body></html>`, port)
		case "/inner-same":
			fmt.Fprintf(w, frameInnerPage, "same")
		case "/inner-cross":
			fmt.Fprintf(w, frameInnerPage, "cross")
		default:
			fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Parent</title></head><body>
<button id="top" onclick="document.getElementById('out').textContent='top-clicked'">Top button</button>
<iframe id="f1" title="Same origin frame" src="/inner-same" width="300" height="120"></iframe>
<iframe id="f2" title="Cross site frame" src="http://localhost:%d/inner-cross" width="300" height="120"></iframe>
<div id="out"></div>
<script>window.addEventListener('message', function(e){ document.getElementById('out').textContent = 'msg=' + e.data; });</script>
</body></html>`, port)
		}
	}))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return fmt.Sprintf("http://127.0.0.1:%d/", port), func() int { return int(hits.Load()) }
}

// waitForFrameButtons polls inspect until it lists want "Say hi" buttons — one
// per loaded frame — and returns that snapshot. A frame's document loads after
// the page's own load event, so a fixed sleep either wastes time or, on a slow
// runner, is too short; polling is neither.
func waitForFrameButtons(t *testing.T, want int, inspect func() (string, error)) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var out string
	var err error
	for {
		out, err = inspect()
		if err == nil && strings.Count(out, `button "Say hi"`) >= want {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("frames never finished loading (want %d buttons, err=%v); last snapshot:\n%s", want, err, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestIntegration_FramesInspectListsControlsInsideFrames(t *testing.T) {
	skipIfNoBrowser(t)
	parent := newFramesFixture(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, parent, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	out := waitForFrameButtons(t, 2, func() (string, error) {
		return m.Inspect(ctx, "", InspectOptions{InteractiveOnly: true})
	})
	for _, want := range []string{
		`button "Top button"`,
		`--- frame "f1" http://127.0.0.1:`,
		`--- frame "f2" http://localhost:`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot missing %q, got:\n%s", want, out)
		}
	}
	if n := strings.Count(out, `button "Say hi"`); n != 2 {
		t.Errorf("want the inner button from BOTH frames (same-site and cross-site), found %d:\n%s", n, out)
	}
}

// TestIntegration_FramesRefsTypeAndClick drives controls inside both frame
// kinds by ref and proves each action landed in the right frame: the inner
// button posts what the inner textbox holds up to the parent page.
func TestIntegration_FramesRefsTypeAndClick(t *testing.T) {
	skipIfNoBrowser(t)
	parent := newFramesFixture(t)
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(func() { _ = m.Close(ctx) })

	if _, err := m.Navigate(ctx, parent, "load"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	out := waitForFrameButtons(t, 2, func() (string, error) {
		return m.Inspect(ctx, "", InspectOptions{InteractiveOnly: true})
	})

	// Collect the refs per frame section, in document order.
	type frameRefs struct{ name, say string }
	var frames []frameRefs
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "--- frame"):
			frames = append(frames, frameRefs{})
		case strings.Contains(line, `textbox "Inner name"`) && len(frames) > 0:
			frames[len(frames)-1].name = strings.Fields(line)[0]
		case strings.Contains(line, `button "Say hi"`) && len(frames) > 0:
			frames[len(frames)-1].say = strings.Fields(line)[0]
		}
	}
	if len(frames) != 2 || frames[0].name == "" || frames[0].say == "" || frames[1].name == "" || frames[1].say == "" {
		t.Fatalf("could not find both frames' refs in:\n%s", out)
	}

	for i, kind := range []string{"same", "cross"} {
		if err := m.Type(ctx, frames[i].name, "ada-"+kind, false); err != nil {
			t.Fatalf("%s frame: Type by ref: %v", kind, err)
		}
		if err := m.Click(ctx, frames[i].say); err != nil {
			t.Fatalf("%s frame: Click by ref: %v", kind, err)
		}
		want := "msg=" + kind + ":ada-" + kind
		if err := m.Wait(ctx, "", want, false, 5*time.Second); err != nil {
			got, _ := m.Eval(ctx, `document.getElementById('out').textContent`)
			t.Fatalf("%s frame: parent never saw %q (out = %s): %v", kind, want, got, err)
		}
	}

	// The top-level control still works alongside the frame refs.
	var topRef string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, `button "Top button"`) {
			topRef = strings.Fields(line)[0]
		}
	}
	if err := m.Click(ctx, topRef); err != nil {
		t.Fatalf("Click top-level ref: %v", err)
	}
	if err := m.Wait(ctx, "", "top-clicked", false, 5*time.Second); err != nil {
		t.Fatalf("top-level click did not land: %v", err)
	}
}

// newIsolatedSession launches Chrome the way a hardened launcher would — site
// isolation ON, so a cross-site iframe runs in its own process and is its own
// CDP target — and wraps it in a session. (rod's default launcher turns
// isolation off; the browser package must not depend on that.)
func newIsolatedSession(t *testing.T) *session {
	t.Helper()
	skipIfNoBrowser(t)
	bin, err := findChromeBinary()
	if err != nil {
		t.Skip(err)
	}
	l := launcher.New().Bin(bin).Headless(true).UserDataDir(t.TempDir()).Leakless(true).
		Delete("disable-site-isolation-trials").
		Set("disable-features", "TranslateUI").
		Set("enable-features", "IsolateOrigins,site-per-process")
	u, err := l.Launch()
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	br := rod.New().ControlURL(u)
	if err := br.Connect(); err != nil {
		l.Kill()
		l.Cleanup()
		t.Fatalf("connect: %v", err)
	}
	pg, err := br.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		l.Kill()
		l.Cleanup()
		t.Fatalf("page: %v", err)
	}
	s := newSessionCore(br, l, pg, sessionConfig{})
	t.Cleanup(func() { _ = s.close() })
	return s
}

// hasIframeTarget reports whether the browser has a cross-process iframe target.
func hasIframeTarget(t *testing.T, s *session) bool {
	t.Helper()
	res, err := proto.TargetGetTargets{}.Call(s.activePage())
	if err != nil {
		t.Fatalf("Target.getTargets: %v", err)
	}
	for _, ti := range res.TargetInfos {
		if ti.Type == "iframe" {
			return true
		}
	}
	return false
}

// TestIntegration_FramesUnderSiteIsolation is the same drive-both-frames flow
// as TestIntegration_FramesRefsTypeAndClick, with the cross-site frame in its
// own process: it is absent from the page's frame tree, so it is found by
// auto-attach and its refs are resolved (and driven) on its own target.
func TestIntegration_FramesUnderSiteIsolation(t *testing.T) {
	s := newIsolatedSession(t)
	parent := newFramesFixture(t)
	ctx := context.Background()

	if _, err := s.navigate(ctx, parent, "load"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	out := waitForFrameButtons(t, 2, func() (string, error) {
		return s.inspect(ctx, "", InspectOptions{InteractiveOnly: true})
	})
	if !hasIframeTarget(t, s) {
		t.Fatal("precondition: the cross-site frame should be a separate iframe target with site isolation on")
	}
	if n := strings.Count(out, `button "Say hi"`); n != 2 {
		t.Fatalf("want the inner button from BOTH frames, found %d:\n%s", n, out)
	}

	type frameRefs struct{ header, name, say string }
	var frames []frameRefs
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "--- frame"):
			frames = append(frames, frameRefs{header: line})
		case strings.Contains(line, `textbox "Inner name"`) && len(frames) > 0:
			frames[len(frames)-1].name = strings.Fields(line)[0]
		case strings.Contains(line, `button "Say hi"`) && len(frames) > 0:
			frames[len(frames)-1].say = strings.Fields(line)[0]
		}
	}
	if len(frames) != 2 {
		t.Fatalf("frames = %+v in:\n%s", frames, out)
	}
	for _, f := range frames {
		kind := "same"
		if strings.Contains(f.header, "localhost") {
			kind = "cross"
		}
		if err := s.typeText(ctx, f.name, "ada-"+kind, false); err != nil {
			t.Fatalf("%s frame: type by ref: %v", kind, err)
		}
		if err := s.click(ctx, f.say); err != nil {
			t.Fatalf("%s frame: click by ref: %v", kind, err)
		}
		want := "msg=" + kind + ":ada-" + kind
		if err := s.wait(ctx, "", want, false, 5*time.Second); err != nil {
			got, _ := s.eval(ctx, `document.getElementById('out').textContent`)
			t.Fatalf("%s frame: parent never saw %q (out = %s): %v", kind, want, got, err)
		}
	}
}

// TestIntegration_MetadataGuardCoversCrossProcessFrames pins the guarantee the
// floor makes: with site isolation on, a hostile cross-site iframe still can't
// reach a blocked endpoint — neither by fetch() from inside the frame nor by
// the frame's own document being one. The guard is armed on the frame before
// it starts running.
func TestIntegration_MetadataGuardCoversCrossProcessFrames(t *testing.T) {
	orig := blockedURLPatterns
	blockedURLPatterns = append(append([]string(nil), orig...), "*://localhost:*/blocked/*")
	defer func() { blockedURLPatterns = orig }()

	s := newIsolatedSession(t)
	parent, blockedHits := newGuardedFramesFixture(t)
	ctx := context.Background()

	if _, err := s.navigate(ctx, parent+"guard", "load"); err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if err := s.wait(ctx, "", "fetch:rejected,fulfilled", false, 10*time.Second); err != nil {
		got, _ := s.eval(ctx, `document.getElementById('out').textContent`)
		t.Fatalf("the frame's fetches did not settle as (blocked, ok); out = %s: %v", got, err)
	}
	if !hasIframeTarget(t, s) {
		t.Fatal("precondition: the frames should be cross-process with site isolation on")
	}
	// Give the blocked-document frame time to (not) load.
	time.Sleep(time.Second)
	if n := blockedHits(); n != 0 {
		t.Errorf("%d request(s) reached a blocked path from a cross-process frame", n)
	}
	got, err := s.eval(ctx, `document.getElementById('out').textContent`)
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if strings.Contains(got, "blocked-page-loaded") {
		t.Errorf("a frame whose document is a blocked URL ran anyway: %s", got)
	}
}
