package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
)

// Helper: build a `yottacode openai-auth ...` command rooted at the
// real subtree, redirect IO, return the trees + buffers so each test
// can assert on stdout/stderr without spawning a process.
func newOpenAIAuthCmdHarness(t *testing.T) (cmd *bytes.Buffer, errOut *bytes.Buffer, run func(args ...string) error) {
	t.Helper()
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	root := newOpenAIAuthCmd()
	root.SetOut(out)
	root.SetErr(errBuf)
	root.SilenceUsage = true
	root.SilenceErrors = true
	return out, errBuf, func(args ...string) error {
		out.Reset()
		errBuf.Reset()
		root.SetArgs(args)
		return root.Execute()
	}
}

// status with no token store — payload still written to stdout, but
// the command returns errOpenAIAuthNotLoggedIn so main exits 1.
func TestOpenAIAuthStatus_NotLoggedIn(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "openai-auth.json")

	out, _, run := newOpenAIAuthCmdHarness(t)
	err := run("status", "--store", storePath, "--json")
	if err == nil {
		t.Errorf("expected sentinel error from status when not logged in")
	} else if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in' sentinel, got %v", err)
	}
	if !strings.Contains(out.String(), `"logged_in": false`) {
		t.Errorf("status JSON should include logged_in=false; got %q", out.String())
	}
}

// status when logged in with a fresh token — JSON shape stable, exit 0.
func TestOpenAIAuthStatus_LoggedInJSON(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "openai-auth.json")
	if err := openaiauth.Save(storePath, openaiauth.TokenSet{
		AccessToken:  "atk",
		RefreshToken: "rtk",
		Email:        "p@example.com",
		AccountID:    "user_42",
		ExpiresAt:    time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	out, _, run := newOpenAIAuthCmdHarness(t)
	if err := run("status", "--store", storePath, "--json"); err != nil {
		t.Fatalf("status returned error: %v", err)
	}
	var rep statusReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("unmarshal status: %v\noutput: %s", err, out.String())
	}
	if !rep.LoggedIn || !rep.Valid {
		t.Errorf("expected logged_in=true valid=true; got %+v", rep)
	}
	if rep.Email != "p@example.com" || rep.AccountID != "user_42" {
		t.Errorf("identity claims wrong: %+v", rep)
	}
	if rep.StorePath != storePath {
		t.Errorf("StorePath = %q, want %q", rep.StorePath, storePath)
	}
}

// status text mode shows the human summary, including expiry.
func TestOpenAIAuthStatus_LoggedInText(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "openai-auth.json")
	if err := openaiauth.Save(storePath, openaiauth.TokenSet{
		AccessToken:  "atk",
		RefreshToken: "rtk",
		Email:        "p@example.com",
		AccountID:    "user_42",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	out, _, run := newOpenAIAuthCmdHarness(t)
	if err := run("status", "--store", storePath); err != nil {
		t.Fatalf("status: %v", err)
	}
	got := out.String()
	for _, want := range []string{"logged in as p@example.com", "user_42", "expires:", storePath} {
		if !strings.Contains(got, want) {
			t.Errorf("status output missing %q\n%s", want, got)
		}
	}
}

// logout deletes the store and reports the deletion. Idempotent on a
// missing store — does NOT error.
func TestOpenAIAuthLogout_RemovesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "openai-auth.json")
	if err := openaiauth.Save(storePath, openaiauth.TokenSet{AccessToken: "atk", RefreshToken: "rtk"}); err != nil {
		t.Fatal(err)
	}

	out, _, run := newOpenAIAuthCmdHarness(t)
	if err := run("logout", "--store", storePath, "--yes"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if !strings.Contains(out.String(), "logged out") {
		t.Errorf("expected logout message; got %q", out.String())
	}
	if _, err := openaiauth.Load(storePath); err == nil {
		t.Errorf("store should have been deleted")
	}

	// Idempotent — second call should succeed cleanly with the
	// "already logged out" message, not error.
	if err := run("logout", "--store", storePath, "--yes"); err != nil {
		t.Errorf("second logout should be idempotent; got %v", err)
	}
	if !strings.Contains(out.String(), "already logged out") {
		t.Errorf("expected 'already logged out'; got %q", out.String())
	}
}

// The login flow uses a real browser launch + callback server; not
// testable end-to-end without an OAuth fixture. Asserting on the
// command tree's shape (subcommands registered + help text) covers
// the wiring without trying to drive the OAuth dance from a unit
// test.
func TestOpenAIAuthCmd_TreeShape(t *testing.T) {
	cmd := newOpenAIAuthCmd()
	want := map[string]bool{"login": false, "logout": false, "status": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("openai-auth subtree missing %q subcommand", name)
		}
	}
}

// TestOpenAIAuthLogin_HasCandidatesFlag verifies the maintainer-only
// flag the standalone probe binary used to expose now lives on the
// blessed user-facing surface. Lookup-only — wiring the flag into a
// scan needs the OAuth fixture.
func TestOpenAIAuthLogin_HasCandidatesFlag(t *testing.T) {
	tree := newOpenAIAuthCmd()
	var login *cobra.Command
	for _, sub := range tree.Commands() {
		if sub.Name() == "login" {
			login = sub
			break
		}
	}
	if login == nil {
		t.Fatal("login subcommand missing from openai-auth tree")
	}
	if f := login.Flags().Lookup("candidates"); f == nil {
		t.Errorf("login is missing --candidates flag")
	}
	if f := login.Flags().Lookup("store"); f == nil {
		t.Errorf("login is missing --store flag")
	}
}

func TestRenderScanTable(t *testing.T) {
	var buf bytes.Buffer
	renderScanTable(&buf, []openaiauth.ScanResult{
		{Name: "gpt-5.5", OK: true, Status: "OK", Detail: "200, response received"},
		{Name: "gpt-5.4-mini", OK: false, Status: "403", Detail: "not in your plan"},
	})
	out := buf.String()
	for _, want := range []string{"MODEL", "STATUS", "DETAIL", "gpt-5.5", "OK", "gpt-5.4-mini", "403", "not in your plan"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Header separator is part of the contract — scripts can split on
	// the dashed line to skip the banner and parse rows.
	if !strings.Contains(out, "----") {
		t.Errorf("output missing separator line:\n%s", out)
	}
}
