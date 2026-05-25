package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: with the default deny list populated, a read of a
// credential-bearing path is refused even though read_file otherwise
// auto-executes. Closes the silent prompt-injection exfiltration vector.
func TestReadFileTool_DefaultDeny_RefusesCredentialPath(t *testing.T) {
	tmp := t.TempDir()
	// Plant a fake "credential" file at one of the deny-listed paths.
	// Use a sub-tree we control: a fake $HOME inside tmp, then point the
	// deny list at it.
	fakeHome := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(fakeHome, ".aws"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	credPath := filepath.Join(fakeHome, ".aws", "credentials")
	if err := os.WriteFile(credPath, []byte("[default]\nkey=SECRET\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := &ReadFileTool{
		Cwd:           NewCwdRef(tmp),
		DenyReadPaths: []string{filepath.Join(fakeHome, ".aws")},
	}
	args, _ := json.Marshal(map[string]string{"path": credPath})
	out, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatalf("expected error, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "read deny list") {
		t.Errorf("expected deny-list error, got %v", err)
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("credential leaked into tool output: %q", out)
	}
}

// Without the deny list (empty), reads pass through normally — the
// validator is opt-in via DenyReadPaths population.
func TestReadFileTool_EmptyDenyList_AllowsRead(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "foo.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := &ReadFileTool{Cwd: NewCwdRef(tmp)}
	args, _ := json.Marshal(map[string]string{"path": p})
	out, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "     1\thello" {
		t.Errorf("got %q", out)
	}
}

// Regression: read_many_files must enforce the deny list per-path.
// One denied entry in the list refuses the whole call.
func TestReadManyFilesTool_DefaultDeny_RefusesCredentialPath(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(fakeHome, ".ssh"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	keyPath := filepath.Join(fakeHome, ".ssh", "id_rsa")
	if err := os.WriteFile(keyPath, []byte("BEGIN PRIVATE KEY"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	benign := filepath.Join(tmp, "ok.txt")
	if err := os.WriteFile(benign, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := &ReadManyFilesTool{
		Cwd:           NewCwdRef(tmp),
		DenyReadPaths: []string{filepath.Join(fakeHome, ".ssh")},
	}
	args, _ := json.Marshal(map[string]any{"paths": []string{benign, keyPath}})
	out, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatalf("expected error, got out=%q", out)
	}
	if strings.Contains(out, "BEGIN PRIVATE KEY") {
		t.Errorf("credential leaked: %q", out)
	}
}

// Regression: grep refuses to recurse into a denied path. Without
// this, `grep "API_KEY=" ~/.aws/credentials` extracts secrets line by
// line just as effectively as read_file.
func TestGrepTool_DefaultDeny_RefusesCredentialPath(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "home", ".aws")
	if err := os.MkdirAll(fakeHome, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fakeHome, "credentials"), []byte("aws_secret_access_key=SECRET\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tool := &GrepTool{
		Cwd:           NewCwdRef(tmp),
		DenyReadPaths: []string{filepath.Join(tmp, "home", ".aws")},
	}
	args, _ := json.Marshal(map[string]any{
		"pattern": "secret",
		"path":    fakeHome,
	})
	out, err := tool.Execute(context.Background(), string(args))
	if err == nil {
		t.Fatalf("expected error, got out=%q", out)
	}
	if strings.Contains(out, "SECRET") {
		t.Errorf("credential leaked: %q", out)
	}
}

// DefaultDenyReadPaths returns a non-empty list with the well-known
// credential locations under $HOME plus project-local .env files.
func TestDefaultDenyReadPaths_PopulatesExpectedEntries(t *testing.T) {
	cwd := t.TempDir()
	got := DefaultDenyReadPaths(cwd)
	if len(got) == 0 {
		t.Fatalf("expected non-empty deny list")
	}
	mustContain := []string{
		filepath.Join(cwd, ".env"),
		filepath.Join(cwd, ".env.local"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		mustContain = append(mustContain,
			filepath.Join(home, ".ssh"),
			filepath.Join(home, ".aws"),
			filepath.Join(home, ".yottacode", ".env"),
		)
	}
	for _, want := range mustContain {
		found := false
		for _, p := range got {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("deny list missing %q", want)
		}
	}
}

// ValidateReadPath rejects empty paths and NUL bytes — same hardening
// the write validator does.
func TestValidateReadPath_RejectsMalformedPaths(t *testing.T) {
	if err := ValidateReadPath("", nil); err == nil {
		t.Errorf("expected empty-path error")
	}
	if err := ValidateReadPath("foo\x00bar", nil); err == nil {
		t.Errorf("expected NUL-byte error")
	}
}
