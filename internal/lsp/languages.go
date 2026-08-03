// Package lsp provides the small language-server bridge used by yottacode's
// experimental code-intelligence tools. It intentionally owns only discovery,
// JSON-RPC framing, and result shaping; installation and long-lived IDE-style
// server management stay outside the agent.
package lsp

import (
	"context"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Language describes one supported language-server family. Each entry includes
// a user-facing install hint and exact install command so unavailable servers
// degrade into actionable, approval-gated setup guidance instead of a bare
// "command not found" error.
type Language struct {
	ID             string
	Name           string
	Extensions     []string
	Command        []string
	InstallHint    string
	InstallCommand string
}

// Languages returns the supported language-server families in deterministic
// order. The first Command element is the binary checked on PATH and executed
// directly without a shell.
func Languages() []Language {
	langs := []Language{
		{
			ID:         "go",
			Name:       "Go",
			Extensions: []string{".go"},
			Command:    []string{"gopls"},
			InstallHint: "Install gopls: go install golang.org/x/tools/gopls@latest " +
				"and ensure $(go env GOPATH)/bin is on PATH.",
			InstallCommand: "go install golang.org/x/tools/gopls@latest",
		},
		{
			ID:             "typescript",
			Name:           "TypeScript/JavaScript",
			Extensions:     []string{".ts", ".tsx", ".js", ".jsx"},
			Command:        []string{"typescript-language-server", "--stdio"},
			InstallHint:    "Install TypeScript language server: npm install -g typescript typescript-language-server.",
			InstallCommand: "npm install -g typescript typescript-language-server",
		},
		{
			ID:             "python",
			Name:           "Python",
			Extensions:     []string{".py"},
			Command:        []string{"pyright-langserver", "--stdio"},
			InstallHint:    "Install Pyright language server: npm install -g pyright.",
			InstallCommand: "npm install -g pyright",
		},
		{
			ID:             "rust",
			Name:           "Rust",
			Extensions:     []string{".rs"},
			Command:        []string{"rust-analyzer"},
			InstallHint:    "Install rust-analyzer through rustup, your package manager, or https://rust-analyzer.github.io/.",
			InstallCommand: "rustup component add rust-analyzer",
		},
	}
	return langs
}

// ApplyOverrides returns a copy of lang with any user-configured command
// override applied. Empty overrides leave the built-in command intact.
func ApplyOverrides(lang Language, overrides map[string][]string) Language {
	if len(overrides) == 0 {
		return lang
	}
	if cmd := overrides[lang.ID]; len(cmd) > 0 && strings.TrimSpace(cmd[0]) != "" {
		lang.Command = append([]string(nil), cmd...)
	}
	return lang
}

// ApplyOverridesToDetected returns detected languages with command overrides
// applied while preserving file counts and availability recalculated from the
// overridden binary.
func ApplyOverridesToDetected(in []DetectedLanguage, overrides map[string][]string) []DetectedLanguage {
	out := make([]DetectedLanguage, 0, len(in))
	for _, d := range in {
		lang := ApplyOverrides(d.Language, overrides)
		out = append(out, DetectedLanguage{Language: lang, FilesAvailable: d.FilesAvailable, ServerAvailable: ServerAvailable(lang)})
	}
	return out
}

// ServerCommandOverrides is the minimal config shape consumed by the LSP tool
// registration path. It avoids importing the config package into lsp/agent.
type ServerCommandOverrides map[string][]string

// ResolveFile maps a source file path to the language-server family that owns
// its extension. Matching is case-insensitive because editors commonly open
// generated or copied files with odd extension casing.
func ResolveFile(path string) (Language, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return Language{}, false
	}
	for _, lang := range Languages() {
		for _, candidate := range lang.Extensions {
			if ext == candidate {
				return lang, true
			}
		}
	}
	return Language{}, false
}

// ResolveID returns a language by its stable ID.
func ResolveID(id string) (Language, bool) {
	id = strings.TrimSpace(strings.ToLower(id))
	for _, lang := range Languages() {
		if lang.ID == id {
			return lang, true
		}
	}
	return Language{}, false
}

// ServerAvailable reports whether a language server's binary is present on
// PATH. A language with no command is treated as unavailable so future entries
// cannot panic by indexing an empty command.
func ServerAvailable(lang Language) bool {
	if len(lang.Command) == 0 || strings.TrimSpace(lang.Command[0]) == "" {
		return false
	}
	_, err := exec.LookPath(lang.Command[0])
	return err == nil
}

// DetectedLanguage combines workspace detection with server readiness.
type DetectedLanguage struct {
	Language
	FilesAvailable  int
	ServerAvailable bool
}

// DetectWorkspace scans root for supported source files and reports the
// language families present. The scan is bounded and skips heavy directories so
// lsp_status is cheap enough to run as a normal read-only tool.
func DetectWorkspace(ctx context.Context, root string, maxFiles int) ([]DetectedLanguage, error) {
	if maxFiles <= 0 {
		maxFiles = 2000
	}
	counts := map[string]int{}
	seen := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if shouldSkipDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		seen++
		if seen > maxFiles {
			return fs.SkipAll
		}
		if lang, ok := ResolveFile(path); ok {
			counts[lang.ID]++
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return nil, err
	}
	out := make([]DetectedLanguage, 0, len(counts))
	for _, lang := range Languages() {
		if n := counts[lang.ID]; n > 0 {
			out = append(out, DetectedLanguage{
				Language:        lang,
				FilesAvailable:  n,
				ServerAvailable: ServerAvailable(lang),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".yottacode", "node_modules", "vendor", "target", "build", "dist":
		return true
	default:
		return strings.HasPrefix(name, ".")
	}
}
