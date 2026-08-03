package agent

import (
	"strings"
	"testing"
)

// metadataCase bundles the assertions every Tool implementation has to pass.
// Schema must be a JSON-schema object with a properties map; Name and
// Description must be non-empty; PreviewCall must produce something useful
// for a given args fixture.
type metadataCase struct {
	tool                 Tool
	wantName             string
	wantApproval         bool
	approvalArgsJSON     string // empty = use "{}" as default
	previewArgsJSON      string
	wantPreviewSubstring string
	wantSchemaRequired   []string // properties that must be in the "required" array
}

func TestTools_Metadata(t *testing.T) {
	cwd := t.TempDir()
	cases := []metadataCase{
		{
			tool:                 &ReadFileTool{Cwd: NewCwdRef(cwd)},
			wantName:             "read_file",
			wantApproval:         false,
			previewArgsJSON:      `{"path":"x.go"}`,
			wantPreviewSubstring: "x.go",
			wantSchemaRequired:   []string{"path"},
		},
		{
			tool:                 &WriteFileTool{Cwd: NewCwdRef(cwd), WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}},
			wantName:             "write_file",
			wantApproval:         true,
			previewArgsJSON:      `{"path":"x.go","content":"hi"}`,
			wantPreviewSubstring: "x.go",
			wantSchemaRequired:   []string{"path", "content"},
		},
		{
			tool:                 &EditFileTool{Cwd: NewCwdRef(cwd), WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}},
			wantName:             "edit_file",
			wantApproval:         true,
			previewArgsJSON:      `{"path":"x.go","old_string":"foo","new_string":"bar"}`,
			wantPreviewSubstring: "x.go",
			wantSchemaRequired:   []string{"path", "old_string", "new_string"},
		},
		{
			tool:                 &ReadManyFilesTool{Cwd: NewCwdRef(cwd)},
			wantName:             "read_many_files",
			wantApproval:         false,
			previewArgsJSON:      `{"paths":["a.go","b.go"]}`,
			wantPreviewSubstring: "2 paths",
			wantSchemaRequired:   []string{"paths"},
		},
		{
			tool:                 &MkdirTool{Cwd: NewCwdRef(cwd), WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}},
			wantName:             "mkdir",
			wantApproval:         true,
			previewArgsJSON:      `{"path":"sub/dir"}`,
			wantPreviewSubstring: "sub/dir",
			wantSchemaRequired:   []string{"path"},
		},
		{
			tool:                 &CopyFileTool{Cwd: NewCwdRef(cwd), WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}},
			wantName:             "copy_file",
			wantApproval:         true,
			previewArgsJSON:      `{"src":"a.go","dst":"b.go"}`,
			wantPreviewSubstring: "a.go -> b.go",
			wantSchemaRequired:   []string{"src", "dst"},
		},
		{
			tool:                 &MoveFileTool{Cwd: NewCwdRef(cwd), WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}},
			wantName:             "move_file",
			wantApproval:         true,
			previewArgsJSON:      `{"src":"a.go","dst":"b.go"}`,
			wantPreviewSubstring: "a.go -> b.go",
			wantSchemaRequired:   []string{"src", "dst"},
		},
		{
			tool:                 &DeleteFileTool{Cwd: NewCwdRef(cwd), WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}},
			wantName:             "delete_file",
			wantApproval:         true,
			previewArgsJSON:      `{"path":"old.go"}`,
			wantPreviewSubstring: "old.go",
			wantSchemaRequired:   []string{"path"},
		},
		{
			tool:                 &ApplyDiffTool{Cwd: NewCwdRef(cwd)},
			wantName:             "apply_diff",
			wantApproval:         true,
			previewArgsJSON:      `{"diff":"diff --git a/x b/x"}`,
			wantPreviewSubstring: "apply_diff",
			wantSchemaRequired:   []string{"diff"},
		},
		{
			tool:                 &ListGitChangedFilesTool{Cwd: NewCwdRef(cwd)},
			wantName:             "list_git_changed_files",
			wantApproval:         false,
			previewArgsJSON:      `{}`,
			wantPreviewSubstring: "list_git_changed_files",
		},
		{
			tool:                 &GitCheckpointTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_checkpoint",
			wantApproval:         true,
			previewArgsJSON:      `{"message":"cp"}`,
			wantPreviewSubstring: "cp",
		},
		{
			tool:                 &RollbackTool{Cwd: NewCwdRef(cwd)},
			wantName:             "rollback",
			wantApproval:         true,
			previewArgsJSON:      `{"target":"HEAD~1"}`,
			wantPreviewSubstring: "HEAD~1",
		},
		{
			tool:                 &RunTestsTool{Cwd: NewCwdRef(cwd)},
			wantName:             "run_tests",
			wantApproval:         true,
			previewArgsJSON:      `{"command":"go test ./..."}`,
			wantPreviewSubstring: "go test ./...",
		},
		{
			tool:                 &MediaProbeTool{Cwd: NewCwdRef(cwd)},
			wantName:             "media_probe",
			wantApproval:         false,
			previewArgsJSON:      `{"path":"demo.mp4"}`,
			wantPreviewSubstring: "demo.mp4",
			wantSchemaRequired:   []string{"path"},
		},
		{
			tool:                 &MediaAnalyzeTool{Cwd: NewCwdRef(cwd)},
			wantName:             "media_analyze",
			wantApproval:         false,
			previewArgsJSON:      `{"path":"demo.mp4"}`,
			wantPreviewSubstring: "demo.mp4",
			wantSchemaRequired:   []string{"path"},
		},
		{
			tool:                 &MediaRenderTool{Cwd: NewCwdRef(cwd), WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}},
			wantName:             "media_render",
			wantApproval:         true,
			previewArgsJSON:      `{"input":"demo.mp4","output":"out.mp4","profiles":["youtube_16x9","x_16x9"]}`,
			wantPreviewSubstring: "profiles=youtube_16x9,x_16x9",
			wantSchemaRequired:   []string{"input", "output"},
		},
		{
			tool:                 &MediaComposeTool{Cwd: NewCwdRef(cwd), WriteOpts: WritePathOptions{Cwd: NewCwdRef(cwd)}},
			wantName:             "media_compose",
			wantApproval:         true,
			previewArgsJSON:      `{"output":"promo.mp4","segments":[{"type":"title","text":"Hi","duration":1}]}`,
			wantPreviewSubstring: "1 segments -> promo.mp4",
			wantSchemaRequired:   []string{"output", "segments"},
		},
		{
			tool:                 &GitBranchStatusTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_branch_status",
			wantApproval:         false,
			previewArgsJSON:      `{}`,
			wantPreviewSubstring: "git_branch_status",
		},
		{
			tool:                 &GitShowFileAtRevTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_show_file_at_rev",
			wantApproval:         false,
			previewArgsJSON:      `{"path":"x.go","rev":"HEAD"}`,
			wantPreviewSubstring: "x.go",
			wantSchemaRequired:   []string{"path"},
		},
		{
			tool:                 &GitDiffFilesTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_diff_files",
			wantApproval:         false,
			previewArgsJSON:      `{"paths":["x.go"]}`,
			wantPreviewSubstring: "paths=1",
		},
		{
			tool:                 &GitStageFilesTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_stage_files",
			wantApproval:         true,
			previewArgsJSON:      `{"paths":["x.go"]}`,
			wantPreviewSubstring: "x.go",
			// Cross-field validation (paths vs all) replaces the
			// single-field "required" assertion the old schema had.
		},
		{
			tool:                 &GitUnstageFilesTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_unstage_files",
			wantApproval:         true,
			previewArgsJSON:      `{"paths":["x.go"]}`,
			wantPreviewSubstring: "x.go",
			wantSchemaRequired:   []string{"paths"},
		},
		{
			tool:                 &GitCreateBranchTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_create_branch",
			wantApproval:         true,
			previewArgsJSON:      `{"name":"feat/x"}`,
			wantPreviewSubstring: "feat/x",
			wantSchemaRequired:   []string{"name"},
		},
		{
			tool:                 &GitCommitTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_commit",
			wantApproval:         true,
			previewArgsJSON:      `{"message":"msg"}`,
			wantPreviewSubstring: "msg",
			wantSchemaRequired:   []string{"message"},
		},
		{
			tool:                 &GitLogFileTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_log_file",
			wantApproval:         false,
			previewArgsJSON:      `{"path":"x.go"}`,
			wantPreviewSubstring: "x.go",
			wantSchemaRequired:   []string{"path"},
		},
		{
			tool:                 &GitBlameLinesTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_blame_lines",
			wantApproval:         false,
			previewArgsJSON:      `{"path":"x.go","start":1,"end":2}`,
			wantPreviewSubstring: "x.go:1-2",
			wantSchemaRequired:   []string{"path", "start", "end"},
		},
		{
			tool:                 &GitMergeBaseTool{Cwd: NewCwdRef(cwd)},
			wantName:             "git_merge_base",
			wantApproval:         false,
			previewArgsJSON:      `{"base":"main","head":"feature"}`,
			wantPreviewSubstring: "main, feature",
			wantSchemaRequired:   []string{"base", "head"},
		},
		{
			tool:                 &ListDirTool{Cwd: NewCwdRef(cwd)},
			wantName:             "list_dir",
			wantApproval:         false,
			previewArgsJSON:      `{"path":"sub"}`,
			wantPreviewSubstring: "sub",
		},
		{
			tool:                 &ListProjectStructureTool{Cwd: NewCwdRef(cwd)},
			wantName:             "list_project_structure",
			wantApproval:         false,
			previewArgsJSON:      `{"path":"sub","max_depth":2}`,
			wantPreviewSubstring: "sub",
		},
		{
			tool:                 &GlobTool{Cwd: NewCwdRef(cwd)},
			wantName:             "glob",
			wantApproval:         false,
			previewArgsJSON:      `{"pattern":"**/*.go"}`,
			wantPreviewSubstring: "**/*.go",
			wantSchemaRequired:   []string{"pattern"},
		},
		{
			tool:                 &GrepTool{Cwd: NewCwdRef(cwd)},
			wantName:             "grep",
			wantApproval:         false,
			previewArgsJSON:      `{"pattern":"needle"}`,
			wantPreviewSubstring: "needle",
			wantSchemaRequired:   []string{"pattern"},
		},
		{
			tool:                 &FetchURLTool{},
			wantName:             "fetch_url",
			wantApproval:         false,
			previewArgsJSON:      `{"url":"https://example.com"}`,
			wantPreviewSubstring: "https://example.com",
			wantSchemaRequired:   []string{"url"},
		},
	}

	for _, c := range cases {
		t.Run(c.wantName, func(t *testing.T) {
			if got := c.tool.Name(); got != c.wantName {
				t.Errorf("Name = %q, want %q", got, c.wantName)
			}
			if got := c.tool.Description(); got == "" {
				t.Errorf("Description must be non-empty")
			}
			schema := c.tool.Schema()
			if schema["type"] != "object" {
				t.Errorf("schema type = %v, want 'object'", schema["type"])
			}
			if _, ok := schema["properties"].(map[string]any); !ok {
				t.Errorf("schema must define properties as a map; got %T", schema["properties"])
			}
			for _, want := range c.wantSchemaRequired {
				required, _ := schema["required"].([]string)
				found := false
				for _, r := range required {
					if r == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("schema 'required' must include %q; got %v", want, required)
				}
			}
			args := c.approvalArgsJSON
			if args == "" {
				args = "{}"
			}
			if got := c.tool.RequiresApproval(args); got != c.wantApproval {
				t.Errorf("RequiresApproval = %v, want %v", got, c.wantApproval)
			}
			if got := c.tool.PreviewCall(c.previewArgsJSON); !strings.Contains(got, c.wantPreviewSubstring) {
				t.Errorf("PreviewCall = %q, missing %q", got, c.wantPreviewSubstring)
			}
		})
	}
}

func TestRunBashTool_Metadata(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	if tool.Name() != "run_bash" {
		t.Errorf("Name = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Errorf("Description empty")
	}
	if !tool.RequiresApproval("{}") {
		t.Errorf("run_bash must require approval")
	}
	preview := tool.PreviewCall(`{"command":"ls -la"}`)
	if !strings.Contains(preview, "ls -la") {
		t.Errorf("preview missing command: %q", preview)
	}
	if strings.Contains(preview, "[") {
		t.Errorf("run_bash preview should not carry a backend tag: %q", preview)
	}
	schema := tool.Schema()
	if got, _ := schema["required"].([]string); len(got) == 0 || got[0] != "command" {
		t.Errorf("schema required = %v, want [command]", got)
	}
}

func TestGitTool_Metadata(t *testing.T) {
	// GitTool's RequiresApproval is args-driven, so the table-based test
	// (which uses "{}") doesn't fit cleanly. Cover it here instead.
	tool := &GitTool{Cwd: NewCwdRef(t.TempDir())}
	if tool.Name() != "git" {
		t.Errorf("Name = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Errorf("Description empty")
	}
	schema := tool.Schema()
	if schema["type"] != "object" {
		t.Errorf("schema type = %v", schema["type"])
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["args"]; !ok {
		t.Errorf("schema should define an 'args' property")
	}
	required, _ := schema["required"].([]string)
	found := false
	for _, r := range required {
		if r == "args" {
			found = true
		}
	}
	if !found {
		t.Errorf("schema required = %v, want to include 'args'", required)
	}
}
