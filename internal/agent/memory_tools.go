package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// memSaveLocks serializes the read→archive→write→index sequence per
// memory-file path across the whole process. memory_save is not
// ParallelSafe, but that only serializes calls within ONE agent loop's
// parallel batch — the same *MemorySaveTool pointer is shared into
// background subagents (which run in detached goroutines), so two
// concurrent saves to the same name would otherwise both read the same
// prior, both archive only that prior, and the last write would win,
// silently losing the other writer's NEW content. A per-path lock makes
// the sequence serial so each writer archives the previous writer's
// content — nothing is silently lost. (Cross-process concurrent saves to
// the same name remain a documented gap; an OS file lock would close it.)
var memSaveLocks sync.Map // abs path -> *sync.Mutex

func lockMemoryPath(path string) func() {
	mu, _ := memSaveLocks.LoadOrStore(path, &sync.Mutex{})
	m := mu.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// MemorySaveTool persists a typed memory file under either the
// user-scope (~/.yottacode/memory/user/) or project-scope
// (~/.yottacode/memory/projects/<slug>/) directory and refreshes the
// MEMORY.md index for that scope. Replaces the post-turn extractor —
// the agent now decides in-band when something is worth remembering.
//
// When Embedder is set, a vector sidecar (.vec) is generated alongside
// the memory file for semantic retrieval.
type MemorySaveTool struct {
	Cwd      *CwdRef
	Embedder *memory.EmbedClient
}

func (t *MemorySaveTool) Name() string { return "memory_save" }

func (t *MemorySaveTool) Description() string {
	return "Persist a typed memory file under the user-scope or project-scope memory directory. Use this PROACTIVELY whenever you learn something durable — a user preference or correction, a validated approach, a project fact you'd otherwise re-derive, a hard-won debugging insight, a recurring pattern. Don't wait for the user to say \"remember this\": if future-you would want it in a later session, save it now, before the task moves on. If a memory with the same name already exists this updates it in place (the prior version is archived, never silently lost) — use memory_get first if you want to preserve parts you aren't changing. Updates the MEMORY.md index."
}

func (t *MemorySaveTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"scope": map[string]any{
				"type": "string",
				"enum": []string{"user", "project"},
				// Steering lives HERE, not only in the system prompt: the
				// schema is what the model reads at the moment it fills the
				// enum, and mid-repo work pulls everything toward "project"
				// otherwise. Wording mirrors the prompt's scope-selection
				// section on purpose — paraphrase drift teaches models to
				// hedge. Pinned by TestMemorySaveTool_ScopeSteeringPinned.
				"description": "user = loaded in EVERY project (DEFAULT — most learnings about how the person works, thinks, and prefers are portable); project = ONLY for facts meaningless outside this repo. The test: would this help in a completely different repo? If yes → user",
			},
			"type": map[string]any{
				"type":        "string",
				"description": "a short lowercase label categorizing the memory. Conventional labels — user (preferences), feedback (corrections), project (repo facts), reference (material to look back at) — group together in the index; use one when it fits, or your own short label (e.g. \"decision\", \"gotcha\") when none do.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "kebab-case slug, becomes the filename (a-z, 0-9, hyphen)",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "one-line summary shown in the MEMORY.md index",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "the memory body in markdown — concise, written for a future agent",
			},
		},
		"required": []string{"scope", "type", "name", "description", "content"},
	}
}

func (t *MemorySaveTool) RequiresApproval(string) bool { return false }
func (t *MemorySaveTool) ParallelSafe(string) bool     { return false }

func (t *MemorySaveTool) PreviewCall(argsJSON string) string {
	var a memorySaveArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("memory_save(scope=%s, type=%s, name=%s)", a.Scope, a.Type, a.Name)
}

type memorySaveArgs struct {
	Scope       string `json:"scope"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

// portableMemoryTypes are the conventional memory types that are about
// the person rather than the repo — the canonical user-scope material
// (mirrors the scope guidance in prompt.go and the schema description).
// The scope-check reflection in Execute fires when one of these is
// filed project-scope; extend this set if a new portable convention
// joins the type vocabulary.
var portableMemoryTypes = map[string]bool{
	"user":     true,
	"feedback": true,
}

func (t *MemorySaveTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var a memorySaveArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("memory_save: invalid args: %w", err)
	}
	memType, err := validateMemoryType(a.Type)
	if err != nil {
		return "", err
	}
	if err := ensureMemoryDir(a.Scope, t.Cwd.Get()); err != nil {
		return "", err
	}
	path, err := memory.MemoryFilePath(a.Scope, a.Name, t.Cwd.Get())
	if err != nil {
		return "", err
	}
	// Serialize the whole read→archive→write→index sequence for this path
	// so two concurrent saves to the same name can't lose-update each
	// other (see memSaveLocks).
	unlock := lockMemoryPath(path)
	defer unlock()
	// Read-before-write: recover the original creation time and detect an
	// overwrite. The memory store's whole promise is that the agent keeps
	// what it learned, so a save must never SILENTLY destroy a different
	// memory that happens to reuse this name (the model picks names on its
	// own). We preserve `created` across updates and archive the prior
	// version on any content change — recoverable, never lost.
	existing, _ := os.ReadFile(path) // nil when absent
	created := time.Now()
	verb := "created"
	if len(existing) > 0 {
		verb = "updated"
		if fm, _, ok := memory.ParseFrontmatter(existing); ok && fm.Created != "" {
			if ts, perr := time.Parse(time.RFC3339, fm.Created); perr == nil {
				created = ts
			}
		}
	}
	desc := flatten(a.Description)
	body := a.Content
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	full := memory.RenderFrontmatter(a.Name, memType, desc, created) + body

	archived := false
	if len(existing) > 0 && string(existing) != full {
		// Content actually changes — archive the prior version first.
		// Soft on failure: better to complete the save the agent asked
		// for than to block it because archiving hiccupped.
		if ap, aerr := memory.ArchivePrior(path, fmt.Sprintf("%d", time.Now().UnixNano())); aerr == nil && ap != "" {
			archived = true
		}
	}
	if err := memory.AtomicWrite(path, []byte(full), 0o600); err != nil {
		return "", fmt.Errorf("memory_save: write %q: %w", path, err)
	}
	if err := memory.RegenerateMemoryIndex(a.Scope, t.Cwd.Get()); err != nil {
		return "", fmt.Errorf("memory_save: regenerate index: %w", err)
	}
	note := ""
	if t.Embedder != nil {
		text := a.Name + " " + a.Description + " " + a.Content
		vec, err := t.Embedder.Embed(context.Background(), text)
		switch {
		case err != nil:
			// The memory is durably saved; only its semantic vector is
			// missing, so retrieval falls back to BM25 for this entry.
			// Surface it (instead of silently swallowing) so the model /
			// user can recover once Ollama is back.
			note = " (note: semantic index not updated — embedding unavailable; run `yottacode memory reindex` once Ollama is reachable)"
		default:
			if werr := memory.WriteVecWithModel(memory.VecPath(path), vec, t.Embedder.Model); werr != nil {
				note = " (note: semantic vector not written: " + werr.Error() + ")"
			}
		}
	}
	msg := fmt.Sprintf("%s %s memory %q", verb, a.Scope, a.Name)
	if archived {
		msg += " (previous version archived to .archive/)"
	}
	// Scope reflection: a portable-typed memory saved to project scope
	// is usually a portability miss — preferences and corrections
	// travel with the person, not the repo. Surface the mismatch in the
	// tool result, the one moment correction is a single tool call away,
	// and leave the call to the agent (informational only; the harness
	// never performs a memory op itself). The trigger is narrow on
	// purpose: it must never fire on legitimately repo-bound
	// type=project facts or free-form labels, or the model learns to
	// ignore it.
	if a.Scope == "project" && portableMemoryTypes[memType] {
		msg += fmt.Sprintf(" (scope check: a %s-typed memory is usually portable across repos — if it isn't specific to this one, re-save it with scope=user and memory_forget the project-scope copy)", memType)
	}
	return msg + note, nil
}

// MemoryForgetTool deletes a memory file and regenerates the scope's
// MEMORY.md index. Errors cleanly when the named memory does not
// exist — the agent can use that signal to learn the right names.
type MemoryForgetTool struct {
	Cwd *CwdRef
}

func (t *MemoryForgetTool) Name() string { return "memory_forget" }

func (t *MemoryForgetTool) Description() string {
	return "Delete a previously-saved memory file and refresh the MEMORY.md index. Use this when a stored memory is wrong, stale, or no longer useful. Errors when the named memory doesn't exist."
}

func (t *MemoryForgetTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"user", "project"},
				"description": "the scope the memory was saved under",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "the memory's name (matches its filename without .md)",
			},
		},
		"required": []string{"scope", "name"},
	}
}

func (t *MemoryForgetTool) RequiresApproval(string) bool { return false }
func (t *MemoryForgetTool) ParallelSafe(string) bool     { return false }

func (t *MemoryForgetTool) PreviewCall(argsJSON string) string {
	var a memoryForgetArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("memory_forget(scope=%s, name=%s)", a.Scope, a.Name)
}

type memoryForgetArgs struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
}

func (t *MemoryForgetTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var a memoryForgetArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("memory_forget: invalid args: %w", err)
	}
	path, err := memory.MemoryFilePath(a.Scope, a.Name, t.Cwd.Get())
	if err != nil {
		return "", err
	}
	// Hold the same per-path lock memory_save takes: forget shares
	// RegenerateMemoryIndex, and both tools' pointers are reachable from
	// background subagents (detached goroutines). Without this, a
	// concurrent save+forget (or two forgets) on the same name can
	// interleave the remove and the index regeneration, leaving MEMORY.md
	// transiently listing a deleted memory or dropping a live one.
	unlock := lockMemoryPath(path)
	defer unlock()
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("memory_forget: no %s memory named %q", a.Scope, a.Name)
		}
		return "", fmt.Errorf("memory_forget: remove %q: %w", path, err)
	}
	memory.DeleteVec(path)
	if err := memory.RegenerateMemoryIndex(a.Scope, t.Cwd.Get()); err != nil {
		return "", fmt.Errorf("memory_forget: regenerate index: %w", err)
	}
	return fmt.Sprintf("forgot %s memory %q", a.Scope, a.Name), nil
}

// memoryTypeSeparators matches a run of the three interchangeable word
// separators (space, underscore, hyphen). validateMemoryType collapses
// each run to a single canonical hyphen so labels that differ only by
// separator — "api shape", "api_shape", "api-shape" — store identically
// and group as ONE "## <type>" section in MEMORY.md instead of three.
var memoryTypeSeparators = regexp.MustCompile(`[ _-]+`)

// memoryTypePattern bounds the CANONICALIZED type label to a short,
// index-safe token. The type is stored as a frontmatter value, rendered
// as a "## <type>" group header in MEMORY.md, and shown as a "[<type>]"
// tag in the prompt, so it must stay single-line and free of characters
// that would break those surfaces. After canonicalization a valid label
// is lowercase letters/digits joined by single hyphens, starting
// alphanumeric, ≤32 chars.
var memoryTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// validateMemoryType normalizes a memory type and checks it's a usable
// label. Normalization is lowercase + trim, then any run of
// space/underscore/hyphen collapses to a single hyphen (ends trimmed) so
// separator variants don't fragment the index into near-duplicate groups.
// Types are FREE-FORM: user / feedback / project / reference are
// conventions that group together in the index, not a closed set — the
// agent may coin its own short label when none fit. Returns the canonical
// value to store.
func validateMemoryType(t string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(t))
	if norm == "" {
		return "", fmt.Errorf("memory: type is required (e.g. user, feedback, project, reference, or your own short label)")
	}
	// Canonicalize separators so "api shape" / "api_shape" / "api-shape"
	// all become "api-shape" and group as one type rather than three.
	norm = strings.Trim(memoryTypeSeparators.ReplaceAllString(norm, "-"), "-")
	if !memoryTypePattern.MatchString(norm) {
		return "", fmt.Errorf("memory: invalid type %q (use a short label: lowercase letters and digits, words separated by spaces, hyphens or underscores; max 32 chars)", norm)
	}
	return norm, nil
}

func ensureMemoryDir(scope, cwd string) error {
	switch scope {
	case "user":
		_, err := memory.EnsureUserMemoryDir()
		return err
	case "project":
		_, err := memory.EnsureProjectMemoryDir(cwd)
		return err
	default:
		return fmt.Errorf("memory: invalid scope %q (want \"user\" or \"project\")", scope)
	}
}

// flatten collapses every embedded newline so a multi-line value
// fits on one frontmatter line. Memories themselves can be
// multi-line; the description shown in the index cannot.
func flatten(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

// Compile-time interface checks.
var (
	_ Tool             = (*MemorySaveTool)(nil)
	_ Tool             = (*MemoryForgetTool)(nil)
	_ ParallelSafeTool = (*MemorySaveTool)(nil)
	_ ParallelSafeTool = (*MemoryForgetTool)(nil)
)
