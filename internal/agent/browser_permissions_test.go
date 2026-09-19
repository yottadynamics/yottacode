package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// browserPermsCfg builds a loop whose registry holds prompting browser_*
// stand-ins, so these tests exercise the real derive/persist/evaluate path in
// loop.go without a Chrome process.
func browserPermsCfg(t *testing.T, calls ...adapter.ToolCall) (LoopConfig, string, *scriptedStreamer) {
	t.Helper()
	perms, cwd := permsForTest(t, nil, nil, nil)
	var turns [][]adapter.StreamEvent
	for _, c := range calls {
		turns = append(turns, []adapter.StreamEvent{sseDone("", c)})
	}
	turns = append(turns, []adapter.StreamEvent{sseToken("ok"), sseDone("ok")})
	streamer := &scriptedStreamer{turns: turns}
	reg := NewRegistry()
	for _, name := range []string{"browser_inspect", "browser_click", "browser_navigate", "browser_upload"} {
		reg.Register(&mockTool{name: name, requiresApproval: true, output: "x"})
	}
	return LoopConfig{
		Adapter: streamer, Registry: reg, Permissions: perms,
		Cwd: NewCwdRef(cwd), MaxIterations: 8,
	}, cwd, streamer
}

func localPermsFile(t *testing.T, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cwd, ".yottacode", "permissions.local.json"))
	if err != nil {
		t.Fatalf("read permissions.local.json: %v", err)
	}
	return string(b)
}

// [S] on a browser call grants that verb for the rest of the session — a later
// call with a different selector doesn't prompt — without touching disk, and
// without granting any other browser verb.
func TestLoop_BrowserAllowSession_GrantsVerbNotOthers(t *testing.T) {
	cfg, cwd, _ := browserPermsCfg(t,
		adapter.ToolCall{ID: "c1", Name: "browser_inspect", ArgsJSON: `{"selector":"main li:nth-of-type(1)"}`},
		adapter.ToolCall{ID: "c2", Name: "browser_inspect", ArgsJSON: `{"selector":"#a-completely-different-selector"}`},
		adapter.ToolCall{ID: "c3", Name: "browser_click", ArgsJSON: `{"selector":"#go"}`},
	)
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	var prompted []string
	events, err := runTurnSync(t, context.Background(), cfg, &hist, func(n ApprovalNeeded) Decision {
		prompted = append(prompted, n.ToolName)
		if n.ToolName == "browser_inspect" {
			return AllowSession
		}
		return AllowOnce
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if got := strings.Join(prompted, ","); got != "browser_inspect,browser_click" {
		t.Errorf("prompts = %q, want the first inspect and the click only (second inspect covered by the session grant)", got)
	}
	var sawSessionAuto bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "permissions" && a.RuleSource == "session" {
			sawSessionAuto = true
		}
	}
	if !sawSessionAuto {
		t.Error("expected the second inspect to be auto-approved from the session grant")
	}
	if _, err := os.Stat(filepath.Join(cwd, ".yottacode", "permissions.local.json")); !os.IsNotExist(err) {
		t.Errorf("[S] must not write permissions.local.json (stat err = %v)", err)
	}
}

// [A] saves Browser(<verb>) to permissions.local.json.
func TestLoop_BrowserAllowAlways_SavesRule(t *testing.T) {
	cfg, cwd, _ := browserPermsCfg(t,
		adapter.ToolCall{ID: "c1", Name: "browser_inspect", ArgsJSON: `{"selector":"main"}`},
	)
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}
	if _, err := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision { return AllowAlways }); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if got := localPermsFile(t, cwd); !strings.Contains(got, `"Browser(inspect)"`) {
		t.Errorf("permissions.local.json = %s, want it to contain Browser(inspect) under allow", got)
	}
}

// [D] saves a block rule and refuses this call; the next matching call is
// refused by the rule without prompting again.
func TestLoop_BrowserDenyAlways_BlocksThisAndLaterCalls(t *testing.T) {
	cfg, cwd, _ := browserPermsCfg(t,
		adapter.ToolCall{ID: "c1", Name: "browser_click", ArgsJSON: `{"selector":"#buy"}`},
		adapter.ToolCall{ID: "c2", Name: "browser_click", ArgsJSON: `{"selector":"#other"}`},
	)
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}
	var prompts int
	if _, err := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		prompts++
		return DenyAlways
	}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if prompts != 1 {
		t.Errorf("prompts = %d, want 1 (the later click is blocked by the saved rule, not re-asked)", prompts)
	}
	got := localPermsFile(t, cwd)
	if !strings.Contains(got, `"deny"`) || !strings.Contains(got, `"Browser(click)"`) {
		t.Errorf("permissions.local.json = %s, want Browser(click) under deny", got)
	}
}

// browser_navigate grants are per exact site: allowing one site never quietly
// allows another.
func TestLoop_BrowserAllowSession_NavigateIsPerSite(t *testing.T) {
	cfg, _, _ := browserPermsCfg(t,
		adapter.ToolCall{ID: "c1", Name: "browser_navigate", ArgsJSON: `{"url":"https://streeteasy.com/for-rent/long-island-city"}`},
		adapter.ToolCall{ID: "c2", Name: "browser_navigate", ArgsJSON: `{"url":"https://streeteasy.com/for-sale/queens"}`},
		adapter.ToolCall{ID: "c3", Name: "browser_navigate", ArgsJSON: `{"url":"https://evil.example/"}`},
	)
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}
	var prompted []string
	if _, err := runTurnSync(t, context.Background(), cfg, &hist, func(n ApprovalNeeded) Decision {
		prompted = append(prompted, n.ArgsJSON)
		return AllowSession
	}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(prompted) != 2 || !strings.Contains(prompted[0], "streeteasy.com/for-rent") || !strings.Contains(prompted[1], "evil.example") {
		t.Errorf("prompted for %q; want the first streeteasy.com call and the evil.example call only", prompted)
	}
}

// Uploads move local file bytes off the machine, so no allow rule is ever
// derived for them: [S]/[A] are no-ops and the next upload prompts again.
func TestLoop_BrowserUpload_NeverGrantedByAllowSession(t *testing.T) {
	cfg, cwd, _ := browserPermsCfg(t,
		adapter.ToolCall{ID: "c1", Name: "browser_upload", ArgsJSON: `{"selector":"#f","paths":["a.txt"]}`},
		adapter.ToolCall{ID: "c2", Name: "browser_upload", ArgsJSON: `{"selector":"#f","paths":["b.txt"]}`},
	)
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}
	var prompts int
	if _, err := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		prompts++
		return AllowSession
	}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if prompts != 2 {
		t.Errorf("prompts = %d, want 2 (every upload is its own decision)", prompts)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".yottacode", "permissions.local.json")); !os.IsNotExist(err) {
		t.Errorf("nothing should have been saved (stat err = %v)", err)
	}
}
