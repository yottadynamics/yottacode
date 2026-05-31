package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// memTestSetup hijacks HOME so the tools write under a tempdir and
// returns (home, cwd). The cwd is also a fresh tempdir so the
// project slug (filepath.Base) is deterministic per test.
func memTestSetup(t *testing.T) (home, cwd string) {
	t.Helper()
	home = t.TempDir()
	cwd = t.TempDir()
	t.Setenv("HOME", home)
	return home, cwd
}

func TestMemorySave_WritesFileAndIndex(t *testing.T) {
	home, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	out, err := tool.Execute(context.Background(), `{
		"scope": "user",
		"type": "feedback",
		"name": "verbose-output",
		"description": "user dislikes wall-of-stack output",
		"content": "Keep stack traces collapsed by default; expand only on request."
	}`)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(out, "created user memory") {
		t.Errorf("expected create confirmation, got %q", out)
	}
	path := filepath.Join(home, ".yottacode", "memory", "verbose-output.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("memory file missing: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"---",
		"name: verbose-output",
		"type: feedback",
		"description: user dislikes wall-of-stack output",
		"created: ",
		"Keep stack traces collapsed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("memory file missing %q\n--- file ---\n%s", want, body)
		}
	}
	indexPath := filepath.Join(home, ".yottacode", "memory", "MEMORY.md")
	idx, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("index missing: %v", err)
	}
	if !strings.Contains(string(idx), "verbose-output") {
		t.Errorf("index does not list new memory:\n%s", idx)
	}
}

func TestMemorySave_OverwritesExisting(t *testing.T) {
	home, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	args1 := `{"scope":"user","type":"user","name":"prefs","description":"old","content":"first body"}`
	args2 := `{"scope":"user","type":"user","name":"prefs","description":"new","content":"second body"}`
	if _, err := tool.Execute(context.Background(), args1); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if _, err := tool.Execute(context.Background(), args2); err != nil {
		t.Fatalf("second save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "prefs.md"))
	if err != nil {
		t.Fatalf("read after overwrite: %v", err)
	}
	body := string(data)
	if strings.Contains(body, "first body") {
		t.Errorf("overwrite should replace body, not append; got:\n%s", body)
	}
	if !strings.Contains(body, "second body") {
		t.Errorf("expected second body in file:\n%s", body)
	}
	if !strings.Contains(body, "description: new") {
		t.Errorf("description should be overwritten; got:\n%s", body)
	}
	idx, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if c := strings.Count(string(idx), "[prefs]"); c != 1 {
		t.Errorf("index should list memory exactly once after overwrite, got %d:\n%s", c, idx)
	}
}

// TestMemorySave_OverwriteArchivesAndPreservesCreated guards the
// no-silent-loss fix: an overwrite that changes content archives the
// prior version (recoverable) and preserves the original created
// timestamp, and the result distinguishes created vs updated.
//
// The v1 file is written DIRECTLY with a deliberately historical created,
// so the preservation assertion is clock-independent — an un-preserved
// time.Now() would render today's date, not 2020, and fail. (A prior
// version of this test saved v1 through the tool and compared two
// back-to-back saves, which both render the same 1-second-resolution
// RFC3339 string and so passed even with the fix removed.)
func TestMemorySave_OverwriteArchivesAndPreservesCreated(t *testing.T) {
	home, cwd := memTestSetup(t)
	memDir := filepath.Join(home, ".yottacode", "memory")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const histCreated = "2020-01-01T00:00:00Z"
	v1 := "---\nname: prefs\ntype: feedback\ndescription: v1\ncreated: " + histCreated + "\n---\nORIGINAL CONTENT\n"
	if err := os.WriteFile(filepath.Join(memDir, "prefs.md"), []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	// A different fact saved under the same name (the silent-loss scenario).
	out, err := tool.Execute(context.Background(),
		`{"scope":"user","type":"feedback","name":"prefs","description":"v2","content":"COMPLETELY DIFFERENT FACT"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "updated") || !strings.Contains(out, "archived") {
		t.Errorf("overwrite of different content should report updated + archived; got %q", out)
	}

	// Current file holds the new content with the ORIGINAL (historical)
	// created preserved — provably, not by clock coincidence.
	cur, _ := os.ReadFile(filepath.Join(memDir, "prefs.md"))
	if !strings.Contains(string(cur), "COMPLETELY DIFFERENT FACT") {
		t.Errorf("current file should hold the new content")
	}
	fm, _, _ := memory.ParseFrontmatter(cur)
	if fm.Created != histCreated {
		t.Errorf("created must be preserved across update: want %q, got %q", histCreated, fm.Created)
	}

	// The prior version is recoverable in .archive, not silently lost.
	archDir := filepath.Join(memDir, memory.ArchiveDirName)
	entries, err := os.ReadDir(archDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected an archived prior version in %s; err=%v", archDir, err)
	}
	var foundOriginal bool
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(archDir, e.Name()))
		if strings.Contains(string(b), "ORIGINAL CONTENT") {
			foundOriginal = true
		}
	}
	if !foundOriginal {
		t.Error("archived copy should contain the original content")
	}

	// The archive must NOT leak into the index or the scanned corpus.
	idx, _ := os.ReadFile(filepath.Join(memDir, "MEMORY.md"))
	if c := strings.Count(string(idx), "[prefs]"); c != 1 {
		t.Errorf("index should list prefs exactly once (archive excluded); got %d", c)
	}
	loaded, _ := memory.Load(cwd)
	if len(loaded.UserMemories) != 1 {
		t.Errorf("scan should see exactly 1 user memory (archive excluded); got %d", len(loaded.UserMemories))
	}
}

// TestMemorySave_ConcurrentSameNameNoSilentLoss guards the per-path lock:
// N concurrent saves to the same name (the parent-loop + background-
// subagent scenario, which shares one *MemorySaveTool) must lose nothing
// — every distinct version stays recoverable from the live file or an
// archive copy. Without the lock the racers archive the same prior and
// the last write wins, so most versions vanish.
func TestMemorySave_ConcurrentSameNameNoSilentLoss(t *testing.T) {
	home, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	const n = 30

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := fmt.Sprintf(`{"scope":"user","type":"feedback","name":"hot","description":"d","content":"VERSION-%02d-unique"}`, i)
			if _, err := tool.Execute(context.Background(), args); err != nil {
				t.Errorf("save %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	memDir := filepath.Join(home, ".yottacode", "memory")
	seen := map[int]bool{}
	collect := func(b []byte) {
		for i := 0; i < n; i++ {
			if strings.Contains(string(b), fmt.Sprintf("VERSION-%02d-unique", i)) {
				seen[i] = true
			}
		}
	}
	live, _ := os.ReadFile(filepath.Join(memDir, "hot.md"))
	collect(live)
	archDir := filepath.Join(memDir, memory.ArchiveDirName)
	ents, _ := os.ReadDir(archDir)
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(archDir, e.Name()))
		collect(b)
	}
	if len(seen) != n {
		t.Errorf("expected all %d versions recoverable (live + archive); got %d — silent loss under concurrency", n, len(seen))
	}
}

func TestMemorySave_RoutesScopes(t *testing.T) {
	home, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	if _, err := tool.Execute(context.Background(), `{"scope":"user","type":"user","name":"prefs","description":"x","content":"x"}`); err != nil {
		t.Fatalf("user-scope save: %v", err)
	}
	if _, err := tool.Execute(context.Background(), `{"scope":"project","type":"project","name":"layout","description":"x","content":"x"}`); err != nil {
		t.Fatalf("project-scope save: %v", err)
	}
	userPath := filepath.Join(home, ".yottacode", "memory", "prefs.md")
	if _, err := os.Stat(userPath); err != nil {
		t.Errorf("user-scope file missing at %s: %v", userPath, err)
	}
	projects := filepath.Join(home, ".yottacode", "projects")
	infos, err := os.ReadDir(projects)
	if err != nil {
		t.Fatalf("projects dir missing: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected exactly one project subdir under %s, got %d", projects, len(infos))
	}
	projectMemPath := filepath.Join(projects, infos[0].Name(), "memory", "layout.md")
	if _, err := os.Stat(projectMemPath); err != nil {
		t.Errorf("project-scope file missing at %s: %v", projectMemPath, err)
	}
}

func TestMemorySave_RejectsBadName(t *testing.T) {
	_, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	cases := []struct {
		name string
		args string
	}{
		{"uppercase", `{"scope":"user","type":"user","name":"BadName","description":"x","content":"x"}`},
		{"traversal", `{"scope":"user","type":"user","name":"../escape","description":"x","content":"x"}`},
		{"reserved", `{"scope":"user","type":"user","name":"memory","description":"x","content":"x"}`},
		{"reserved2", `{"scope":"user","type":"user","name":"yottacode","description":"x","content":"x"}`},
		{"empty", `{"scope":"user","type":"user","name":"","description":"x","content":"x"}`},
		{"badscope", `{"scope":"global","type":"user","name":"foo","description":"x","content":"x"}`},
		{"emptytype", `{"scope":"user","type":"","name":"foo","description":"x","content":"x"}`},
		{"badtypechars", `{"scope":"user","type":"a/b","name":"foo","description":"x","content":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := tool.Execute(context.Background(), c.args); err == nil {
				t.Errorf("expected error for %s, got nil", c.name)
			}
		})
	}
}

func TestValidateMemoryType_FreeForm(t *testing.T) {
	// Conventional + custom labels are accepted and normalized.
	for _, c := range []struct{ in, want string }{
		{"user", "user"},
		{"feedback", "feedback"},
		{"decision", "decision"},
		{"  Gotcha ", "gotcha"}, // trimmed + lowercased
		// Separator variants all canonicalize to a single-hyphen form so
		// they group as ONE "## <type>" section in the index, not three.
		{"API-shape", "api-shape"},
		{"api shape", "api-shape"},
		{"api_shape", "api-shape"},
		{"design note", "design-note"},
		{"a  b__c--d", "a-b-c-d"}, // runs of mixed separators collapse
		{"-leading-trailing-", "leading-trailing"}, // ends trimmed
	} {
		got, err := validateMemoryType(c.in)
		if err != nil {
			t.Errorf("validateMemoryType(%q) errored: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("validateMemoryType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Genuinely invalid labels are still rejected.
	for _, bad := range []string{"", "   ", "a/b", "type#1", "a\nb", strings.Repeat("x", 33)} {
		if _, err := validateMemoryType(bad); err == nil {
			t.Errorf("validateMemoryType(%q) should have errored", bad)
		}
	}
}

func TestMemorySave_FreeFormTypeRoundtripsAndIndexes(t *testing.T) {
	home, cwd := memTestSetup(t)
	save := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	if _, err := save.Execute(context.Background(),
		`{"scope":"user","type":"Decision","name":"queue-writes","description":"db writes go behind a queue","content":"why X"}`); err != nil {
		t.Fatalf("save with custom type: %v", err)
	}
	// Frontmatter stores the normalized custom type.
	data, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "queue-writes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "type: decision") {
		t.Errorf("custom type not stored normalized; got:\n%s", data)
	}
	// The custom type appears as its own group in the index.
	idx, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(idx), "## decision") || !strings.Contains(string(idx), "queue-writes") {
		t.Errorf("custom-type memory missing from index; got:\n%s", idx)
	}
}

func TestMemoryForget_DeletesAndUpdatesIndex(t *testing.T) {
	home, cwd := memTestSetup(t)
	save := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	if _, err := save.Execute(context.Background(), `{"scope":"user","type":"user","name":"a","description":"x","content":"x"}`); err != nil {
		t.Fatalf("save a: %v", err)
	}
	if _, err := save.Execute(context.Background(), `{"scope":"user","type":"user","name":"b","description":"x","content":"x"}`); err != nil {
		t.Fatalf("save b: %v", err)
	}
	forget := &MemoryForgetTool{Cwd: NewCwdRef(cwd)}
	if _, err := forget.Execute(context.Background(), `{"scope":"user","name":"a"}`); err != nil {
		t.Fatalf("forget a: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".yottacode", "memory", "a.md")); !os.IsNotExist(err) {
		t.Errorf("expected a.md gone, stat err = %v", err)
	}
	idx, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "MEMORY.md"))
	if err != nil {
		t.Fatalf("index missing: %v", err)
	}
	if strings.Contains(string(idx), "[a]") {
		t.Errorf("index still references forgotten memory:\n%s", idx)
	}
	if !strings.Contains(string(idx), "[b]") {
		t.Errorf("index lost remaining memory:\n%s", idx)
	}
}

func TestMemoryForget_RemovesIndexWhenEmpty(t *testing.T) {
	home, cwd := memTestSetup(t)
	save := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	if _, err := save.Execute(context.Background(), `{"scope":"user","type":"user","name":"only","description":"x","content":"x"}`); err != nil {
		t.Fatalf("save: %v", err)
	}
	forget := &MemoryForgetTool{Cwd: NewCwdRef(cwd)}
	if _, err := forget.Execute(context.Background(), `{"scope":"user","name":"only"}`); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".yottacode", "memory", "MEMORY.md")); !os.IsNotExist(err) {
		t.Errorf("expected MEMORY.md gone after last forget, stat err = %v", err)
	}
}

func TestMemoryForget_MissingNameErrors(t *testing.T) {
	_, cwd := memTestSetup(t)
	forget := &MemoryForgetTool{Cwd: NewCwdRef(cwd)}
	out, err := forget.Execute(context.Background(), `{"scope":"user","name":"never-saved"}`)
	if err == nil {
		t.Fatalf("expected error forgetting nonexistent memory, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "no user memory") {
		t.Errorf("expected 'no user memory' in error, got: %v", err)
	}
}

func TestMemoryTools_RequiresApprovalFalse(t *testing.T) {
	if (&MemorySaveTool{}).RequiresApproval("") {
		t.Errorf("MemorySaveTool.RequiresApproval should be false (silent per design)")
	}
	if (&MemoryForgetTool{}).RequiresApproval("") {
		t.Errorf("MemoryForgetTool.RequiresApproval should be false (silent per design)")
	}
}

func TestMemoryTools_NotParallelSafe(t *testing.T) {
	if (&MemorySaveTool{}).ParallelSafe("") {
		t.Errorf("MemorySaveTool should not be parallel-safe (rewrites index)")
	}
	if (&MemoryForgetTool{}).ParallelSafe("") {
		t.Errorf("MemoryForgetTool should not be parallel-safe (rewrites index)")
	}
}
