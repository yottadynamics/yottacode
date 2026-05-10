package dotenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBasic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := `# leading comment
KEY1=value1
KEY2 = value2
export KEY3=value3
EMPTY=
QUOTED="hello world"
SINGLE='no $expansion'
TRAILING=value # comment after value
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{
		"KEY1":     "value1",
		"KEY2":     "value2",
		"KEY3":     "value3",
		"EMPTY":    "",
		"QUOTED":   "hello world",
		"SINGLE":   "no $expansion",
		"TRAILING": "value",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent.env"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("missing file should yield empty map, got %v", got)
	}
}

// SetKey writes a fresh file with the managed header when no file
// exists yet, then merges new keys without disturbing existing ones.
// The round-trip — write, read, write again — must be idempotent
// because /provider add and /provider remove call SetKey/DeleteKey
// repeatedly across a session.
func TestSetKey_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := SetKey(path, "ANTHROPIC_API_KEY", "sk-ant-1"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if err := SetKey(path, "OPENAI_API_KEY", "sk-openai-2"); err != nil {
		t.Fatalf("SetKey 2: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["ANTHROPIC_API_KEY"] != "sk-ant-1" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want sk-ant-1", got["ANTHROPIC_API_KEY"])
	}
	if got["OPENAI_API_KEY"] != "sk-openai-2" {
		t.Errorf("OPENAI_API_KEY = %q, want sk-openai-2", got["OPENAI_API_KEY"])
	}
	// File must end up with the managed header so users opening it
	// know it's rewritten by yottacode.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(string(body), "# yottacode-managed API keys.") {
		t.Errorf("missing managed header; body:\n%s", body)
	}
}

// File mode must be 0600 — API keys live here.
func TestSetKey_FileMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := SetKey(path, "X", "y"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 0600", mode)
	}
}

// DeleteKey removes the named key, preserves the rest, and reports
// (true, nil) so the caller can log the deletion. The .env on disk
// after the delete must NOT contain the deleted key.
func TestDeleteKey_RemovesAndReports(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := SetKey(path, "ANTHROPIC_API_KEY", "sk-ant"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if err := SetKey(path, "NVIDIA_API_KEY", "nvapi-test"); err != nil {
		t.Fatalf("SetKey 2: %v", err)
	}
	deleted, err := DeleteKey(path, "NVIDIA_API_KEY")
	if err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if !deleted {
		t.Errorf("DeleteKey should report true when key was present")
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := got["NVIDIA_API_KEY"]; ok {
		t.Errorf("NVIDIA_API_KEY should be gone, got %q", got["NVIDIA_API_KEY"])
	}
	if got["ANTHROPIC_API_KEY"] != "sk-ant" {
		t.Errorf("ANTHROPIC_API_KEY should be preserved, got %q", got["ANTHROPIC_API_KEY"])
	}
}

// Missing file is a no-op — DeleteKey returns (false, nil) so
// callers (provider remove cleanup) can run unconditionally without
// branching on file existence.
func TestDeleteKey_MissingFileNoOp(t *testing.T) {
	deleted, err := DeleteKey(filepath.Join(t.TempDir(), "absent.env"), "ANYTHING")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if deleted {
		t.Errorf("missing file should report deleted=false")
	}
}

// Missing key in an existing file is also a no-op — file is left
// untouched, deleted=false.
func TestDeleteKey_MissingKeyNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := SetKey(path, "OTHER_KEY", "value"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	deleted, err := DeleteKey(path, "ABSENT_KEY")
	if err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if deleted {
		t.Errorf("missing key should report deleted=false")
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["OTHER_KEY"] != "value" {
		t.Errorf("OTHER_KEY should be preserved, got %q", got["OTHER_KEY"])
	}
}

// Invalid key names (POSIX-violating) are rejected at SetKey, not
// silently written. Without this guard a typo like SetKey(path,
// "9BAD", "v") writes a file Load() refuses to parse on the next
// open.
func TestSetKey_RejectsInvalidKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := SetKey(path, "9BAD", "v"); err == nil {
		t.Errorf("SetKey with leading digit should error")
	}
	if err := SetKey(path, "BAD-KEY", "v"); err == nil {
		t.Errorf("SetKey with hyphen should error")
	}
}

func TestLoadInvalidKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("9BAD=oops\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Errorf("expected error for digit-leading key")
	} else if !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("error should mention invalid key, got %v", err)
	}
}

func TestLoadMissingEquals(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("BARE_LINE\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Errorf("expected error for missing =")
	}
}

func TestLoadChainPrecedence(t *testing.T) {
	dir := t.TempDir()
	low := filepath.Join(dir, "low.env")
	high := filepath.Join(dir, "high.env")
	if err := os.WriteFile(low, []byte("A=1\nB=2\n"), 0o644); err != nil {
		t.Fatalf("write low: %v", err)
	}
	if err := os.WriteFile(high, []byte("B=99\nC=3\n"), 0o644); err != nil {
		t.Fatalf("write high: %v", err)
	}
	got, err := LoadChain(low, high)
	if err != nil {
		t.Fatalf("LoadChain: %v", err)
	}
	if got["A"] != "1" {
		t.Errorf("A: want 1, got %q", got["A"])
	}
	if got["B"] != "99" {
		t.Errorf("B should be overridden by high, got %q", got["B"])
	}
	if got["C"] != "3" {
		t.Errorf("C: want 3, got %q", got["C"])
	}
}

func TestApplyDoesNotOverrideEnv(t *testing.T) {
	t.Setenv("YOTTA_TEST_PREEXISTING", "from-env")
	applied := Apply(map[string]string{
		"YOTTA_TEST_PREEXISTING": "from-file",
		"YOTTA_TEST_NEW":         "from-file",
	})
	if got := os.Getenv("YOTTA_TEST_PREEXISTING"); got != "from-env" {
		t.Errorf("OS env should win, got %q", got)
	}
	if got := os.Getenv("YOTTA_TEST_NEW"); got != "from-file" {
		t.Errorf("missing var should be set from file, got %q", got)
	}
	hasNew := false
	for _, k := range applied {
		if k == "YOTTA_TEST_NEW" {
			hasNew = true
		}
		if k == "YOTTA_TEST_PREEXISTING" {
			t.Errorf("preexisting should not appear in applied list")
		}
	}
	if !hasNew {
		t.Errorf("applied list should include YOTTA_TEST_NEW, got %v", applied)
	}
}
