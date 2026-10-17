package config

import (
	"strings"
	"testing"
)

func TestLoad_LSPServerOverridesValidateAndRoundTrip(t *testing.T) {
	cfg, err := Load(writeFile(t, `
[lsp]
disabled = ["python"]

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
	if len(roundTripped.LSP.Disabled) != 1 || roundTripped.LSP.Disabled[0] != "python" {
		t.Fatalf("disabled language lost after Render: %#v\nrendered:\n%s", roundTripped.LSP.Disabled, Render(cfg))
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
		{"unknown disabled language", "[lsp]\ndisabled = [\"ruby\"]", "unknown language"},
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

func TestLoad_CodeMapLimitsValidateAndRoundTrip(t *testing.T) {
	cfg, err := Load(writeFile(t, "[code_map]\nmax_files = 12000\nmax_build_seconds = 45\nworkers = 4\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CodeMap.MaxFiles != 12000 || cfg.CodeMap.MaxBuildSeconds != 45 || cfg.CodeMap.Workers != 4 {
		t.Fatalf("code_map = %+v", cfg.CodeMap)
	}
	again, err := Load(writeFile(t, Render(cfg)))
	if err != nil {
		t.Fatalf("Load(Render): %v", err)
	}
	if again.CodeMap != cfg.CodeMap {
		t.Fatalf("code_map lost after Render (a saved config would silently drop it): %+v\n%s", again.CodeMap, Render(cfg))
	}
	blank, err := Load(writeFile(t, ""))
	if err != nil {
		t.Fatalf("Load blank: %v", err)
	}
	rendered := Render(blank)
	if i := strings.Index(rendered, "[code_map]"); i >= 0 {
		block := rendered[i+len("[code_map]"):]
		if j := strings.Index(block, "\n["); j >= 0 {
			block = block[:j]
		}
		if strings.Contains(block, "=") {
			t.Fatalf("unset code_map limits must not render as zero-valued keys, got block:\n%s", block)
		}
	}
}

func TestLoad_RejectsOutOfRangeCodeMapLimits(t *testing.T) {
	for name, src := range map[string]string{
		"negative files":   "[code_map]\nmax_files = -1",
		"too many files":   "[code_map]\nmax_files = 200001",
		"negative seconds": "[code_map]\nmax_build_seconds = -5",
		"too many seconds": "[code_map]\nmax_build_seconds = 601",
		"too many workers": "[code_map]\nworkers = 65",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeFile(t, src)); err == nil || !strings.Contains(err.Error(), "code_map.") {
				t.Fatalf("Load error = %v, want a code_map range error", err)
			}
		})
	}
}
