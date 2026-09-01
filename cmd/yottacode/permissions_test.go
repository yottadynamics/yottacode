package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newPermissionsCmdHarness builds a `yottacode permissions ...` command
// tree with the process cwd pinned to an isolated tempdir (seeded with the
// given permissions.json rules) for the duration of the test. `permissions
// test` reads the current directory the same way `yottacode trust add`
// defaults to it — no --cwd flag — so exercising it means chdir'ing into a
// throwaway project instead of pointing at $HOME like the trust harness
// does.
func newPermissionsCmdHarness(t *testing.T, rulesJSON string) (out *bytes.Buffer, run func(args ...string) error) {
	t.Helper()
	dir := t.TempDir()
	if rulesJSON != "" {
		if err := os.MkdirAll(filepath.Join(dir, ".yottacode"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".yottacode", "permissions.json"), []byte(rulesJSON), 0o644); err != nil {
			t.Fatalf("write permissions.json: %v", err)
		}
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
	o := &bytes.Buffer{}
	return o, func(args ...string) error {
		o.Reset()
		root := newPermissionsCmd()
		root.SetOut(o)
		root.SilenceUsage = true
		root.SilenceErrors = true
		root.SetArgs(args)
		return root.Execute()
	}
}

func TestPermissionsTest_MatchesAllowRule(t *testing.T) {
	out, run := newPermissionsCmdHarness(t, `{"permissions":{"allow":["Bash(go *)"]}}`)
	if err := run("test", "run_bash", `{"command":"go test ./..."}`); err != nil {
		t.Fatalf("test: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "verdict: allow") {
		t.Errorf("output = %q, want verdict: allow", got)
	}
	if !strings.Contains(got, "matched: Bash(go *)") {
		t.Errorf("output = %q, want matched rule shown", got)
	}
}

func TestPermissionsTest_BashAliasAcceptsBareCommand(t *testing.T) {
	out, run := newPermissionsCmdHarness(t, `{"permissions":{"deny":["Bash(curl *)"]}}`)
	if err := run("test", "bash", "curl http://evil"); err != nil {
		t.Fatalf("test: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "verdict: deny") {
		t.Errorf("output = %q, want verdict: deny", got)
	}
	if !strings.Contains(got, "tool:    run_bash") {
		t.Errorf("output = %q, want the bash alias resolved to run_bash", got)
	}
}

func TestPermissionsTest_NoRuleMatchedFallsBackToDefault(t *testing.T) {
	out, run := newPermissionsCmdHarness(t, "")
	if err := run("test", "run_bash", `{"command":"ls"}`); err != nil {
		t.Fatalf("test: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "verdict: default") {
		t.Errorf("output = %q, want verdict: default", got)
	}
	if !strings.Contains(got, "falls back to run_bash's own approval policy") {
		t.Errorf("output = %q, want the fallback explanation", got)
	}
}

func TestPermissionsTest_UnsupportedToolNameReportsNA(t *testing.T) {
	out, run := newPermissionsCmdHarness(t, "")
	if err := run("test", "not_a_real_tool", "{}"); err != nil {
		t.Fatalf("test: %v", err)
	}
	if !strings.Contains(out.String(), "n/a") {
		t.Errorf("output = %q, want an n/a verdict for an unrecognized tool name", out.String())
	}
}

func TestPermissionsTest_InvalidJSONErrors(t *testing.T) {
	_, run := newPermissionsCmdHarness(t, "")
	if err := run("test", "write_file", "not json"); err == nil {
		t.Errorf("expected an error for invalid args-json, got nil")
	}
}
