package acp

import (
	"testing"

	coderacp "github.com/coder/acp-go-sdk"
)

func TestConvertMCPServers_Stdio(t *testing.T) {
	out := convertMCPServers([]coderacp.McpServer{
		{Stdio: &coderacp.McpServerStdio{
			Name:    "fs",
			Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
			Env:     []coderacp.EnvVariable{{Name: "FOO", Value: "bar"}},
		}},
	})
	if len(out) != 1 {
		t.Fatalf("got %d servers, want 1", len(out))
	}
	s := out[0]
	if s.Name != "fs" || s.Command != "npx" || s.Transport != "" {
		t.Errorf("stdio entry = %+v", s)
	}
	if len(s.Args) != 2 || s.Args[0] != "-y" {
		t.Errorf("stdio args = %+v", s.Args)
	}
	if s.Env["FOO"] != "bar" {
		t.Errorf("stdio env = %+v", s.Env)
	}
}

func TestConvertMCPServers_HTTP(t *testing.T) {
	out := convertMCPServers([]coderacp.McpServer{
		{Http: &coderacp.McpServerHttpInline{
			Name:    "docs",
			Url:     "https://example.com/mcp",
			Headers: []coderacp.HttpHeader{{Name: "Authorization", Value: "Bearer secret"}},
		}},
	})
	if len(out) != 1 {
		t.Fatalf("got %d servers, want 1", len(out))
	}
	s := out[0]
	if s.Name != "docs" || s.Transport != "http" || s.URL != "https://example.com/mcp" {
		t.Errorf("http entry = %+v", s)
	}
	if s.Headers["Authorization"] != "Bearer secret" {
		t.Errorf("http headers = %+v", s.Headers)
	}
}

func TestConvertMCPServers_SSE(t *testing.T) {
	out := convertMCPServers([]coderacp.McpServer{
		{Sse: &coderacp.McpServerSseInline{
			Name: "docs-sse",
			Url:  "https://example.com/mcp/sse",
		}},
	})
	if len(out) != 1 {
		t.Fatalf("got %d servers, want 1", len(out))
	}
	s := out[0]
	if s.Name != "docs-sse" || s.Transport != "sse" || s.URL != "https://example.com/mcp/sse" {
		t.Errorf("sse entry = %+v", s)
	}
}

func TestConvertMCPServers_MixedAndEmpty(t *testing.T) {
	out := convertMCPServers([]coderacp.McpServer{
		{Stdio: &coderacp.McpServerStdio{Name: "fs", Command: "npx"}},
		{Http: &coderacp.McpServerHttpInline{Name: "docs", Url: "https://example.com"}},
		{}, // no variant set — must be skipped, not panic
	})
	if len(out) != 2 {
		t.Fatalf("got %d servers, want 2 (empty entry skipped)", len(out))
	}
	if out[0].Name != "fs" || out[1].Name != "docs" {
		t.Errorf("unexpected order/names: %+v", out)
	}
}

func TestConvertMCPServers_Empty(t *testing.T) {
	if out := convertMCPServers(nil); out != nil {
		t.Errorf("convertMCPServers(nil) = %+v, want nil", out)
	}
}
