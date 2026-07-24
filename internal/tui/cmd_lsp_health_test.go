package tui

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/lsp"
)

func TestRenderLSPAdvisory_PythonMissing(t *testing.T) {
	card := stripANSI(renderLSPAdvisory([]lsp.DetectedLanguage{{
		Language:        lsp.Language{ID: "python", Name: "Python", Command: []string{"pyright-langserver", "--stdio"}, InstallHint: "Install Pyright language server: npm install -g pyright."},
		FilesAvailable:  1,
		ServerAvailable: false,
	}}))

	for _, want := range []string{
		"LSP Code Intelligence",
		"Python detected",
		"semantic server missing",
		"npm install -g pyright",
		"Continuing with normal file reads for now.",
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("LSP advisory missing %q:\n%s", want, card)
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
