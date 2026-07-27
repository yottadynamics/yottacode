package agent

import (
	"github.com/yottadynamics/yottacode/internal/codemap"
	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

// CoreToolDeps carries the per-session settings the core cwd-bound tools
// need at construction. It is passed to RegisterCoreCwdTools so the same
// toolset can be built against different working directories — the parent
// session's cwd (TUI / oneshot) and a dispatch subagent's isolated
// worktree cwd — without duplicating the registration list.
type CoreToolDeps struct {
	// WriteOpts is the write-path policy for the mutating file tools. Its
	// Cwd field is overridden to match the cwd passed to
	// RegisterCoreCwdTools, so callers don't have to keep the two in sync.
	WriteOpts WritePathOptions

	// DenyReads is the credential-bearing read denylist (read_file,
	// read_many_files, grep). Typically DefaultDenyReadPaths(cwd).
	DenyReads []string

	// SupportsImages mirrors the adapter profile's image capability so
	// read_file can return image blocks when the model accepts them.
	SupportsImages bool

	// EnableLSP registers the experimental language-server-backed read-only
	// code-intelligence tools. The gate lives outside this helper so parent
	// sessions and dispatch workers expose the same surface once enabled.
	EnableLSP bool

	// LSPClientFactory lets tests inject a fake language-server client. Nil
	// uses the production stdio JSON-RPC client.
	LSPClientFactory lspClientFactory

	// LSPManager reuses initialized language-server processes across tool calls
	// for the parent session. Nil keeps the simple one-process-per-call path.
	LSPManager *lspci.Manager

	// LSPServers carries optional per-language server command overrides keyed by
	// stable language ID (go/typescript/python/rust).
	LSPServers map[string][]string

	// CodeMapProvider exposes the optional experimental repository structure
	// index to read-only agent tools.
	CodeMapProvider codemap.Provider

	// EnableCodeMap registers the experimental read-only code-map tools.
	EnableCodeMap bool
}

// RegisterCoreCwdTools registers the core working-directory-bound tools —
// file read/write/edit, directory + code search, git-read/stage/commit,
// checkpoints, and command execution — against the given CwdRef. Every
// tool resolves relative paths through cwd, so passing a fresh CwdRef
// (e.g. one pinned to a git worktree) yields a fully isolated toolset.
//
// This is the shared core both the parent session (internal/tui/run.go,
// internal/oneshot/oneshot.go) and the dispatch worktree-child registry
// build on. It deliberately excludes session-scoped extras the parent
// registers separately — GitHub/PR/issue tools, memory, worktree-admin,
// the commit-workflow composites, web fetch/search, todo, plan-mode — and
// the Agent/dispatch/integrate delegation tools, none of which a leaf
// worker needs.
func RegisterCoreCwdTools(reg *Registry, cwd *CwdRef, deps CoreToolDeps) {
	// Bind the write policy to this cwd so a caller can't accidentally
	// register write tools confined to a different directory than the
	// read/search/git tools.
	wo := deps.WriteOpts
	wo.Cwd = cwd

	reg.Register(&ReadFileTool{Cwd: cwd, DenyReadPaths: deps.DenyReads, SupportsImages: deps.SupportsImages})
	reg.Register(&ReadManyFilesTool{Cwd: cwd, DenyReadPaths: deps.DenyReads})
	reg.Register(&WriteFileTool{Cwd: cwd, WriteOpts: wo, LSPManager: deps.LSPManager, LSPServers: deps.LSPServers})
	reg.Register(&EditFileTool{Cwd: cwd, WriteOpts: wo, LSPManager: deps.LSPManager, LSPServers: deps.LSPServers})
	reg.Register(&ApplyDiffTool{Cwd: cwd, WriteOpts: wo})
	reg.Register(&MkdirTool{Cwd: cwd, WriteOpts: wo})
	reg.Register(&CopyFileTool{Cwd: cwd, WriteOpts: wo, DenyReadPaths: deps.DenyReads})
	reg.Register(&MoveFileTool{Cwd: cwd, WriteOpts: wo})
	reg.Register(&DeleteFileTool{Cwd: cwd, WriteOpts: wo})

	reg.Register(&ListGitChangedFilesTool{Cwd: cwd})
	reg.Register(&GitBranchStatusTool{Cwd: cwd})
	reg.Register(&GitShowFileAtRevTool{Cwd: cwd})
	reg.Register(&GitDiffFilesTool{Cwd: cwd})
	reg.Register(&GitStageFilesTool{Cwd: cwd})
	reg.Register(&GitUnstageFilesTool{Cwd: cwd})
	reg.Register(&GitCreateBranchTool{Cwd: cwd})
	reg.Register(&GitCommitTool{Cwd: cwd})
	reg.Register(&GitLogFileTool{Cwd: cwd})
	reg.Register(&GitBlameLinesTool{Cwd: cwd})
	reg.Register(&GitMergeBaseTool{Cwd: cwd})
	reg.Register(&GitDiffStatTool{Cwd: cwd})
	reg.Register(&GitDiffStagedTool{Cwd: cwd})
	reg.Register(&GitDiffUnstagedTool{Cwd: cwd})
	reg.Register(&GitCommitsBetweenTool{Cwd: cwd})
	reg.Register(&GitBranchAheadBehindTool{Cwd: cwd})
	reg.Register(&GitBranchDiffTool{Cwd: cwd})
	reg.Register(&GitCommitAmendTool{Cwd: cwd})
	reg.Register(&GitCommitFixupTool{Cwd: cwd})
	reg.Register(&GitCheckpointTool{Cwd: cwd})
	reg.Register(&RollbackTool{Cwd: cwd})

	reg.Register(&RunTestsTool{Cwd: cwd})
	reg.Register(&RunBashTool{Cwd: cwd})

	reg.Register(&MediaProbeTool{Cwd: cwd, DenyReadPaths: deps.DenyReads})
	reg.Register(&MediaAnalyzeTool{Cwd: cwd, DenyReadPaths: deps.DenyReads})
	reg.Register(&MediaRenderTool{Cwd: cwd, DenyReadPaths: deps.DenyReads, WriteOpts: wo})

	reg.Register(&ListDirTool{Cwd: cwd})
	reg.Register(&ListProjectStructureTool{Cwd: cwd})
	reg.Register(&GlobTool{Cwd: cwd})
	reg.Register(&GrepTool{Cwd: cwd, DenyReadPaths: deps.DenyReads})
	reg.Register(&PRReadinessContextTool{Cwd: cwd})
	if deps.EnableCodeMap {
		reg.Register(&CodeMapTool{Provider: deps.CodeMapProvider})
		reg.Register(&CodeSymbolsTool{Provider: deps.CodeMapProvider})
		reg.Register(&CodeStructureProjectionTool{Provider: deps.CodeMapProvider})
		reg.Register(&CodeDependenciesTool{Provider: deps.CodeMapProvider})
		reg.Register(&CodeDependentsTool{Provider: deps.CodeMapProvider})
		reg.Register(&CodeImpactTool{Provider: deps.CodeMapProvider})
		reg.Register(&CodeCyclesTool{Provider: deps.CodeMapProvider})
		reg.Register(&CodeMapDiagramTool{Provider: deps.CodeMapProvider})
	}
	if deps.EnableLSP {
		base := lspToolBase{Cwd: cwd, DenyReadPaths: deps.DenyReads, NewClient: deps.LSPClientFactory, Servers: deps.LSPServers, Manager: deps.LSPManager}
		reg.Register(&LSPStatusTool{lspToolBase: base})
		reg.Register(&LSPSymbolsTool{lspToolBase: base})
		reg.Register(&LSPDocumentSymbolsTool{lspToolBase: base})
		reg.Register(&LSPDocumentHighlightsTool{lspToolBase: base})
		reg.Register(&LSPSelectionRangesTool{lspToolBase: base})
		reg.Register(&LSPDefinitionTool{lspToolBase: base})
		reg.Register(&LSPTypeDefinitionTool{lspToolBase: base})
		reg.Register(&LSPImplementationTool{lspToolBase: base})
		reg.Register(&LSPReferencesTool{lspToolBase: base})
		reg.Register(&LSPHoverTool{lspToolBase: base})
		reg.Register(&LSPSignatureHelpTool{lspToolBase: base})
		reg.Register(&LSPDiagnosticsTool{lspToolBase: base})
		reg.Register(&LSPChangedFilesDiagnosticsTool{lspToolBase: base})
		reg.Register(&LSPCodeActionsTool{lspToolBase: base})
		reg.Register(&LSPCodeActionPreviewTool{lspToolBase: base})
		reg.Register(&LSPRenamePreviewTool{lspToolBase: base})
		reg.Register(&LSPFormatPreviewTool{lspToolBase: base})
		reg.Register(&LSPApplyWorkspaceEditTool{lspToolBase: base, WriteOpts: wo})
		reg.Register(&LSPCallHierarchyTool{lspToolBase: base})
	}
}
