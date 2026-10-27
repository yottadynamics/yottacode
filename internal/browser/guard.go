package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// guard enforces netPolicy on EVERY target of the browser — pages, out-of-
// process iframes, and workers — over its own CDP connection.
//
// Per-page Fetch interception through chromedp cannot see a cross-site iframe
// that Chrome isolates in another process: that frame is a separate target
// with its own session, chromedp never talks to it, and attaching afterwards
// loses the race against the frame's first script. So the guard asks Chrome to
// auto-attach to every target *paused* (waitForDebuggerOnStart), installs
// Fetch interception on it, and only then lets it run. Because the target is
// paused until the guard resumes it, there is no window in which a request can
// slip past.
//
// If the guard cannot start, the session does not launch (fail closed). If its
// connection later drops, the session reports itself dead and relaunches.

type guard struct {
	conn   net.Conn
	policy *netPolicy

	// onBlock is told about every refused request so the agent can see why.
	onBlock func(targetID, targetType, url string, err error)
	// onDeath runs when the connection is lost without us closing it. Policy
	// is no longer enforced, so the owner must stop the browser.
	onDeath func()
	closing atomic.Bool

	wmu    syncutil.Mutex // serializes websocket writes
	nextID atomic.Int64

	pmu     syncutil.Mutex
	pending map[int64]chan cdpReply

	targetsMu syncutil.Mutex
	targets   map[string]guardTarget // sessionID -> target

	dead chan struct{}
	once sync.Once

	// attaching counts attachSession runs in flight, so startGuard can wait for
	// the targets that already existed to be guarded before returning.
	attaching sync.WaitGroup
}

// guardTarget is one attached target. owner is the id of the page it belongs
// to: the page itself for a page, the parent's owner for an iframe or worker
// attached beneath it, and "" for a target with no page above it (a shared or
// service worker attached at the browser level).
type guardTarget struct{ id, kind, owner string }

type cdpMsg struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type cdpReply struct {
	result json.RawMessage
	err    error
}

// guardTargetTypes are the target types that get request interception. Any
// other type is simply resumed.
var guardTargetTypes = map[string]bool{
	"page": true, "iframe": true, "worker": true, "service_worker": true, "shared_worker": true,
}

// devToolsPortWait is how long to wait for Chrome to publish its endpoint. A
// var so tests can shorten it.
var devToolsPortWait = 10 * time.Second

// browserWSURL reads the DevTools endpoint Chrome publishes in the profile dir.
func browserWSURL(profile string) (string, error) {
	deadline := time.Now().Add(devToolsPortWait)
	for {
		b, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) >= 2 && lines[0] != "" && lines[1] != "" {
				return "ws://127.0.0.1:" + strings.TrimSpace(lines[0]) + strings.TrimSpace(lines[1]), nil
			}
		}
		if time.Now().After(deadline) {
			return "", errors.New("DevToolsActivePort not published")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func startGuard(ctx context.Context, profile string, policy *netPolicy, onBlock func(targetID, targetType, url string, err error), onDeath func()) (*guard, error) {
	url, err := browserWSURL(profile)
	if err != nil {
		return nil, err
	}
	conn, _, _, err := ws.Dial(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("guard connect: %w", err)
	}
	g := &guard{
		conn: conn, policy: policy, onBlock: onBlock, onDeath: onDeath,
		pending: map[int64]chan cdpReply{},
		targets: map[string]guardTarget{},
		dead:    make(chan struct{}),
	}
	go g.readLoop()
	// Browser-level auto-attach covers every top-level target, including the
	// ones that already exist; each attached session then auto-attaches its own
	// children (iframes, workers) in attachSession.
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := g.call(cctx, "", "Target.setAutoAttach", map[string]any{
		"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true,
	}); err != nil {
		g.close()
		return nil, fmt.Errorf("guard auto-attach: %w", err)
	}
	// attachedToTarget for existing targets precedes the reply on the wire.
	// Use a second browser-level command as a protocol barrier; the reply
	// guarantees the read loop has received the initial attachment events.
	if _, err := g.call(cctx, "", "Target.getTargets", nil); err != nil {
		g.close()
		return nil, fmt.Errorf("guard target barrier: %w", err)
	}
	g.attaching.Wait()
	return g, nil
}

func (g *guard) alive() bool {
	select {
	case <-g.dead:
		return false
	default:
		return true
	}
}

func (g *guard) close() {
	g.once.Do(func() {
		close(g.dead)
		_ = g.conn.Close()
		g.pmu.Lock()
		for id, ch := range g.pending {
			ch <- cdpReply{err: errors.New("guard closed")}
			delete(g.pending, id)
		}
		g.pmu.Unlock()
	})
}

func (g *guard) send(m cdpMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	g.wmu.Lock()
	defer g.wmu.Unlock()
	return wsutil.WriteClientText(g.conn, b)
}

func (g *guard) call(ctx context.Context, session, method string, params any) (json.RawMessage, error) {
	id := g.nextID.Add(1)
	ch := make(chan cdpReply, 1)
	g.pmu.Lock()
	g.pending[id] = ch
	g.pmu.Unlock()
	raw, _ := json.Marshal(params)
	if err := g.send(cdpMsg{ID: id, Method: method, Params: raw, SessionID: session}); err != nil {
		g.pmu.Lock()
		delete(g.pending, id)
		g.pmu.Unlock()
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		g.pmu.Lock()
		delete(g.pending, id)
		g.pmu.Unlock()
		return nil, ctx.Err()
	case <-g.dead:
		return nil, errors.New("guard closed")
	}
}

// shutdown closes the guard on purpose; unlike a lost connection it does not
// report a death.
func (g *guard) shutdown() {
	g.closing.Store(true)
	g.close()
}

func (g *guard) readLoop() {
	defer func() {
		g.close()
		if !g.closing.Load() && g.onDeath != nil {
			g.onDeath()
		}
	}()
	for {
		b, err := wsutil.ReadServerText(g.conn)
		if err != nil {
			return
		}
		var m cdpMsg
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		switch {
		case m.ID != 0:
			g.pmu.Lock()
			ch := g.pending[m.ID]
			delete(g.pending, m.ID)
			g.pmu.Unlock()
			if ch != nil {
				r := cdpReply{result: m.Result}
				if m.Error != nil {
					r.err = errors.New(m.Error.Message)
				}
				ch <- r
			}
		case m.Method == "Target.attachedToTarget":
			sessionID, params := m.SessionID, append(json.RawMessage(nil), m.Params...)
			g.attaching.Add(1)
			go func() {
				defer g.attaching.Done()
				g.attachSession(sessionID, params)
			}()
		case m.Method == "Target.detachedFromTarget":
			var ev struct {
				SessionID string `json:"sessionId"`
			}
			if json.Unmarshal(m.Params, &ev) == nil {
				g.targetsMu.Lock()
				gone := g.targets[ev.SessionID]
				delete(g.targets, ev.SessionID)
				g.targetsMu.Unlock()
				if gone.id != "" {
					g.policy.forgetTarget(gone.id)
				}
			}
		case m.Method == "Fetch.requestPaused":
			// One goroutine per paused request: the read loop must never block
			// on a reply it is itself responsible for reading. The count is
			// bounded by Chrome's own in-flight request limits.
			go g.onPaused(m.SessionID, m.Params)
		}
	}
}

// attachSession installs interception on a freshly attached (paused) target,
// then resumes it. It always resumes, even on error, so a failure here degrades
// to a refused request or a closed session rather than a frozen page.
// parentSession is the session whose auto-attach produced this target ("" for
// one attached at the browser level).
func (g *guard) attachSession(parentSession string, params json.RawMessage) {
	var ev struct {
		SessionID  string `json:"sessionId"`
		TargetInfo struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfo"`
		WaitingForDebugger bool `json:"waitingForDebugger"`
	}
	if json.Unmarshal(params, &ev) != nil || ev.SessionID == "" {
		return
	}
	g.targetsMu.Lock()
	owner := ""
	if ev.TargetInfo.Type == "page" {
		owner = ev.TargetInfo.TargetID
	} else if parent, ok := g.targets[parentSession]; ok && parentSession != "" {
		owner = parent.owner
	}
	g.targets[ev.SessionID] = guardTarget{ev.TargetInfo.TargetID, ev.TargetInfo.Type, owner}
	g.targetsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if guardTargetTypes[ev.TargetInfo.Type] {
		// Children (iframes, workers) of this target attach paused to us too.
		_, e1 := g.call(ctx, ev.SessionID, "Target.setAutoAttach", map[string]any{
			"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true,
		})
		_, e2 := g.call(ctx, ev.SessionID, "Fetch.enable", map[string]any{})
		if e1 != nil || e2 != nil {
			// A target we could not guard must not run unguarded.
			_, _ = g.call(ctx, "", "Target.closeTarget", map[string]any{"targetId": ev.TargetInfo.TargetID})
			return
		}
	}
	if ev.WaitingForDebugger {
		_, _ = g.call(ctx, ev.SessionID, "Runtime.runIfWaitingForDebugger", map[string]any{})
	}
}

func (g *guard) onPaused(session string, params json.RawMessage) {
	var ev struct {
		RequestID    string `json:"requestId"`
		FrameID      string `json:"frameId"`
		ResourceType string `json:"resourceType"`
		Request      struct {
			URL string `json:"url"`
		} `json:"request"`
	}
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	g.targetsMu.Lock()
	tgt := g.targets[session]
	g.targetsMu.Unlock()
	// A page target's main frame shares its id with the target, so a Document
	// request for that frame is the page navigating (each redirect hop included).
	rc := reqCtx{
		// The page this request ultimately belongs to: a frame or worker is
		// judged by its owner's location, never by a session-wide flag.
		targetID: tgt.owner,
		topNav:   tgt.kind == "page" && ev.FrameID == tgt.id && ev.ResourceType == "Document",
	}
	if err := g.policy.checkRequest(ev.Request.URL, rc); err != nil {
		if g.onBlock != nil {
			// Attribute the refusal to the owning page's tab when there is one.
			if tgt.owner != "" {
				g.onBlock(tgt.owner, "page", ev.Request.URL, err)
			} else {
				g.onBlock(tgt.id, tgt.kind, ev.Request.URL, err)
			}
		}
		g.answer(ctx, session, "Fetch.failRequest", map[string]any{"requestId": ev.RequestID, "errorReason": "BlockedByClient"})
		return
	}
	g.answer(ctx, session, "Fetch.continueRequest", map[string]any{"requestId": ev.RequestID})
}

// answer sends a Fetch verdict. A request that vanished before we answered (the
// page navigated away, the frame was destroyed) is normal and ignored; any other
// failure is retried once. A request we could not answer stays paused, which
// is the safe outcome for a refusal and merely stalls one request otherwise.
func (g *guard) answer(ctx context.Context, session, method string, params map[string]any) {
	for attempt := 0; attempt < 2; attempt++ {
		_, err := g.call(ctx, session, method, params)
		if err == nil || benignGone(err) || !g.alive() {
			return
		}
	}
}

// benignGone reports errors that only mean the thing we were answering is gone.
func benignGone(err error) bool {
	m := strings.ToLower(err.Error())
	for _, s := range []string{"invalid interceptionid", "no session with given id", "session with given id not found", "target closed", "no target with given id"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
