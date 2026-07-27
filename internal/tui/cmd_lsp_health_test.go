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
		"┌─ LSP Code Intelligence ───────────────── Python ─┐",
		"│                                                  │",
		"│   pyright not found — running without go-to-def, │",
		"│   live diagnostics, and symbol-aware review.     │",
		"│                                                  │",
		"│     npm install -g pyright                       │",
		"│                                                  │",
		"│   Everything works without it; this just unlocks │",
		"│   deeper code intelligence.                      │",
		"│                                                  │",
		"└──────────────────────────────────────────────────┘",
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

// TestRenderLSPAdvisory_ClampsPathologicallyLongInstallHint is a
// regression test: renderLSPAdvisoryBox previously had no width cap at
// all, so a very long InstallHint (a custom LSP config entry, or any
// unrecognized language ID whose install command falls through to
// InstallHint) could stretch the box arbitrarily wide, well past any
// real terminal. It now wraps at the shared labeledBoxCap ceiling like
// the approval modal and plan-mode decision cards.
func TestRenderLSPAdvisory_ClampsPathologicallyLongInstallHint(t *testing.T) {
	longHint := strings.Repeat("verylonginstallhintwithnospaces", 20) // 640 chars, unbreakable
	card := stripANSI(renderLSPAdvisory([]lsp.DetectedLanguage{{
		Language:        lsp.Language{ID: "custom-unknown", Name: "Custom", InstallHint: longHint},
		FilesAvailable:  1,
		ServerAvailable: false,
	}}))

	lines := strings.Split(card, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected a multi-line box, got %q", card)
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w > labeledBoxCap+4 {
			t.Errorf("row %d width %d exceeds labeledBoxCap+4=%d: %q", i, w, labeledBoxCap+4, line)
		}
	}
	// Every row in a well-formed box shares one width — confirms the
	// wrapped continuation lines were padded/framed consistently, not
	// just truncated.
	wantWidth := ansi.StringWidth(lines[0])
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != wantWidth {
			t.Errorf("row %d width = %d, want %d (ragged box):\n%s", i, got, wantWidth, card)
		}
	}
}
