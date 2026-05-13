package agent

import (
	"path/filepath"
	"sort"
	"testing"
)

// Mutators are validated together because the surface is intentionally
// tiny — one method per tool — and a single table makes "all snapshot
// the right path(s)" easy to read at a glance.

func TestMutators_ReturnExpectedPaths(t *testing.T) {
	cwd := "/work"

	cases := []struct {
		name     string
		tool     Mutator
		argsJSON string
		want     []string
	}{
		{
			name:     "WriteFileTool",
			tool:     &WriteFileTool{Cwd: cwd},
			argsJSON: `{"path":"a/b.go","content":"x"}`,
			want:     []string{filepath.Join(cwd, "a/b.go")},
		},
		{
			name:     "EditFileTool",
			tool:     &EditFileTool{Cwd: cwd},
			argsJSON: `{"path":"a/b.go","old_string":"x","new_string":"y"}`,
			want:     []string{filepath.Join(cwd, "a/b.go")},
		},
		{
			name:     "DeleteFileTool",
			tool:     &DeleteFileTool{Cwd: cwd},
			argsJSON: `{"path":"old.txt"}`,
			want:     []string{filepath.Join(cwd, "old.txt")},
		},
		{
			name:     "MoveFileTool_BothEnds",
			tool:     &MoveFileTool{Cwd: cwd},
			argsJSON: `{"src":"a.txt","dst":"b.txt"}`,
			want: []string{
				filepath.Join(cwd, "a.txt"),
				filepath.Join(cwd, "b.txt"),
			},
		},
		{
			name:     "CopyFileTool_DstOnly",
			tool:     &CopyFileTool{Cwd: cwd},
			argsJSON: `{"src":"a.txt","dst":"b.txt"}`,
			want:     []string{filepath.Join(cwd, "b.txt")},
		},
		{
			name: "ApplyDiffTool_MultiFile",
			tool: &ApplyDiffTool{Cwd: cwd},
			argsJSON: `{"diff":"diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@\n-old\n+new\ndiff --git a/y.go b/y.go\n--- a/y.go\n+++ b/y.go\n@@\n-old\n+new\n"}`,
			want: []string{
				filepath.Join(cwd, "x.go"),
				filepath.Join(cwd, "y.go"),
			},
		},
		{
			name:     "WriteFileTool_EmptyPathReturnsNil",
			tool:     &WriteFileTool{Cwd: cwd},
			argsJSON: `{"path":"","content":"x"}`,
			want:     nil,
		},
		{
			name:     "EditFileTool_InvalidJSONReturnsNil",
			tool:     &EditFileTool{Cwd: cwd},
			argsJSON: `not json`,
			want:     nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.tool.PathsToSnapshot(cwd, c.argsJSON)
			// Sort to make equality robust to ordering choices —
			// callers don't rely on order, since checkpoint
			// snapshotting is content-addressed.
			sort.Strings(got)
			want := append([]string(nil), c.want...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
				}
			}
		})
	}
}

func TestMutators_BashAndGitToolsDoNotImplementMutator(t *testing.T) {
	// Defensive — if someone adds Mutator to RunBashTool or a git_*
	// tool by accident, the checkpoint surface diverges from Claude's
	// /rewind in a way that's hard to spot in review. Make the
	// invariant explicit. (We assert by type assertion, not by
	// instantiating the actual tool, so test isn't tied to construction
	// requirements.)
	type sentinelInterface interface {
		PathsToSnapshot(cwd, argsJSON string) []string
	}

	// Build a minimal list of tools that MUST stay non-mutator. The
	// list mirrors the documented "bash mutations are not tracked"
	// caveat. Adding to this list requires updating docs + picker
	// footer, so test failures here are a load-bearing signal.
	type guard struct {
		name string
		tool Tool
	}
	guards := []guard{
		{name: "RunBashTool", tool: &RunBashTool{}},
		{name: "GitCheckpointTool", tool: &GitCheckpointTool{}},
		{name: "RollbackTool", tool: &RollbackTool{}},
	}
	for _, g := range guards {
		if _, ok := g.tool.(sentinelInterface); ok {
			t.Errorf("%s implements Mutator — bash & git mutations must remain untracked to mirror Claude /rewind", g.name)
		}
	}
}
