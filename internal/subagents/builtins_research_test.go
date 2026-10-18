package subagents

import "testing"

func TestParseAgentFile_MaxIterations(t *testing.T) {
	parse := func(extra string) AgentConfig {
		t.Helper()
		cfg, err := ParseAgentFile([]byte("---\nname: x\ndescription: d\n" + extra + "---\nbody\n"))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if got := parse("max_iterations: 25\n").MaxIterations; got != 25 {
		t.Errorf("max_iterations: 25 → %d", got)
	}
	for _, bad := range []string{"max_iterations: 0\n", "max_iterations: -4\n", "max_iterations: lots\n", ""} {
		if got := parse(bad).MaxIterations; got != 0 {
			t.Errorf("%q should leave the default (0), got %d", bad, got)
		}
	}
}

// TestLoadBuiltins_ResearchRoster pins the four agents the deep_research
// tool drives. They run unattended and in parallel, so none may carry a
// write or shell tool, and the two that touch the web must be able to search
// and fetch.
func TestLoadBuiltins_ResearchRoster(t *testing.T) {
	byName := map[string]AgentConfig{}
	for _, c := range LoadBuiltins() {
		byName[c.Name] = c
	}
	forbidden := []string{
		"write_file", "edit_file", "apply_hashline", "apply_diff", "mkdir",
		"copy_file", "move_file", "delete_file", "run_bash", "run_tests",
	}
	for _, name := range []string{"research-planner", "researcher", "research-verifier", "research-synthesizer"} {
		c, ok := byName[name]
		if !ok {
			t.Fatalf("builtin %q missing", name)
		}
		if len(c.Tools) == 0 {
			t.Errorf("%s: must declare an explicit tools list (empty means inherit-all)", name)
		}
		if c.Prompt == "" {
			t.Errorf("%s: empty prompt", name)
		}
		for _, tool := range c.Tools {
			for _, bad := range forbidden {
				if tool == bad {
					t.Errorf("%s: carries mutating tool %q", name, tool)
				}
			}
		}
	}
	// The two web agents are the expensive ones: each carries a lower
	// iteration budget than the session default so a wandering run stops.
	for _, name := range []string{"researcher", "research-verifier"} {
		if got := byName[name].MaxIterations; got != 30 {
			t.Errorf("%s: MaxIterations = %d, want 30", name, got)
		}
	}
	// Least privilege: these two read arbitrary, untrusted web pages and hold
	// network egress, so they must not also hold any local-file tool. A hostile
	// page could otherwise steer an unattended agent into reading a local secret
	// and leaking it into a claim or a fetch_url query string.
	localRead := []string{"read_file", "read_many_files", "grep", "glob", "list_dir", "list_project_structure", "read_document", "search_document", "session_recall", "memory_search"}
	for _, name := range []string{"researcher", "research-verifier"} {
		for _, tool := range byName[name].Tools {
			for _, bad := range localRead {
				if tool == bad {
					t.Errorf("%s holds local-read tool %q alongside web access", name, tool)
				}
			}
		}
	}
	for _, name := range []string{"researcher", "research-verifier"} {
		have := map[string]bool{}
		for _, tool := range byName[name].Tools {
			have[tool] = true
		}
		for _, want := range []string{"web_search", "fetch_url"} {
			if !have[want] {
				t.Errorf("%s: missing %s", name, want)
			}
		}
	}
}
