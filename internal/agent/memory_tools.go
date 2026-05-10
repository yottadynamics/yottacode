package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// MemorySaveTool persists a typed memory file under either the
// user-scope (~/.yottacode/memory/) or project-scope
// (~/.yottacode/projects/<slug>/memory/) directory and refreshes the
// MEMORY.md index for that scope. Replaces the post-turn extractor —
// the agent now decides in-band when something is worth remembering.
type MemorySaveTool struct {
	Cwd string
}

func (t *MemorySaveTool) Name() string { return "memory_save" }

func (t *MemorySaveTool) Description() string {
	return "Persist a typed memory file under the user-scope or project-scope memory directory. Overwrites silently if a file with the same name already exists. Updates the MEMORY.md index. Use this when the user states a durable preference, correction, or project fact you should remember in future sessions."
}

func (t *MemorySaveTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"user", "project"},
				"description": "user = applies across every project; project = scoped to this repo",
			},
			"type": map[string]any{
				"type":        "string",
				"enum":        []string{"user", "feedback", "project", "reference"},
				"description": "taxonomy: user preferences, feedback/corrections, project facts, or reference material",
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

func (t *MemorySaveTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var a memorySaveArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("memory_save: invalid args: %w", err)
	}
	if err := validateMemoryType(a.Type); err != nil {
		return "", err
	}
	if err := ensureMemoryDir(a.Scope, t.Cwd); err != nil {
		return "", err
	}
	path, err := memory.MemoryFilePath(a.Scope, a.Name, t.Cwd)
	if err != nil {
		return "", err
	}
	desc := flatten(a.Description)
	body := a.Content
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	full := memory.RenderFrontmatter(a.Name, a.Type, desc, time.Now()) + body
	if err := atomicWriteMemoryFile(path, []byte(full)); err != nil {
		return "", fmt.Errorf("memory_save: write %q: %w", path, err)
	}
	if err := memory.RegenerateMemoryIndex(a.Scope, t.Cwd); err != nil {
		return "", fmt.Errorf("memory_save: regenerate index: %w", err)
	}
	return fmt.Sprintf("saved %s memory %q", a.Scope, a.Name), nil
}

// MemoryForgetTool deletes a memory file and regenerates the scope's
// MEMORY.md index. Errors cleanly when the named memory does not
// exist — the agent can use that signal to learn the right names.
type MemoryForgetTool struct {
	Cwd string
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
	path, err := memory.MemoryFilePath(a.Scope, a.Name, t.Cwd)
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("memory_forget: no %s memory named %q", a.Scope, a.Name)
		}
		return "", fmt.Errorf("memory_forget: remove %q: %w", path, err)
	}
	if err := memory.RegenerateMemoryIndex(a.Scope, t.Cwd); err != nil {
		return "", fmt.Errorf("memory_forget: regenerate index: %w", err)
	}
	return fmt.Sprintf("forgot %s memory %q", a.Scope, a.Name), nil
}

func validateMemoryType(t string) error {
	switch t {
	case "user", "feedback", "project", "reference":
		return nil
	default:
		return fmt.Errorf("memory: invalid type %q (want one of: user, feedback, project, reference)", t)
	}
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

func atomicWriteMemoryFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Compile-time interface checks.
var (
	_ Tool             = (*MemorySaveTool)(nil)
	_ Tool             = (*MemoryForgetTool)(nil)
	_ ParallelSafeTool = (*MemorySaveTool)(nil)
	_ ParallelSafeTool = (*MemoryForgetTool)(nil)
)
