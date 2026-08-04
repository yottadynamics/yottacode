package agent

import (
	"context"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

// notifyLSPFileChanged keeps a pooled language server in sync after yottacode
// writes a source file. It returns a short advisory note instead of an error so
// file edits never fail just because optional LSP infrastructure is unavailable.
func notifyLSPFileChanged(ctx context.Context, cwd *CwdRef, manager *lspci.Manager, servers map[string][]string, path, text string) string {
	if manager == nil {
		return ""
	}
	lang, ok := lspci.ResolveFile(path)
	if !ok {
		return ""
	}
	lang = lspci.ApplyOverrides(lang, servers)
	if !lspci.ServerAvailable(lang) {
		return ""
	}
	fallback := "."
	if cwd != nil {
		fallback = cwd.Get()
	}
	root := lspci.WorkspaceRoot(path, lang, fallback)
	if err := manager.NotifyFileChanged(ctx, lang, root, path, text); err != nil {
		// LSP sync is advisory. Raw local-server transport errors (for example
		// "write |1: broken pipe") looked like edit failures even though the file
		// write succeeded, so keep them out of tool output.
		return ""
	}
	return "○ lsp · document synced"
}

// invalidateLSPServers closes pooled language servers after a bulk mutation that
// can rewrite many files outside the single-file didChange hook. LSP is
// advisory, so invalidation never makes the underlying git/rollback operation
// fail; the next semantic tool call will reopen a clean server view lazily.
func invalidateLSPServers(manager *lspci.Manager) string {
	if manager == nil {
		return ""
	}
	manager.InvalidateAll()
	return "○ lsp · servers invalidated"
}
