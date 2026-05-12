package subagents

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

//go:embed builtins/*.md
var builtinFiles embed.FS

// LoadBuiltins parses every embedded *.md file under builtins/ and
// returns the resulting AgentConfig set. The Source is always "builtin"
// so warnings/diagnostics can distinguish embedded vs disk-loaded
// definitions. Parse errors here would be ship-blocking (the embedded
// files are part of the binary), so we panic — the test suite catches
// any bad frontmatter before release.
func LoadBuiltins() []AgentConfig {
	entries, err := fs.ReadDir(builtinFiles, "builtins")
	if err != nil {
		panic(fmt.Sprintf("subagents: read builtins dir: %v", err))
	}
	out := make([]AgentConfig, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.ToSlash(filepath.Join("builtins", e.Name()))
		body, err := fs.ReadFile(builtinFiles, path)
		if err != nil {
			panic(fmt.Sprintf("subagents: read builtin %q: %v", path, err))
		}
		cfg, err := ParseAgentFile(body)
		if err != nil {
			panic(fmt.Sprintf("subagents: parse builtin %q: %v", path, err))
		}
		cfg.Source = "builtin"
		out = append(out, cfg)
	}
	return out
}
