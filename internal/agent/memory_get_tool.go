package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/yottadynamics/yottacode/internal/memory"
)

// MemoryGetTool returns the full, untruncated contents of one saved
// memory (frontmatter + body). memory_search only exposes a 300-char
// preview, so without this the agent has no way to see a memory's full
// body before memory_save overwrites the whole file — a blind
// read-modify-write that silently drops the unseen tail. This is the
// READ half of an agent-performed update; the model still decides what
// (if anything) to write back, so it stays within the agent-owned
// memory model.
type MemoryGetTool struct {
	Cwd *CwdRef
}

func (t *MemoryGetTool) Name() string { return "memory_get" }

func (t *MemoryGetTool) Description() string {
	return "Return the full, untruncated contents of one saved memory (frontmatter + body) by scope and name. Use this before updating a memory so you can preserve the parts you are not changing — memory_save overwrites the whole file."
}

func (t *MemoryGetTool) Schema() map[string]any {
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
				"description": "the memory's name (its filename without .md)",
			},
		},
		"required": []string{"scope", "name"},
	}
}

func (t *MemoryGetTool) RequiresApproval(string) bool { return false }
func (t *MemoryGetTool) ParallelSafe(string) bool     { return true }

func (t *MemoryGetTool) PreviewCall(argsJSON string) string {
	var a memoryGetArgs
	_ = json.Unmarshal([]byte(argsJSON), &a)
	return fmt.Sprintf("memory_get(scope=%s, name=%s)", a.Scope, a.Name)
}

type memoryGetArgs struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
}

func (t *MemoryGetTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var a memoryGetArgs
	if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
		return "", fmt.Errorf("memory_get: invalid args: %w", err)
	}
	path, err := memory.MemoryFilePath(a.Scope, a.Name, t.Cwd.Get())
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("memory_get: no %s memory named %q", a.Scope, a.Name)
		}
		return "", fmt.Errorf("memory_get: read %q: %w", path, err)
	}
	return string(data), nil
}

var (
	_ Tool             = (*MemoryGetTool)(nil)
	_ ParallelSafeTool = (*MemoryGetTool)(nil)
)
