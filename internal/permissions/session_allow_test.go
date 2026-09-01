package permissions

import (
	"os"
	"testing"
)

// TestAddSessionAllow_NotPersistedToDisk is the core guarantee of the
// "[S] allow for this session" approval answer: the grant must never touch
// permissions.local.json. AddAllow (the "[A] always" path) creates that
// file; AddSessionAllow must not.
func TestAddSessionAllow_NotPersistedToDisk(t *testing.T) {
	cwd := t.TempDir()
	p := LoadEmpty(cwd)
	if err := p.AddSessionAllow("Bash(go *)"); err != nil {
		t.Fatalf("AddSessionAllow: %v", err)
	}
	if _, err := os.Stat(p.LocalPath()); !os.IsNotExist(err) {
		t.Errorf("permissions.local.json exists after AddSessionAllow (want: never created), stat err = %v", err)
	}
}

// TestAddSessionAllow_GrantsMatchingCalls confirms the session rule
// actually gates Evaluate, the same as a persisted allow rule would.
func TestAddSessionAllow_GrantsMatchingCalls(t *testing.T) {
	cwd := t.TempDir()
	p := LoadEmpty(cwd)
	if got := p.Evaluate("run_bash", `{"command":"go test ./..."}`); got != Default {
		t.Fatalf("before grant = %v, want Default", got)
	}
	if err := p.AddSessionAllow("Bash(go *)"); err != nil {
		t.Fatalf("AddSessionAllow: %v", err)
	}
	if got := p.Evaluate("run_bash", `{"command":"go test ./..."}`); got != Allow {
		t.Errorf("after grant = %v, want Allow", got)
	}
	// A non-matching command is unaffected.
	if got := p.Evaluate("run_bash", `{"command":"curl http://evil"}`); got != Default {
		t.Errorf("unrelated command = %v, want Default", got)
	}
}

// TestAddSessionAllow_DenyStillWins: a session grant is still just an
// Allow-tier rule — an explicit Deny (persisted or otherwise) outranks it,
// same as it outranks a persisted allow.
func TestAddSessionAllow_DenyStillWins(t *testing.T) {
	cwd := t.TempDir()
	p := newPerms(t, cwd, []string{"Bash(go *)"}, nil, nil)
	if err := p.AddSessionAllow("Bash(go *)"); err != nil {
		t.Fatalf("AddSessionAllow: %v", err)
	}
	if got := p.Evaluate("run_bash", `{"command":"go test ./..."}`); got != Deny {
		t.Errorf("deny + session-allow on the same pattern = %v, want Deny", got)
	}
}

// TestAddSessionAllow_SurvivesReload: Reload() re-reads permissions.json /
// permissions.local.json from disk and swaps in the fresh deny/allow/ask
// lists. A session grant lives outside that swap (Permissions.sessionAllow
// is untouched by Reload), so editing the files in vim and reloading must
// not silently revoke an in-session grant the user is actively relying on.
func TestAddSessionAllow_SurvivesReload(t *testing.T) {
	cwd := t.TempDir()
	p := LoadEmpty(cwd)
	if err := p.AddSessionAllow("Bash(go *)"); err != nil {
		t.Fatalf("AddSessionAllow: %v", err)
	}
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := p.Evaluate("run_bash", `{"command":"go test ./..."}`); got != Allow {
		t.Errorf("after Reload = %v, want Allow (session grant should survive)", got)
	}
}

// TestAddSessionAllow_Idempotent mirrors AddAllow's dedup behavior: adding
// the same pattern twice, or a pattern already covered by a persisted
// allow rule, must not grow the in-memory list unboundedly or change the
// verdict.
func TestAddSessionAllow_Idempotent(t *testing.T) {
	cwd := t.TempDir()
	p := newPerms(t, cwd, nil, []string{"Git(status *)"}, nil)
	if err := p.AddSessionAllow("Git(status *)"); err != nil {
		t.Fatalf("AddSessionAllow (already-persisted pattern): %v", err)
	}
	if len(p.sessionAllow) != 0 {
		t.Errorf("sessionAllow = %d entries, want 0 (pattern already covered by a persisted allow rule)", len(p.sessionAllow))
	}
	if err := p.AddSessionAllow("Bash(go *)"); err != nil {
		t.Fatalf("AddSessionAllow: %v", err)
	}
	if err := p.AddSessionAllow("Bash(go *)"); err != nil {
		t.Fatalf("AddSessionAllow (duplicate): %v", err)
	}
	if len(p.sessionAllow) != 1 {
		t.Errorf("sessionAllow = %d entries, want 1 (duplicate should be a no-op)", len(p.sessionAllow))
	}
}

// TestAddSessionAllow_MultiTargetCombinesWithPersistedAllow: a batch call
// (read_many_files) where one path matches a persisted allow rule and the
// other matches only a session grant must still resolve to Allow overall —
// the ratcheted "all descriptors must match SOME allow rule" semantics
// need to check both rule sources per descriptor, not evaluate them as two
// independent all-or-nothing passes.
func TestAddSessionAllow_MultiTargetCombinesWithPersistedAllow(t *testing.T) {
	cwd := t.TempDir()
	p := newPerms(t, cwd, nil, []string{"Read(internal/**)"}, nil)
	if err := p.AddSessionAllow("Read(cmd/**)"); err != nil {
		t.Fatalf("AddSessionAllow: %v", err)
	}
	args := `{"paths":["internal/a.go","cmd/b.go"]}`
	if got := p.Evaluate("read_many_files", args); got != Allow {
		t.Errorf("mixed persisted+session allow batch = %v, want Allow", got)
	}
	// A batch with one path outside both allow sources must NOT auto-allow.
	mixed := `{"paths":["internal/a.go","cmd/b.go","docs/c.md"]}`
	if got := p.Evaluate("read_many_files", mixed); got != Default {
		t.Errorf("batch with an ungoverned path = %v, want Default", got)
	}
}
