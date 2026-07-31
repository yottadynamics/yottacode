package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

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
	t.Setenv("YOTTACODE_HOME", "")
	return home, cwd
}

// TestMemorySave_ScopeReflectionHint pins the scope-check reflection:
// it must fire on the one suspicious combination (a portable-typed
// memory filed as project scope) and stay silent everywhere else, or
// the model learns to ignore it.
func TestMemorySave_ScopeReflectionHint(t *testing.T) {
	cases := []struct {
		name     string
		scope    string
		memType  string
		wantHint bool
	}{
		{"project scope + user type fires", "project", "user", true},
		{"project scope + feedback type fires", "project", "feedback", true},
		// Non-canonical input: the hint depends on validateMemoryType
		// lowercasing "Feedback" -> "feedback" BEFORE the portableMemoryTypes
		// lookup. Pins that the reflection keys on the normalized type, not
		// the raw arg — a regression that looked up a.Type would miss this.
		{"project scope + non-canonical Feedback fires", "project", "Feedback", true},
		{"project scope + project type silent", "project", "project", false},
		{"project scope + free-form type silent", "project", "gotcha", false},
		{"user scope + feedback type silent", "user", "feedback", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, cwd := memTestSetup(t)
			tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
			out, err := tool.Execute(context.Background(), fmt.Sprintf(`{
				"scope": %q,
				"type": %q,
				"name": "scope-hint-probe",
				"description": "x",
				"content": "x"
			}`, c.scope, c.memType))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got := strings.Contains(out, "scope check:"); got != c.wantHint {
				t.Errorf("scope=%s type=%s: hint present = %v, want %v; result: %q",
					c.scope, c.memType, got, c.wantHint, out)
			}
		})
	}
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
	path := filepath.Join(home, ".yottacode", "memory", "user", "verbose-output.md")
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
	indexPath := filepath.Join(home, ".yottacode", "memory", "user", "MEMORY.md")
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
	data, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "user", "prefs.md"))
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
	idx, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "user", "MEMORY.md"))
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
	memDir := filepath.Join(home, ".yottacode", "memory", "user")
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

	memDir := filepath.Join(home, ".yottacode", "memory", "user")
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

func TestMemorySave_ConcurrentDifferentNamesKeepsCompleteIndex(t *testing.T) {
	home, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	const n = 40

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := fmt.Sprintf(`{"scope":"user","type":"reference","name":"mem-%02d","description":"memory %02d","content":"body %02d"}`, i, i, i)
			if _, err := tool.Execute(context.Background(), args); err != nil {
				t.Errorf("save %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	idx, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "user", "MEMORY.md"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("mem-%02d", i)
		if !strings.Contains(string(idx), "["+name+"]") {
			t.Fatalf("index missing %s after concurrent saves:\n%s", name, idx)
		}
	}
}

func TestMemorySave_ReadErrorDoesNotOverwriteExisting(t *testing.T) {
	home, cwd := memTestSetup(t)
	memDir := filepath.Join(home, ".yottacode", "memory", "user")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(memDir, "locked.md")
	original := "---\nname: locked\ntype: reference\ndescription: original\ncreated: 2020-01-01T00:00:00Z\n---\nORIGINAL\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	_, err := tool.Execute(context.Background(), `{"scope":"user","type":"reference","name":"locked","description":"new","content":"NEW"}`)
	if err == nil || !strings.Contains(err.Error(), "read existing") {
		t.Fatalf("expected read-existing error, got %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("memory_save overwrote unreadable existing memory:\n%s", data)
	}
}

func TestMemorySave_ArchiveFailureDoesNotOverwriteExisting(t *testing.T) {
	home, cwd := memTestSetup(t)
	memDir := filepath.Join(home, ".yottacode", "memory", "user")
	if err := os.MkdirAll(memDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(memDir, "prefs.md")
	original := "---\nname: prefs\ntype: feedback\ndescription: original\ncreated: 2020-01-01T00:00:00Z\n---\nORIGINAL\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	// ArchivePrior needs <memory-dir>/.archive to be a directory. A file at
	// that path deterministically makes archiving fail without relying on
	// platform-specific permission semantics.
	if err := os.WriteFile(filepath.Join(memDir, memory.ArchiveDirName), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	_, err := tool.Execute(context.Background(), `{"scope":"user","type":"feedback","name":"prefs","description":"new","content":"NEW"}`)
	if err == nil || !strings.Contains(err.Error(), "archive prior") {
		t.Fatalf("expected archive-prior error, got %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("memory_save overwrote existing memory after archive failure:\n%s", data)
	}
}

func TestMemorySave_UpdateDeletesStaleVecWhenEmbeddingFails(t *testing.T) {
	home, cwd := memTestSetup(t)
	if runtime.GOOS == "windows" {
		t.Skip("test uses localhost port 1 failure timing that is platform-specific on Windows")
	}
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	if _, err := tool.Execute(context.Background(), `{"scope":"user","type":"reference","name":"topic","description":"old","content":"old body"}`); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	path := filepath.Join(home, ".yottacode", "memory", "user", "topic.md")
	vecPath := memory.VecPath(path)
	if err := memory.WriteVecWithModel(vecPath, []float32{1, 0, 0}, "nomic-embed-text"); err != nil {
		t.Fatalf("write vec: %v", err)
	}
	tool.Embedder = &memory.EmbedClient{BaseURL: "http://127.0.0.1:1", Model: "nomic-embed-text", Timeout: time.Second}
	out, err := tool.Execute(context.Background(), `{"scope":"user","type":"reference","name":"topic","description":"new","content":"new body"}`)
	if err != nil {
		t.Fatalf("update should save even when embedding fails: %v", err)
	}
	if !strings.Contains(out, "semantic index not updated") {
		t.Fatalf("expected semantic failure note, got %q", out)
	}
	if _, err := os.Stat(vecPath); !os.IsNotExist(err) {
		t.Fatalf("stale vec sidecar should be removed when updated memory cannot be re-embedded, stat err=%v", err)
	}
}

func TestMemorySave_CanceledContextCancelsEmbedding(t *testing.T) {
	home, cwd := memTestSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := &MemorySaveTool{
		Cwd: NewCwdRef(cwd),
		Embedder: &memory.EmbedClient{
			BaseURL: "http://127.0.0.1:1",
			Model:   "nomic-embed-text",
			Timeout: time.Second,
		},
	}
	out, err := tool.Execute(ctx, `{"scope":"user","type":"reference","name":"cancel-embed","description":"cancel embed","content":"body"}`)
	if err != nil {
		t.Fatalf("save should remain durable when embedding is canceled: %v", err)
	}
	if !strings.Contains(out, "semantic index not updated") {
		t.Fatalf("expected non-fatal semantic note, got %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".yottacode", "memory", "user", "cancel-embed.md")); err != nil {
		t.Fatalf("memory file should still be written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".yottacode", "memory", "user", "cancel-embed.vec")); !os.IsNotExist(err) {
		t.Fatalf("canceled embedding should not write vec sidecar, stat err=%v", err)
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
	userPath := filepath.Join(home, ".yottacode", "memory", "user", "prefs.md")
	if _, err := os.Stat(userPath); err != nil {
		t.Errorf("user-scope file missing at %s: %v", userPath, err)
	}
	projects := filepath.Join(home, ".yottacode", "memory", "projects")
	infos, err := os.ReadDir(projects)
	if err != nil {
		t.Fatalf("projects dir missing: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected exactly one project subdir under %s, got %d", projects, len(infos))
	}
	projectMemPath := filepath.Join(projects, infos[0].Name(), "layout.md")
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
		// An EMPTY type is no longer an error — it defaults to "note", see
		// TestMemorySave_QuickCaptureDefaults. A malformed one still is.
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

// P3 — a name+content quick capture must persist with sensible defaults
// rather than erroring on the three now-optional fields.
func TestMemorySave_QuickCaptureDefaults(t *testing.T) {
	home, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}

	out, err := tool.Execute(context.Background(),
		`{"name":"jwt-rotation","content":"## Key rotation\nTokens rotate every 90 days; the cron lives in ops/rotate.sh."}`)
	if err != nil {
		t.Fatalf("minimal save errored: %v", err)
	}
	if out == "" {
		t.Error("minimal save produced no result text")
	}

	// scope defaults to user, so it lands in the user tree.
	body, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "user", "jwt-rotation.md"))
	if err != nil {
		t.Fatalf("expected a user-scope memory file: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "type: note") {
		t.Errorf("omitted type should default to note; got:\n%s", got)
	}
	// The description is derived from the first non-empty content line with
	// the markdown heading marker stripped.
	if !strings.Contains(got, "description: Key rotation") {
		t.Errorf("description should be derived from the first content line; got:\n%s", got)
	}
	if !strings.Contains(got, "rotate every 90 days") {
		t.Errorf("body should be preserved; got:\n%s", got)
	}
}

// The full five-field form must keep working exactly as before — P3 widens
// what is accepted, it does not change what an explicit save does.
func TestMemorySave_FullFormUnchanged(t *testing.T) {
	home, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}

	if _, err := tool.Execute(context.Background(),
		`{"scope":"user","type":"feedback","name":"tabs","description":"prefers tabs","content":"User prefers tabs over spaces in Go."}`); err != nil {
		t.Fatalf("full-form save errored: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "user", "tabs.md"))
	if err != nil {
		t.Fatalf("read saved memory: %v", err)
	}
	got := string(body)
	for _, want := range []string{"type: feedback", "description: prefers tabs", "tabs over spaces"} {
		if !strings.Contains(got, want) {
			t.Errorf("full form lost %q; got:\n%s", want, got)
		}
	}
}

func TestDeriveDescription(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"first line", "the fact\nmore detail", "the fact"},
		{"skips blanks", "\n\n  \nthe fact", "the fact"},
		{"strips heading", "## Heading here\nbody", "Heading here"},
		{"strips bullet", "- a bullet point\nmore", "a bullet point"},
		{"strips quote", "> quoted line", "quoted line"},
		// Only the first line is taken, so CRLF input yields just that line
		// — the trailing \r is flattened away rather than leaking into the
		// index entry.
		{"crlf strips carriage return", "first\r\nsecond", "first"},
		{"empty content", "", ""},
		{"only markers", "###\n---\nreal text", "real text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveDescription(tc.in); got != tc.want {
				t.Errorf("deriveDescription(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// A long opening line is truncated so it can't dominate the
	// always-loaded MEMORY.md index.
	long := strings.Repeat("word ", 100)
	got := deriveDescription(long)
	if len([]rune(got)) > quickCaptureDescMaxRunes+1 {
		t.Errorf("derived description is %d runes, want <= %d", len([]rune(got)), quickCaptureDescMaxRunes+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated description should be elided; got %q", got)
	}
}

// The preview must show the values that will actually be written, or a quick
// capture would render as landing in scope="" type="".
func TestMemorySave_PreviewShowsDerivedValues(t *testing.T) {
	tool := &MemorySaveTool{Cwd: NewCwdRef(t.TempDir())}
	got := tool.PreviewCall(`{"name":"foo","content":"a fact"}`)
	if !strings.Contains(got, "scope=user") || !strings.Contains(got, "type=note") {
		t.Errorf("preview = %q, want derived scope=user and type=note", got)
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
		{"a  b__c--d", "a-b-c-d"},                  // runs of mixed separators collapse
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
	data, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "user", "queue-writes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "type: decision") {
		t.Errorf("custom type not stored normalized; got:\n%s", data)
	}
	// The custom type appears as its own group in the index.
	idx, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "user", "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(idx), "## decision") || !strings.Contains(string(idx), "queue-writes") {
		t.Errorf("custom-type memory missing from index; got:\n%s", idx)
	}
}

func TestMemorySave_UpdateReportsChangeSummary(t *testing.T) {
	_, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd), Source: memory.Source{Session: "session-a"}}
	if _, err := tool.Execute(context.Background(), `{"scope":"user","type":"user","name":"prefs","description":"old desc","content":"old body"}`); err != nil {
		t.Fatalf("first save: %v", err)
	}
	out, err := tool.Execute(context.Background(), `{"scope":"user","type":"feedback","name":"prefs","description":"new desc","content":"new body with more detail"}`)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	for _, want := range []string{"updated user memory", "previous version archived", "changes:", "type \"user\"→\"feedback\"", "description changed", "body changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("update output missing %q: %q", want, out)
		}
	}
}

func TestMemorySave_NoOpUpdateReportsNoChanges(t *testing.T) {
	_, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd), Source: memory.Source{Session: "session-a"}}
	args := `{"scope":"user","type":"user","name":"prefs","description":"same desc","content":"same body"}`
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatalf("first save: %v", err)
	}
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if !strings.Contains(out, "changes: none") {
		t.Errorf("no-op update should report no changes: %q", out)
	}
	if strings.Contains(out, "archived") {
		t.Errorf("no-op update should not archive: %q", out)
	}
}

func TestMemorySave_CreateDoesNotReportChangeSummary(t *testing.T) {
	_, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd)}
	out, err := tool.Execute(context.Background(), `{"scope":"user","type":"user","name":"prefs","description":"desc","content":"body"}`)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if strings.Contains(out, "changes:") {
		t.Errorf("create should not report update changes: %q", out)
	}
}

func TestMemorySave_WritesSourceMetadata(t *testing.T) {
	_, cwd := memTestSetup(t)
	tool := &MemorySaveTool{Cwd: NewCwdRef(cwd), Source: memory.Source{Session: "20260720-120000.000000", Turn: "2"}}
	if _, err := tool.Execute(context.Background(), `{"scope":"user","type":"reference","name":"source-test","description":"Has source","content":"Source-backed body."}`); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	path, err := memory.MemoryFilePath("user", "source-test", cwd)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fm, _, ok := memory.ParseFrontmatter(data)
	if !ok {
		t.Fatal("missing frontmatter")
	}
	if fm.SourceSession != "20260720-120000.000000" || fm.SourceTurn != "2" {
		t.Fatalf("source = %q/%q", fm.SourceSession, fm.SourceTurn)
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
	if _, err := os.Stat(filepath.Join(home, ".yottacode", "memory", "user", "a.md")); !os.IsNotExist(err) {
		t.Errorf("expected a.md gone, stat err = %v", err)
	}
	idx, err := os.ReadFile(filepath.Join(home, ".yottacode", "memory", "user", "MEMORY.md"))
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
	if _, err := os.Stat(filepath.Join(home, ".yottacode", "memory", "user", "MEMORY.md")); !os.IsNotExist(err) {
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

// TestMemorySave_DescriptionIsProactive pins the proactive framing of the
// tool description. The description is what the model reads at
// call-decision time, and the original reactive wording ("use this when
// the user states a durable preference") trained models to save only on
// explicit request — the exact under-saving bug the rewrite fixed. A
// regression back to reactive phrasing would silently kill proactive
// memory, so the framing is gated here.
func TestMemorySave_DescriptionIsProactive(t *testing.T) {
	desc := (&MemorySaveTool{}).Description()
	for _, want := range []string{"PROACTIVELY", "Don't wait", "When in doubt, save"} {
		if !strings.Contains(desc, want) {
			t.Errorf("memory_save description lost proactive framing: missing %q in %q", want, desc)
		}
	}
	if strings.Contains(desc, "when the user states") {
		t.Errorf("memory_save description regressed to reactive framing (%q)", desc)
	}
}
