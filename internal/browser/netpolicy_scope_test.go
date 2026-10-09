package browser

import (
	"errors"
	"testing"
)

func scopePolicy() *netPolicy {
	return testPolicy(map[string][]string{
		"public.example": {"93.184.216.34"},
		"evil.example":   {"93.184.216.35"},
	})
}

func nav(t string) reqCtx { return reqCtx{targetID: t, topNav: true} }
func sub(t string) reqCtx { return reqCtx{targetID: t} }

func blocked(err error) bool { return errors.Is(err, ErrBlockedURL) }

// The failure the review found: opening one local dev page must not leave the
// machine reachable from every public page visited afterwards.
func TestLoopbackAccessEndsWhenTheBrowserLeavesLocalhost(t *testing.T) {
	p := scopePolicy()

	p.allowExplicit("http://localhost:3000/") // the agent opens a dev server
	if err := p.checkRequest("http://localhost:3000/", nav("T1")); err != nil {
		t.Fatalf("explicit navigation to the dev server: %v", err)
	}
	if err := p.checkRequest("http://127.0.0.1:8080/api", sub("T1")); err != nil {
		t.Errorf("the dev page may call its own API: %v", err)
	}

	// The agent then visits a public site in the same tab.
	if err := p.checkRequest("https://evil.example/", nav("T1")); err != nil {
		t.Fatalf("public navigation: %v", err)
	}
	for _, u := range []string{"http://127.0.0.1:9222/json", "http://localhost:3000/", "http://[::1]/"} {
		if err := p.checkRequest(u, sub("T1")); !blocked(err) {
			t.Errorf("public page reached %s: %v", u, err)
		}
		if err := p.checkRequest(u, reqCtx{}); !blocked(err) {
			t.Errorf("with the token retired, an unknown requester reached %s: %v", u, err)
		}
	}

	// Coming back is an explicit act again.
	p.allowExplicit("http://localhost:3000/")
	if err := p.checkRequest("http://localhost:3000/", nav("T1")); err != nil {
		t.Errorf("re-navigating explicitly should work: %v", err)
	}
	if err := p.checkRequest("http://127.0.0.1:8080/api", sub("T1")); err != nil {
		t.Errorf("and the page should work again: %v", err)
	}
}

func TestRedirectFromPublicToLocalIsRefused(t *testing.T) {
	p := scopePolicy()
	p.allowExplicit("http://localhost:3000/")
	if err := p.checkRequest("http://localhost:3000/", nav("T1")); err != nil {
		t.Fatal(err)
	}
	// localhost -> (redirect) public -> (redirect) back to localhost.
	if err := p.checkRequest("https://public.example/hop", nav("T1")); err != nil {
		t.Fatal(err)
	}
	if err := p.checkRequest("http://localhost:3000/admin", nav("T1")); !blocked(err) {
		t.Errorf("a redirect chain that passed through a public host must not land on localhost: %v", err)
	}
}

func TestPopupFromAPublicPageCannotNavigateLocal(t *testing.T) {
	p := scopePolicy()
	p.allowExplicit("http://localhost:3000/")
	_ = p.checkRequest("http://localhost:3000/", nav("T1"))
	_ = p.checkRequest("https://evil.example/", nav("T1")) // retires the token

	// The public page opens a popup pointed at a local service.
	if err := p.checkRequest("http://localhost:9222/json", nav("T2")); !blocked(err) {
		t.Errorf("popup navigation to localhost: %v", err)
	}
}

func TestEachPageIsJudgedByItsOwnLocation(t *testing.T) {
	p := scopePolicy()
	p.allowExplicit("http://localhost:3000/")
	_ = p.checkRequest("http://localhost:3000/", nav("DEV"))

	// A second tab goes public. That retires the token, but the dev tab is still
	// a local page and keeps its own access.
	_ = p.checkRequest("https://evil.example/", nav("PUB"))
	if err := p.checkRequest("http://localhost:8080/api", sub("DEV")); err != nil {
		t.Errorf("the dev tab should keep working: %v", err)
	}
	if err := p.checkRequest("http://localhost:8080/api", sub("PUB")); !blocked(err) {
		t.Errorf("the public tab must not: %v", err)
	}
}

func TestPrivateNetworkAccessIsPerHost(t *testing.T) {
	p := scopePolicy()
	p.allowExplicit("http://192.168.1.20:5000/")
	if err := p.checkRequest("http://192.168.1.20:5000/", nav("T1")); err != nil {
		t.Fatal(err)
	}
	if err := p.checkRequest("http://192.168.1.20:8000/api", sub("T1")); err != nil {
		t.Errorf("same host: %v", err)
	}
	if err := p.checkRequest("http://192.168.1.21/", sub("T1")); !blocked(err) {
		t.Errorf("another LAN host: %v", err)
	}
	if err := p.checkRequest("http://localhost:3000/", sub("T1")); !blocked(err) {
		t.Errorf("a LAN page must not reach loopback: %v", err)
	}
}

func TestForgetTargetClearsItsLocation(t *testing.T) {
	p := scopePolicy()
	p.allowExplicit("http://localhost:3000/")
	_ = p.checkRequest("http://localhost:3000/", nav("T1"))
	p.forgetTarget("T1")
	p.mu.Lock()
	_, ok := p.top["T1"]
	p.mu.Unlock()
	if ok {
		t.Error("a closed target must not linger in the policy")
	}
}

func TestMetadataStaysBlockedForEveryRequester(t *testing.T) {
	p := scopePolicy()
	p.allowExplicit("http://localhost:3000/")
	_ = p.checkRequest("http://localhost:3000/", nav("T1"))
	for _, rc := range []reqCtx{nav("T1"), sub("T1"), {}} {
		if err := p.checkRequest("http://169.254.169.254/latest/meta-data/", rc); !blocked(err) {
			t.Errorf("%+v reached metadata: %v", rc, err)
		}
	}
}
