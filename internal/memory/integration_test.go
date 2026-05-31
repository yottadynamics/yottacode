package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// TestIntegration_BM25_StemAndSynonym verifies the full BM25 pipeline
// without any external dependencies — stemming + synonyms make
// conceptual queries find the right memories.
func TestIntegration_BM25_StemAndSynonym(t *testing.T) {
	entries := []MemoryEntry{
		{Name: "test-prefs", Type: "feedback", Scope: "project",
			Description: "testing preferences",
			Body:        "Prefer integration tests over mocks. Every shipped capability must have tests."},
		{Name: "deploy-process", Type: "project", Scope: "project",
			Description: "release process",
			Body:        "Release commits stay manual. Stage files and hand over commands to the user."},
		{Name: "auth-notes", Type: "reference", Scope: "project",
			Description: "authentication architecture",
			Body:        "GitHub Copilot auth uses device flow with OAuth tokens. Credentials stored in keyring."},
		{Name: "db-config", Type: "reference", Scope: "project",
			Description: "database configuration",
			Body:        "SQLite FTS5 for recall index. Porter stemmer tokenizer. BM25 ranking."},
		{Name: "ui-style", Type: "feedback", Scope: "user",
			Description: "no emoji in TUI",
			Body:        "Plain text for picker headers, banners, status lines. No emoji."},
	}

	cfg := config.RetrievalConfig{Enabled: true, TopK: 1, Strategy: "bm25"}

	tests := []struct {
		query    string
		wantName string
		why      string
	}{
		{"should I use fakes in tests", "test-prefs",
			"fakes→fake→mock (synonym), tests→test (stem)"},
		{"how do we ship this", "deploy-process",
			"ship→ship (synonym of deploy/release)"},
		{"login credentials", "auth-notes",
			"login→login (synonym of auth), credentials→credenti (synonym of auth group)"},
		{"database configuration", "db-config",
			"database→databas (stem), configuration→configur→config (synonym)"},
		{"styling the interface", "ui-style",
			"no direct keyword match, but style is in the name"},
		{"running the specs", "test-prefs",
			"running→run (stem), specs→spec (synonym of test)"},
		{"deploying releases", "deploy-process",
			"deploying→deploy (stem), releases→releas (stem+synonym)"},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			out := Select(entries, tc.query, cfg)
			if len(out) == 0 {
				t.Fatalf("no results for %q (expected %s: %s)", tc.query, tc.wantName, tc.why)
			}
			if out[0].Name != tc.wantName {
				t.Errorf("Select(%q) top result = %s, want %s (%s)",
					tc.query, out[0].Name, tc.wantName, tc.why)
			}
		})
	}
}

// TestIntegration_Semantic_Ollama verifies hybrid BM25+embedding
// retrieval against a live Ollama server. Skipped when Ollama is not
// available — run with: go test -run TestIntegration_Semantic -v
func TestIntegration_Semantic_Ollama(t *testing.T) {
	client := NewEmbedClient("", "nomic-embed-text")
	if !client.Available(context.Background()) {
		t.Skip("Ollama with nomic-embed-text not available — skipping semantic integration test")
	}

	dir := t.TempDir()
	entries := []MemoryEntry{
		{Name: "test-prefs", Type: "feedback", Scope: "project",
			Path:        filepath.Join(dir, "test-prefs.md"),
			Description: "testing preferences",
			Body:        "Prefer integration tests over mocks. Every shipped capability must have tests."},
		{Name: "deploy-process", Type: "project", Scope: "project",
			Path:        filepath.Join(dir, "deploy-process.md"),
			Description: "release process",
			Body:        "Release commits stay manual. Stage files and hand over commands to the user."},
		{Name: "auth-notes", Type: "reference", Scope: "project",
			Path:        filepath.Join(dir, "auth-notes.md"),
			Description: "authentication architecture",
			Body:        "GitHub Copilot auth uses device flow with OAuth tokens. Credentials stored in keyring."},
		{Name: "code-quality", Type: "feedback", Scope: "project",
			Path:        filepath.Join(dir, "code-quality.md"),
			Description: "code quality philosophy",
			Body:        "Clean code matters. Readability over cleverness. Functions should do one thing well."},
		{Name: "error-handling", Type: "feedback", Scope: "project",
			Path:        filepath.Join(dir, "error-handling.md"),
			Description: "error handling approach",
			Body:        "Soft failures everywhere. Errors surface as muted notices. Agent responsiveness is non-negotiable."},
	}

	// Generate embeddings for all entries
	ctx := context.Background()
	for _, e := range entries {
		os.WriteFile(e.Path, []byte(e.Body), 0o600)
		text := e.Name + " " + e.Description + " " + e.Body
		vec, err := client.Embed(ctx, text)
		if err != nil {
			t.Fatalf("embed %s: %v", e.Name, err)
		}
		// Write with the model header so entryCosine's model gate passes
		// and the cosine term actually contributes — otherwise this test
		// silently validates BM25 fallback only.
		if err := WriteVecWithModel(VecPath(e.Path), vec, client.Model); err != nil {
			t.Fatalf("write vec %s: %v", e.Name, err)
		}
	}

	cfg := config.RetrievalConfig{Enabled: true, TopK: 2, Strategy: "semantic"}

	tests := []struct {
		query     string
		wantTop   string
		why       string
	}{
		{"how should I think about error handling philosophy",
			"error-handling",
			"semantic similarity should surface error-handling even without exact keyword overlap"},
		{"what are the security best practices for authentication",
			"auth-notes",
			"semantic: security + authentication → auth-notes"},
		{"what testing strategy should we use for new features",
			"test-prefs",
			"semantic: testing strategy → testing preferences"},
	}

	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			out := SelectWithEmbeddings(entries, tc.query, cfg, client)
			if len(out) == 0 {
				t.Fatalf("no results for %q", tc.query)
			}
			if out[0].Name != tc.wantTop {
				names := make([]string, len(out))
				for i, e := range out {
					names[i] = e.Name
				}
				t.Errorf("SelectWithEmbeddings(%q) top = %s, want %s (%s)\n  got: %v",
					tc.query, out[0].Name, tc.wantTop, tc.why, names)
			}
		})
	}
}
