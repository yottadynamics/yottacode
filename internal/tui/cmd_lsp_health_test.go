package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

func TestRenderLSPAdvisory_PythonMissing(t *testing.T) {
	card := stripANSI(renderLSPAdvisory([]lsp.DetectedLanguage{{
		Language:        lsp.Language{ID: "python", Name: "Python", Command: []string{"pyright-langserver", "--stdio"}, InstallHint: "Install Pyright language server: npm install -g pyright."},
		FilesAvailable:  1,
		ServerAvailable: false,
	}}))

	wantCard := strings.Join([]string{
		"┌─ LSP Code Intelligence ─────────────── Python ─┐",
		"│                                                │",
		"│ pyright not found — running without go-to-def, │",
		"│ live diagnostics, and symbol-aware review.     │",
		"│                                                │",
		"│   npm install -g pyright                       │",
		"│                                                │",
		"│ Everything works without it; this just unlocks │",
		"│ deeper code intelligence.                      │",
		"│                                                │",
		"└────────────────────────────────────────────────┘",
	}, "\n")
	if card != wantCard {
		t.Fatalf("LSP advisory changed:\nwant:\n%s\n\ngot:\n%s", wantCard, card)
	}

	for _, want := range []string{
		"LSP Code Intelligence",
		"Python",
		"pyright not found — running without go-to-def",
		"live diagnostics, and symbol-aware review.",
		"npm install -g pyright",
		"Everything works without it; this just unlocks",
		"deeper code intelligence.",
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("LSP advisory missing %q:\n%s", want, card)
		}
	}

	lines := strings.Split(card, "\n")
	wantWidth := ansi.StringWidth(lines[0])
	for _, line := range lines {
		if got := ansi.StringWidth(line); got != wantWidth {
			t.Fatalf("LSP advisory row width = %d, want %d for %q:\n%s", got, wantWidth, line, card)
		}
	}
}

func TestRenderLSPAdvisory_SuppressesInstalledServers(t *testing.T) {
	card := renderLSPAdvisory([]lsp.DetectedLanguage{{
		Language:        lsp.Language{ID: "python", Name: "Python", Command: []string{"pyright-langserver", "--stdio"}},
		FilesAvailable:  1,
		ServerAvailable: true,
	}})
	if card != "" {
		t.Fatalf("installed servers should not produce advisory card: %q", stripANSI(card))
	}
}

func TestSlashLSPRemoved(t *testing.T) {
	if findSlash("lsp") != nil {
		t.Fatal("/lsp should stay removed; LSP setup is surfaced by the session advisory card")
	}
}
