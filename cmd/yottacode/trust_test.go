package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTrustCmdHarness builds a `yottacode trust ...` command tree
// pointed at an isolated $HOME so each test gets a clean
// trusted-roots.json under tempdir.
func newTrustCmdHarness(t *testing.T) (out *bytes.Buffer, errOut *bytes.Buffer, run func(args ...string) error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	o := &bytes.Buffer{}
	e := &bytes.Buffer{}
	return o, e, func(args ...string) error {
		o.Reset()
		e.Reset()
		root := newTrustCmd()
		root.SetOut(o)
		root.SetErr(e)
		root.SilenceUsage = true
		root.SilenceErrors = true
		root.SetArgs(args)
		return root.Execute()
	}
}

func TestTrustListEmpty(t *testing.T) {
	out, _, run := newTrustCmdHarness(t)
	if err := run("list"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "(no trusted roots)") {
		t.Errorf("empty list output = %q, want sentinel", out.String())
	}
}

func TestTrustAddAndList(t *testing.T) {
	target := t.TempDir()
	out, _, run := newTrustCmdHarness(t)

	if err := run("add", target); err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out.String(), "trusted: "+target) {
		t.Errorf("add output = %q, want 'trusted: %s'", out.String(), target)
	}

	if err := run("list"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), target) {
		t.Errorf("list output = %q, missing target %s", out.String(), target)
	}
}

func TestTrustAddDuplicate(t *testing.T) {
	target := t.TempDir()
	out, _, run := newTrustCmdHarness(t)
	if err := run("add", target); err != nil {
		t.Fatal(err)
	}
	if err := run("add", target); err != nil {
		t.Fatalf("second add: %v", err)
	}
	if !strings.Contains(out.String(), "already trusted") {
		t.Errorf("duplicate add output = %q, want 'already trusted'", out.String())
	}
}

func TestTrustRemove(t *testing.T) {
	target := t.TempDir()
	out, _, run := newTrustCmdHarness(t)
	if err := run("add", target); err != nil {
		t.Fatal(err)
	}
	if err := run("remove", target); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out.String(), "removed: "+target) {
		t.Errorf("remove output = %q", out.String())
	}
	if err := run("list"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "(no trusted roots)") {
		t.Errorf("list after remove = %q, want empty", out.String())
	}
}

func TestTrustRemoveMissing(t *testing.T) {
	_, _, run := newTrustCmdHarness(t)
	err := run("remove", "/does/not/exist")
	if err == nil {
		t.Fatal("remove of missing entry: want error, got nil")
	}
	if !strings.Contains(err.Error(), "not in trust store") {
		t.Errorf("remove error = %v, want 'not in trust store'", err)
	}
}

func TestTrustClear(t *testing.T) {
	out, _, run := newTrustCmdHarness(t)
	if err := run("add", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := run("add", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := run("clear"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if !strings.Contains(out.String(), "cleared 2 trusted root(s)") {
		t.Errorf("clear output = %q", out.String())
	}
}

func TestTrustAddPersistsToHome(t *testing.T) {
	target := t.TempDir()
	_, _, run := newTrustCmdHarness(t)
	if err := run("add", target); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(os.Getenv("HOME"), ".yottacode", "trusted-roots.json")
	b, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("expected trusted-roots.json at %s: %v", storePath, err)
	}
	if !strings.Contains(string(b), target) {
		t.Errorf("on-disk store = %s, missing %s", string(b), target)
	}
}
