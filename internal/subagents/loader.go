package subagents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadResult is what LoadAll returns: a deduplicated, ordered slice of
// configs (project > global > builtin precedence) plus a slice of
// human-readable warnings the caller can surface to the user. Warnings
// are non-fatal — a single malformed file shouldn't block startup.
type LoadResult struct {
	Configs  []AgentConfig
	Warnings []string
}

// LoadAll resolves agent definitions from all three sources and merges
// them with project > global > builtin precedence. validToolNames is
// the set of names the live tool registry exposes — any agent that
// references a tool outside this set emits a warning and that name is
// silently dropped from the allowlist. Pass nil to skip allowlist
// validation entirely (used by tests that don't care).
func LoadAll(cwd string, validToolNames map[string]bool) (LoadResult, error) {
	var res LoadResult
	seen := map[string]int{} // name -> index in res.Configs (so project can overwrite)

	add := func(cfg AgentConfig) {
		if idx, ok := seen[cfg.Name]; ok {
			res.Configs[idx] = cfg
			return
		}
		seen[cfg.Name] = len(res.Configs)
		res.Configs = append(res.Configs, cfg)
	}

	// Lowest precedence first so later sources overwrite earlier ones.
	for _, cfg := range LoadBuiltins() {
		add(cfg)
	}

	userDir, err := UserAgentsDir()
	if err == nil {
		ucfgs, uwarn := loadDir(userDir, "global")
		res.Warnings = append(res.Warnings, uwarn...)
		for _, c := range ucfgs {
			add(c)
		}
	}

	projectDir := ProjectAgentsDir(cwd)
	pcfgs, pwarn := loadDir(projectDir, "project")
	res.Warnings = append(res.Warnings, pwarn...)
	for _, c := range pcfgs {
		add(c)
	}

	if validToolNames != nil {
		res.Warnings = append(res.Warnings, validateToolAllowlists(res.Configs, validToolNames)...)
	}

	sort.Slice(res.Configs, func(i, j int) bool { return res.Configs[i].Name < res.Configs[j].Name })
	return res, nil
}

// loadDir reads every *.md under dir and returns the parsed configs +
// warnings for unparsable files. Missing dir is a non-error (a user
// without any custom agents shouldn't see noise on startup).
func loadDir(dir, source string) ([]AgentConfig, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("read %s agents dir %q: %v", source, dir, err)}
	}
	var out []AgentConfig
	var warnings []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("read %s agent %q: %v", source, path, err))
			continue
		}
		cfg, err := ParseAgentFile(body)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("parse %s agent %q: %v", source, path, err))
			continue
		}
		cfg.Source = source
		cfg.SourcePath = path
		out = append(out, cfg)
	}
	return out, warnings
}

// validateToolAllowlists drops unknown tool names from each config's
// allowlist and produces a warning per dropped entry. Unknown names
// don't fail the load — the agent is still useful with the rest of
// its allowlist (or a degraded inherit-all if everything dropped).
func validateToolAllowlists(configs []AgentConfig, valid map[string]bool) []string {
	var warnings []string
	for i := range configs {
		if len(configs[i].Tools) == 0 {
			continue
		}
		kept := configs[i].Tools[:0]
		for _, name := range configs[i].Tools {
			if valid[name] {
				kept = append(kept, name)
				continue
			}
			warnings = append(warnings, fmt.Sprintf(
				"agent %q (%s) references unknown tool %q — dropping from allowlist",
				configs[i].Name, configs[i].Source, name))
		}
		configs[i].Tools = kept
	}
	return warnings
}

// Find returns the named config or nil. Case-sensitive: agent names
// are validated against agentNamePattern so casing is part of identity.
func Find(configs []AgentConfig, name string) *AgentConfig {
	for i := range configs {
		if configs[i].Name == name {
			return &configs[i]
		}
	}
	return nil
}
