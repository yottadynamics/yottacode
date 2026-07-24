package config

import (
	"strings"
	"testing"
)

func TestLoad_LSPServerOverridesValidateAndRoundTrip(t *testing.T) {
	cfg, err := Load(writeFile(t, `
[lsp.servers]
go = ["/opt/bin/gopls", "-remote=auto"]
python = ["pyright-langserver", "--stdio"]
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := strings.Join(cfg.LSP.Servers["go"], " "); got != "/opt/bin/gopls -remote=auto" {
		t.Fatalf("go override = %q", got)
	}
	roundTripped, err := Load(writeFile(t, Render(cfg)))
	if err != nil {
		t.Fatalf("Load(Render): %v", err)
	}
	if got := strings.Join(roundTripped.LSP.Servers["python"], " "); got != "pyright-langserver --stdio" {
		t.Fatalf("python override lost after Render: %q\nrendered:\n%s", got, Render(cfg))
	}
}

func TestLoad_RejectsInvalidLSPServerOverride(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"unknown language", "[lsp.servers]\nruby = [\"solargraph\"]", "unknown"},
		{"empty command", "[lsp.servers]\ngo = []", "must name a command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeFile(t, tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want containing %q", err, tc.want)
			}
		})
	}
}
