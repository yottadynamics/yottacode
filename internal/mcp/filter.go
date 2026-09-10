package mcp

import "path/filepath"

// FilterTools narrows a server's advertised catalog to the include/exclude
// globs from its config.MCPServer entry. Applied after tools/list (or
// RefreshTools), before registry insertion and before the C0 safety
// floor's description fencing, so a filtered-out tool never reaches the
// model at all.
//
// include, when non-empty, keeps only names matching at least one glob;
// an empty include keeps everything. exclude then drops any name matching
// at least one of its globs, evaluated after include. Globs use
// path.Match syntax ("get_*", "create_pull_request") — the same shape the
// config docs advertise.
//
// Returns the kept tools (order preserved) and how many were dropped.
func FilterTools(tools []ToolDescriptor, include, exclude []string) (kept []ToolDescriptor, hidden int) {
	if len(include) == 0 && len(exclude) == 0 {
		return tools, 0
	}
	kept = make([]ToolDescriptor, 0, len(tools))
	for _, td := range tools {
		if len(include) > 0 && !anyGlobMatch(include, td.Name) {
			hidden++
			continue
		}
		if anyGlobMatch(exclude, td.Name) {
			hidden++
			continue
		}
		kept = append(kept, td)
	}
	return kept, hidden
}

func anyGlobMatch(globs []string, name string) bool {
	for _, g := range globs {
		if ok, err := filepath.Match(g, name); err == nil && ok {
			return true
		}
	}
	return false
}
