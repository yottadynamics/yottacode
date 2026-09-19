package browser

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// fakeCDP is a proto.Client that records every command and answers `{}`.
type fakeCDP struct {
	mu    syncutil.Mutex
	calls []fakeCall
}

type fakeCall struct {
	sessionID, method string
	params            map[string]any
}

func (f *fakeCDP) Call(_ context.Context, sessionID, method string, params any) ([]byte, error) {
	raw, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{sessionID: sessionID, method: method, params: p})
	f.mu.Unlock()
	return []byte(`{}`), nil
}

func (f *fakeCDP) last() fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeCall{}
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeCDP) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func pausedReq(url string, resource proto.NetworkResourceType, frame proto.PageFrameID) *proto.FetchRequestPaused {
	return &proto.FetchRequestPaused{
		RequestID:    "req-1",
		Request:      &proto.NetworkRequest{URL: url},
		ResourceType: resource,
		FrameID:      frame,
	}
}

func TestAnswerPaused_BlockedRequestsFailAndOrdinaryOnesContinueWhenEverythingIsIntercepted(t *testing.T) {
	c := &fakeCDP{}
	tp := &trackedPage{}

	// Everything intercepted (proxy-auth mode): an ordinary request continues…
	answerPaused(c, tp, pausedReq("https://example.com/", proto.NetworkResourceTypeDocument, "main"), true, "main")
	if got := c.last().method; got != "Fetch.continueRequest" {
		t.Errorf("ordinary request: %s, want Fetch.continueRequest", got)
	}
	if tp.blockedCount() != 0 {
		t.Error("an ordinary request must not count as blocked")
	}

	// …and a blocked one fails, with the right reason.
	answerPaused(c, tp, pausedReq("http://169.254.169.254/latest", proto.NetworkResourceTypeFetch, "main"), true, "main")
	if got := c.last(); got.method != "Fetch.failRequest" || got.params["errorReason"] != "BlockedByClient" {
		t.Errorf("blocked request: %+v", got)
	}
}

func TestAnswerPaused_OnlyAMainFrameDocumentCountsAsABlockedNavigation(t *testing.T) {
	blockedURL := "http://169.254.169.254/x"
	cases := []struct {
		name      string
		resource  proto.NetworkResourceType
		frame     proto.PageFrameID
		mainFrame proto.PageFrameID
		wantCount int
	}{
		{"main-frame document", proto.NetworkResourceTypeDocument, "main", "main", 1},
		{"subresource of the main frame", proto.NetworkResourceTypeFetch, "main", "main", 0},
		{"document in a child frame", proto.NetworkResourceTypeDocument, "child", "main", 0},
		{"cross-process frame (no main frame id)", proto.NetworkResourceTypeDocument, "child", "", 0},
	}
	for _, c := range cases {
		cdp := &fakeCDP{}
		tp := &trackedPage{}
		answerPaused(cdp, tp, pausedReq(blockedURL, c.resource, c.frame), false, c.mainFrame)
		if got := tp.blockedCount(); got != c.wantCount {
			t.Errorf("%s: blockedCount = %d, want %d", c.name, got, c.wantCount)
		}
		if cdp.last().method != "Fetch.failRequest" {
			t.Errorf("%s: a blocked request must always be failed, got %s", c.name, cdp.last().method)
		}
	}
}

func TestAnswerAuth_CredentialsGoToTheProxyOnly(t *testing.T) {
	creds := &proxyCreds{user: "ada", pass: "s3cret"}
	challenge := func(src proto.FetchAuthChallengeSource) *proto.FetchAuthRequired {
		return &proto.FetchAuthRequired{RequestID: "r", AuthChallenge: &proto.FetchAuthChallenge{Source: src}}
	}
	response := func(c *fakeCDP) map[string]any {
		resp, _ := c.last().params["authChallengeResponse"].(map[string]any)
		return resp
	}

	c := &fakeCDP{}
	answerAuth(c, creds, challenge(proto.FetchAuthChallengeSourceProxy))
	if r := response(c); r["response"] != "ProvideCredentials" || r["username"] != "ada" || r["password"] != "s3cret" {
		t.Errorf("proxy challenge: %v", r)
	}

	c = &fakeCDP{}
	answerAuth(c, creds, challenge(proto.FetchAuthChallengeSourceServer))
	if r := response(c); r["response"] != "Default" || r["password"] != nil {
		t.Errorf("a site's own challenge must get the default answer and never the proxy password: %v", r)
	}

	c = &fakeCDP{}
	answerAuth(c, nil, challenge(proto.FetchAuthChallengeSourceProxy))
	if r := response(c); r["response"] != "Default" {
		t.Errorf("no credentials configured: %v", r)
	}
	answerAuth(c, creds, &proto.FetchAuthRequired{RequestID: "r"}) // no challenge object: must not panic
}

func TestGuardPatterns(t *testing.T) {
	all := guardPatterns(true)
	if len(all) != 1 || all[0].URLPattern != "*" {
		t.Errorf("intercept-all patterns = %v", all)
	}
	only := guardPatterns(false)
	if len(only) != len(blockedURLPatterns) {
		t.Fatalf("blocked-only patterns = %d, want %d", len(only), len(blockedURLPatterns))
	}
	for i, p := range only {
		if p.URLPattern != blockedURLPatterns[i] {
			t.Errorf("pattern %d = %q, want %q", i, p.URLPattern, blockedURLPatterns[i])
		}
	}
}

func TestTrackedPage_OOPIFBookkeeping(t *testing.T) {
	tp := &trackedPage{}
	a := oopif{target: "t-a", session: "s-a", url: "https://a.test/"}
	b := oopif{target: "t-b", session: "s-b", url: "https://b.test/"}
	tp.addOOPIF(a)
	tp.addOOPIF(b)
	if got := tp.oopifList(); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("oopifList = %v", got)
	}

	// The same frame re-attaching (new session, new URL) replaces its entry.
	a2 := oopif{target: "t-a", session: "s-a2", url: "https://a.test/next"}
	tp.addOOPIF(a2)
	if got := tp.oopifList(); len(got) != 2 || got[0] != a2 {
		t.Errorf("re-attach should replace in place: %v", got)
	}
	if _, ok := tp.oopifBySession("s-a"); ok {
		t.Error("the old session must no longer resolve")
	}
	if o, ok := tp.oopifBySession("s-a2"); !ok || o.url != "https://a.test/next" {
		t.Errorf("oopifBySession(s-a2) = %v, %t", o, ok)
	}

	// The list is a snapshot: mutating it does not touch the page's.
	got := tp.oopifList()
	got[0].url = "mutated"
	if tp.oopifList()[0].url == "mutated" {
		t.Error("oopifList must return a copy")
	}

	tp.removeOOPIF("s-a2")
	if got := tp.oopifList(); len(got) != 1 || got[0] != b {
		t.Errorf("after removing a frame: %v", got)
	}
	tp.removeOOPIF("no-such-session") // must not panic or remove anything
	if len(tp.oopifList()) != 1 {
		t.Error("removing an unknown session changed the list")
	}
}

func TestCrossProcessRef(t *testing.T) {
	s := &session{}
	tp := &trackedPage{}
	tp.addOOPIF(oopif{target: "t-live", session: "s-live"})
	tp.registerRefs(axRender{Refs: map[string]refTarget{
		"e1": {node: 10},                        // the page itself
		"e2": {node: 20, session: "s-live"},     // a live cross-process frame
		"e3": {node: 30, session: "s-detached"}, // a frame that has since gone
	}, RefCount: 3}, true)

	if _, _, ok := s.crossProcessRef(tp, "#css"); ok {
		t.Error("a CSS selector is not a cross-process ref")
	}
	if _, _, ok := s.crossProcessRef(tp, "@e1"); ok {
		t.Error("a page ref belongs to the ordinary path")
	}
	if _, _, ok := s.crossProcessRef(tp, "@e99"); ok {
		t.Error("an unknown ref falls through to the ordinary path, which reports it stale")
	}

	rt, o, ok := s.crossProcessRef(tp, " @e2 ")
	if !ok || rt.node != 20 || o.session != "s-live" || o.target != "t-live" {
		t.Errorf("live frame ref = %+v, %+v, %t", rt, o, ok)
	}

	// A ref into a detached frame is still claimed (so it never falls through
	// to the page session, where the node id means something else) but with no
	// live frame — which the actions report as stale.
	rt, o, ok = s.crossProcessRef(tp, "@e3")
	if !ok || rt.node != 30 || o.session != "" {
		t.Errorf("detached frame ref = %+v, %+v, %t", rt, o, ok)
	}
}

func TestCrossProcessActionsOnAGoneFrameAreStale(t *testing.T) {
	s := &session{}
	tp := &trackedPage{}
	rt := refTarget{node: 30, session: "s-detached"}
	if err := s.crossProcessClick(context.Background(), tp, "@e3", rt, oopif{}); !errors.Is(err, ErrStaleRef) {
		t.Errorf("click: err = %v, want ErrStaleRef", err)
	}
	if err := s.crossProcessType(context.Background(), tp, "@e3", "x", false, rt, oopif{}); !errors.Is(err, ErrStaleRef) {
		t.Errorf("type: err = %v, want ErrStaleRef", err)
	}
	if err := errFrameGone("@e3"); !errors.Is(err, ErrStaleRef) {
		t.Errorf("errFrameGone: %v", err)
	}
}

func TestElementByRef_CrossProcessRefsAreRefusedByEverythingElse(t *testing.T) {
	pg := &rod.Page{}
	tp := &trackedPage{page: pg}
	tp.registerRefs(axRender{Refs: map[string]refTarget{"e2": {node: 20, session: "s-live"}}, RefCount: 2}, true)
	s := &session{pages: []*trackedPage{tp}}

	// Scroll, wait, screenshot, upload, download and scoped inspect all go
	// through element(): a control in a cross-process frame has no rod element.
	_, err := s.element(context.Background(), pg, "@e2")
	if !errors.Is(err, ErrCrossProcessFrame) {
		t.Fatalf("err = %v, want ErrCrossProcessFrame", err)
	}
}

func TestSessionClient_CarriesItsSessionAndContext(t *testing.T) {
	var _ proto.Client = sessionClient{}
	var _ proto.Contextable = sessionClient{}

	if got := (sessionClient{}).GetContext(); got == nil {
		t.Error("a nil context must default to Background, not nil")
	}
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	if got := (sessionClient{ctx: ctx}).GetContext(); got.Value(key{}) != "v" {
		t.Error("GetContext should return the configured context")
	}
}
