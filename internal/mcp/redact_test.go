package mcp_test

import (
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/mcp"
)

func TestRedact(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantHidden string // substring that must NOT appear in the output
		wantMarker string // substring that must appear in the output
	}{
		{"bearer token", "sent header Bearer sk-ant-abc123xyz to the server", "sk-ant-abc123xyz", "Bearer ***"},
		{"authorization header", "Authorization: Bearer sk-ant-abc123xyz", "sk-ant-abc123xyz", "Authorization: ***"},
		{"token query param", "GET /mcp?token=abc123&other=1", "token=abc123", "token=***"},
		{"set-cookie", "Set-Cookie: session=abc123; Path=/; HttpOnly", "abc123", "Set-Cookie: ***"},
		{"case insensitive", "AUTHORIZATION: BEARER xyz789", "xyz789", "***"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mcp.Redact(c.in)
			if strings.Contains(got, c.wantHidden) {
				t.Errorf("Redact(%q) = %q, secret %q leaked", c.in, got, c.wantHidden)
			}
			if !strings.Contains(got, c.wantMarker) {
				t.Errorf("Redact(%q) = %q, want it to contain %q", c.in, got, c.wantMarker)
			}
		})
	}
}

func TestRedact_LeavesUnrelatedTextAlone(t *testing.T) {
	in := "mcp(linear): connect: dial tcp 1.2.3.4:443: connection refused"
	if got := mcp.Redact(in); got != in {
		t.Errorf("Redact should not touch text with no secret patterns; got %q, want %q", got, in)
	}
}

func TestRedact_TokenParamStopsAtDelimiter(t *testing.T) {
	got := mcp.Redact("token=secret123&next=keep-me")
	if !strings.Contains(got, "next=keep-me") {
		t.Errorf("Redact should not eat unrelated query params after the token; got %q", got)
	}
	if strings.Contains(got, "secret123") {
		t.Errorf("Redact should still scrub the token value; got %q", got)
	}
}
