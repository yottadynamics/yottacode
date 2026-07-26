package agent

import (
	"fmt"
	"strings"

	lspci "github.com/yottadynamics/yottacode/internal/lsp"
)

// formatDiagnosticsSnapshot distinguishes a clean published result from a
// timeout/no-publication result. That distinction matters after edits: "clean"
// is confidence, while "not published yet" is only advisory absence.
func formatDiagnosticsSnapshot(snap lspci.DiagnosticsSnapshot) string {
	if !snap.Published {
		return "(diagnostics not published before timeout)\n"
	}
	if len(snap.Diagnostics) == 0 {
		return "(no diagnostics)\n"
	}
	var b strings.Builder
	for _, d := range snap.Diagnostics {
		parts := []string{d.Severity}
		if d.Source != "" {
			parts = append(parts, "source="+d.Source)
		}
		if d.Code != "" {
			parts = append(parts, "code="+d.Code)
		}
		if len(d.Tags) > 0 {
			parts = append(parts, "tags="+strings.Join(d.Tags, ","))
		}
		fmt.Fprintf(&b, "%s:%d:%d\t%s\t%s\n", d.Path, d.Line+1, d.Character+1, strings.Join(parts, " "), d.Message)
		for _, rel := range d.Related {
			fmt.Fprintf(&b, "  related\t%s:%d:%d\t%s\n", rel.Location.Path, rel.Location.Line+1, rel.Location.Character+1, rel.Message)
		}
	}
	return b.String()
}
