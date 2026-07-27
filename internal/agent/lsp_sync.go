package agent

import (
	"context"
	"fmt"

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
		return fmt.Sprintf("◆ lsp: change notification skipped: %v", err)
	}
	return "✓ lsp: document synced"
}
