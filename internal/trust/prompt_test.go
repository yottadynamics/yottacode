package trust

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptYes(t *testing.T) {
	cases := []string{"y\n", "Y\n", "yes\n", "1\n", "  yes  \n"}
	for _, in := range cases {
		var out bytes.Buffer
		res, err := Prompt(strings.NewReader(in), &out, "/home/me/repo")
		if err != nil {
			t.Fatalf("Prompt(%q): %v", in, err)
		}
		if res != PromptYes {
			t.Errorf("Prompt(%q) = %v, want PromptYes", in, res)
		}
	}
}

func TestPromptNo(t *testing.T) {
	cases := []string{"n\n", "no\n", "2\n", "\n", "anything\n"}
	for _, in := range cases {
		var out bytes.Buffer
		res, err := Prompt(strings.NewReader(in), &out, "/home/me/repo")
		if err != nil {
			t.Fatalf("Prompt(%q): %v", in, err)
		}
		if res != PromptNo {
			t.Errorf("Prompt(%q) = %v, want PromptNo", in, res)
		}
	}
}

func TestPromptShowsAbsolutePath(t *testing.T) {
	var out bytes.Buffer
	_, err := Prompt(strings.NewReader("n\n"), &out, ".")
	if err != nil {
		t.Fatal(err)
	}
	// Absolute path of "." is whatever the test process cwd is — but
	// it must not be the literal "."
	if strings.Contains(out.String(), "\n  .\n") {
		t.Error("prompt body should show absolute path, not '.'")
	}
}

func TestEnsureAlreadyTrustedNoOp(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "trusted-roots.json")
	s := &Store{}
	s.Add("/home/me/repo")
	var in, out bytes.Buffer // empty in — would block if a prompt fired
	if err := Ensure(s, storePath, "/home/me/repo", nil, true, &in, &out); err != nil {
		t.Fatalf("Ensure for trusted cwd: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("Ensure should not write to out for trusted cwd, got %q", out.String())
	}
}

func TestEnsureNonInteractiveSkips(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "trusted-roots.json")
	s := &Store{}
	var in, out bytes.Buffer
	if err := Ensure(s, storePath, "/home/me/fresh", nil, false, &in, &out); err != nil {
		t.Errorf("Ensure non-interactive: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("Ensure non-interactive should not prompt, got %q", out.String())
	}
	if len(s.Roots) != 0 {
		t.Error("Ensure non-interactive should not persist trust")
	}
}

func TestEnsurePromptYesPersists(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "trusted-roots.json")
	s := &Store{}
	in := strings.NewReader("y\n")
	var out bytes.Buffer
	cwd := t.TempDir() // real absolute path
	if err := Ensure(s, storePath, cwd, nil, true, in, &out); err != nil {
		t.Fatalf("Ensure prompt-yes: %v", err)
	}
	if !s.Contains(cwd) {
		t.Errorf("store should contain cwd after Yes, got roots=%v", s.Roots)
	}
	// File should be on disk
	loaded, err := Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Contains(cwd) {
		t.Error("loaded store should contain cwd after Yes")
	}
}

func TestEnsurePromptNoReturnsErrUserDeclined(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "trusted-roots.json")
	s := &Store{}
	in := strings.NewReader("n\n")
	var out bytes.Buffer
	err := Ensure(s, storePath, "/home/me/fresh", nil, true, in, &out)
	if !errors.Is(err, ErrUserDeclined) {
		t.Errorf("Ensure on No: err = %v, want ErrUserDeclined", err)
	}
	if len(s.Roots) != 0 {
		t.Error("Ensure on No: store should be untouched")
	}
}

func TestEnsureAllowPathsSatisfiesGate(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "trusted-roots.json")
	s := &Store{}
	var in, out bytes.Buffer
	// cwd is /foo/bar, but --allow-paths /foo means we trust the subtree
	if err := Ensure(s, storePath, "/foo/bar", []string{"/foo"}, true, &in, &out); err != nil {
		t.Errorf("Ensure with allow-paths covering cwd: %v", err)
	}
	if len(s.Roots) != 0 {
		t.Error("allow-paths should not persist to trust store")
	}
	if out.Len() != 0 {
		t.Error("allow-paths should suppress the prompt entirely")
	}
}

func TestEnsureEnvOverride(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "trusted-roots.json")
	s := &Store{}
	t.Setenv(EnvTrustAll, "1")
	var in, out bytes.Buffer
	if err := Ensure(s, storePath, "/foo/bar", nil, true, &in, &out); err != nil {
		t.Errorf("Ensure with YOTTACODE_TRUST_ALL: %v", err)
	}
	if len(s.Roots) != 0 {
		t.Error("YOTTACODE_TRUST_ALL should not persist trust")
	}
	if out.Len() != 0 {
		t.Error("YOTTACODE_TRUST_ALL should suppress prompt")
	}
}
