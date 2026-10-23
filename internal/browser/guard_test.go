package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// fakeDevTools is a scriptable stand-in for Chrome's browser DevTools endpoint,
// so the guard's protocol handling — ordering, error paths, dropped
// connections, junk input — is tested without a browser.
type fakeDevTools struct {
	t   *testing.T
	srv *httptest.Server

	mu    syncutil.Mutex
	conn  net.Conn
	calls []cdpMsg
	// handle returns false to take over replying itself; the default replies {}.
	handle func(f *fakeDevTools, m cdpMsg) bool
	// onBrowserAttach runs when the browser-level auto-attach arrives, before
	// the reply — mirroring Chrome, which announces existing targets first.
	onBrowserAttach func(f *fakeDevTools)
	ready           chan struct{}
}

func newFakeDevTools(t *testing.T) (*fakeDevTools, string) {
	t.Helper()
	f := &fakeDevTools{t: t, ready: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = conn
		f.mu.Unlock()
		close(f.ready)
		for {
			b, err := wsutil.ReadClientText(conn)
			if err != nil {
				return
			}
			var m cdpMsg
			if json.Unmarshal(b, &m) != nil || m.ID == 0 {
				continue
			}
			f.mu.Lock()
			f.calls = append(f.calls, m)
			handle, onAttach := f.handle, f.onBrowserAttach
			f.mu.Unlock()
			if m.Method == "Target.setAutoAttach" && m.SessionID == "" && onAttach != nil {
				onAttach(f)
			}
			if handle != nil && !handle(f, m) {
				continue
			}
			f.reply(m, map[string]any{})
		}
	}))
	t.Cleanup(f.srv.Close)

	dir := t.TempDir()
	port := strconv.Itoa(f.srv.Listener.Addr().(*net.TCPAddr).Port)
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(port+"\n/devtools/browser/fake\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, dir
}

func (f *fakeDevTools) write(v any) {
	b, _ := json.Marshal(v)
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn != nil {
		_ = wsutil.WriteServerText(conn, b)
	}
}
func (f *fakeDevTools) reply(m cdpMsg, result any) {
	r, _ := json.Marshal(result)
	f.write(map[string]any{"id": m.ID, "sessionId": m.SessionID, "result": json.RawMessage(r)})
}
func (f *fakeDevTools) replyErr(m cdpMsg, msg string) {
	f.write(map[string]any{"id": m.ID, "sessionId": m.SessionID, "error": map[string]any{"message": msg}})
}
func (f *fakeDevTools) event(method, session string, params any) {
	f.write(map[string]any{"method": method, "sessionId": session, "params": params})
}
func (f *fakeDevTools) attach(session, targetID, typ string, waiting bool) {
	f.event("Target.attachedToTarget", "", map[string]any{
		"sessionId": session, "waitingForDebugger": waiting,
		"targetInfo": map[string]any{"targetId": targetID, "type": typ},
	})
}
func (f *fakeDevTools) paused(session, reqID, url string) {
	f.event("Fetch.requestPaused", session, map[string]any{"requestId": reqID, "request": map[string]any{"url": url}})
}

// seq returns the methods received for a session, in order.
func (f *fakeDevTools) seq(session string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.calls {
		if m.SessionID == session {
			out = append(out, m.Method)
		}
	}
	return out
}
func (f *fakeDevTools) find(method string) (cdpMsg, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.calls {
		if m.Method == method {
			return m, true
		}
	}
	return cdpMsg{}, false
}
func (f *fakeDevTools) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.calls {
		if m.Method == method {
			n++
		}
	}
	return n
}
func (f *fakeDevTools) await(cond func() bool, what string) {
	f.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type blockRec struct{ target, kind, url string }

func startTestGuard(t *testing.T, dir string) (*guard, *[]blockRec, *syncutil.Mutex) {
	t.Helper()
	var mu syncutil.Mutex
	var blocks []blockRec
	pol := testPolicy(map[string][]string{"example.com": {"93.184.216.34"}})
	g, err := startGuard(context.Background(), dir, pol, func(tid, kind, url string, err error) {
		mu.Lock()
		blocks = append(blocks, blockRec{tid, kind, url})
		mu.Unlock()
	}, nil)
	if err != nil {
		t.Fatalf("startGuard: %v", err)
	}
	t.Cleanup(g.close)
	return g, &blocks, &mu
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func TestGuardGuardsExistingTargetsBeforeStartReturns(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.onBrowserAttach = func(f *fakeDevTools) { f.attach("S1", "T1", "page", false) }
	startTestGuard(t, dir)

	// startGuard returned, so S1 must already be fully guarded.
	seq := f.seq("S1")
	if indexOf(seq, "Fetch.enable") < 0 || indexOf(seq, "Target.setAutoAttach") < 0 {
		t.Errorf("existing page not guarded by the time startGuard returned: %v", seq)
	}
	m, _ := f.find("Target.setAutoAttach")
	if !strings.Contains(string(m.Params), `"waitForDebuggerOnStart":true`) || !strings.Contains(string(m.Params), `"flatten":true`) {
		t.Errorf("browser-level auto-attach must wait for the debugger and flatten: %s", m.Params)
	}
}

func TestGuardInstallsInterceptionBeforeResumingANewTarget(t *testing.T) {
	f, dir := newFakeDevTools(t)
	startTestGuard(t, dir)
	f.attach("S2", "T2", "iframe", true)
	f.await(func() bool { return indexOf(f.seq("S2"), "Runtime.runIfWaitingForDebugger") >= 0 }, "resume")

	seq := f.seq("S2")
	if indexOf(seq, "Fetch.enable") < 0 || indexOf(seq, "Fetch.enable") > indexOf(seq, "Runtime.runIfWaitingForDebugger") {
		t.Errorf("a paused target must be guarded BEFORE it is resumed: %v", seq)
	}
	if indexOf(seq, "Target.setAutoAttach") < 0 {
		t.Errorf("children of the target must also attach to the guard: %v", seq)
	}
}

func TestGuardRefusesAndAllowsRequests(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.onBrowserAttach = func(f *fakeDevTools) { f.attach("S1", "T1", "page", false) }
	_, blocks, mu := startTestGuard(t, dir)

	f.paused("S1", "r-bad", "http://169.254.169.254/latest/meta-data/")
	f.paused("S1", "r-ok", "https://example.com/x")
	f.await(func() bool { return f.count("Fetch.failRequest") == 1 && f.count("Fetch.continueRequest") == 1 }, "verdicts")

	fail, _ := f.find("Fetch.failRequest")
	if !strings.Contains(string(fail.Params), `"requestId":"r-bad"`) || !strings.Contains(string(fail.Params), "BlockedByClient") {
		t.Errorf("failRequest = %s", fail.Params)
	}
	cont, _ := f.find("Fetch.continueRequest")
	if !strings.Contains(string(cont.Params), `"requestId":"r-ok"`) {
		t.Errorf("continueRequest = %s", cont.Params)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*blocks) != 1 || (*blocks)[0] != (blockRec{"T1", "page", "http://169.254.169.254/latest/meta-data/"}) {
		t.Errorf("onBlock = %+v", *blocks)
	}
}

func TestGuardClosesATargetItCannotGuard(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.handle = func(f *fakeDevTools, m cdpMsg) bool {
		if m.Method == "Fetch.enable" {
			f.replyErr(m, "Fetch domain unavailable")
			return false
		}
		return true
	}
	startTestGuard(t, dir)
	f.attach("S3", "T3", "page", true)
	f.await(func() bool { return f.count("Target.closeTarget") == 1 }, "closeTarget")

	if indexOf(f.seq("S3"), "Runtime.runIfWaitingForDebugger") >= 0 {
		t.Error("a target that could not be guarded must never be resumed")
	}
	m, _ := f.find("Target.closeTarget")
	if !strings.Contains(string(m.Params), `"targetId":"T3"`) {
		t.Errorf("closeTarget = %s", m.Params)
	}
}

func TestGuardOnlyResumesTargetTypesItDoesNotIntercept(t *testing.T) {
	f, dir := newFakeDevTools(t)
	startTestGuard(t, dir)
	f.attach("S4", "T4", "other", true)
	f.await(func() bool { return indexOf(f.seq("S4"), "Runtime.runIfWaitingForDebugger") >= 0 }, "resume")
	if seq := f.seq("S4"); indexOf(seq, "Fetch.enable") >= 0 {
		t.Errorf("unguarded types must not get interception: %v", seq)
	}
}

func TestGuardToleratesJunkAndUnknownEvents(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.onBrowserAttach = func(f *fakeDevTools) { f.attach("S1", "T1", "page", false) }
	g, _, _ := startTestGuard(t, dir)

	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	for _, junk := range []string{"not json", "{", `{"method":"Page.frameNavigated","params":{}}`, `{"id":99999,"result":{}}`, `{"method":"Target.attachedToTarget","params":"oops"}`} {
		_ = wsutil.WriteServerText(conn, []byte(junk))
	}
	f.paused("S1", "after-junk", "http://10.0.0.1/")
	f.await(func() bool { return f.count("Fetch.failRequest") == 1 }, "verdict after junk")
	if !g.alive() {
		t.Error("junk input must not kill the guard")
	}
}

func TestGuardForgetsDetachedSessions(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.onBrowserAttach = func(f *fakeDevTools) { f.attach("S1", "T1", "page", false) }
	g, _, _ := startTestGuard(t, dir)
	f.event("Target.detachedFromTarget", "", map[string]any{"sessionId": "S1"})
	f.await(func() bool {
		g.targetsMu.Lock()
		defer g.targetsMu.Unlock()
		_, ok := g.targets["S1"]
		return !ok
	}, "session cleanup")
}

func TestGuardAnswerRetriesRealErrorsOnceButNotVanishedRequests(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.onBrowserAttach = func(f *fakeDevTools) { f.attach("S1", "T1", "page", false) }
	f.handle = func(f *fakeDevTools, m cdpMsg) bool {
		if m.Method == "Fetch.failRequest" {
			if strings.Contains(string(m.Params), "gone") {
				f.replyErr(m, "Invalid InterceptionId.")
			} else {
				f.replyErr(m, "internal error")
			}
			return false
		}
		return true
	}
	g, _, _ := startTestGuard(t, dir)

	f.paused("S1", "gone", "http://10.0.0.1/")
	f.await(func() bool { return f.count("Fetch.failRequest") == 1 }, "first verdict")
	time.Sleep(100 * time.Millisecond)
	if n := f.count("Fetch.failRequest"); n != 1 {
		t.Errorf("a request that vanished must not be retried (calls=%d)", n)
	}

	f.paused("S1", "flaky", "http://10.0.0.1/")
	f.await(func() bool { return f.count("Fetch.failRequest") == 3 }, "one retry")
	time.Sleep(100 * time.Millisecond)
	if n := f.count("Fetch.failRequest"); n != 3 {
		t.Errorf("a real error is retried exactly once (calls=%d, want 3 total)", n)
	}
	if !g.alive() {
		t.Error("verdict errors must not kill the guard")
	}
}

func TestGuardDropFailsInFlightCallsAndMarksItDead(t *testing.T) {
	f, dir := newFakeDevTools(t)
	f.handle = func(f *fakeDevTools, m cdpMsg) bool {
		if m.Method == "Test.hang" {
			return false // never reply
		}
		return true
	}
	g, _, _ := startTestGuard(t, dir)

	done := make(chan error, 1)
	go func() {
		_, err := g.call(context.Background(), "", "Test.hang", map[string]any{})
		done <- err
	}()
	f.await(func() bool { return f.count("Test.hang") == 1 }, "hung call to arrive")

	f.mu.Lock()
	_ = f.conn.Close()
	f.mu.Unlock()

	select {
	case err := <-done:
		if err == nil {
			t.Error("a call in flight when the connection drops must fail")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call hung after the connection dropped")
	}
	f.await(func() bool { return !g.alive() }, "guard to notice the drop")
	if _, err := g.call(context.Background(), "", "Anything", nil); err == nil {
		t.Error("calls on a dead guard must fail, not block")
	}
}

func TestStartGuardFailsClosed(t *testing.T) {
	t.Run("no endpoint published", func(t *testing.T) {
		old := devToolsPortWait
		devToolsPortWait = 200 * time.Millisecond
		defer func() { devToolsPortWait = old }()
		if _, err := startGuard(context.Background(), t.TempDir(), newNetPolicy(), nil, nil); err == nil {
			t.Error("must fail when Chrome never publishes its DevTools port")
		}
	})
	t.Run("auto-attach refused", func(t *testing.T) {
		f, dir := newFakeDevTools(t)
		f.handle = func(f *fakeDevTools, m cdpMsg) bool {
			if m.Method == "Target.setAutoAttach" {
				f.replyErr(m, "not allowed")
				return false
			}
			return true
		}
		if _, err := startGuard(context.Background(), dir, newNetPolicy(), nil, nil); err == nil {
			t.Error("must fail when browser-level auto-attach is refused")
		}
	})
	t.Run("endpoint unreachable", func(t *testing.T) {
		dir := t.TempDir()
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
		_ = l.Close() // nothing listens here now
		_ = os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(port+"\n/devtools/browser/x\n"), 0o600)
		if _, err := startGuard(context.Background(), dir, newNetPolicy(), nil, nil); err == nil {
			t.Error("must fail when the endpoint cannot be reached")
		}
	})
}

func TestBenignGone(t *testing.T) {
	for _, m := range []string{"Invalid InterceptionId.", "No session with given id", "Target closed", "No target with given id found"} {
		if !benignGone(errors.New(m)) {
			t.Errorf("%q should be benign", m)
		}
	}
	if benignGone(errors.New("internal error")) {
		t.Error("an internal error is not benign")
	}
}
