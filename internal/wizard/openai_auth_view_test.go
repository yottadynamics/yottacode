package wizard

import (
	"strings"
	"testing"
)

// TestViewOpenAIAuthLoginKeepsCompleteURL verifies that a narrow terminal
// wraps the OAuth URL instead of replacing its tail with an ellipsis.
func TestViewOpenAIAuthLoginKeepsCompleteURL(t *testing.T) {
	url := "https://auth.openai.com/oauth/authorize?client_id=app_example&code_challenge=abcdefghijklmnopqrstuvwxyz0123456789&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback"
	m := wizardModel{width: 40, openAIAuthURL: url}
	out := m.viewOpenAIAuthLogin()
	urlSection := out[strings.Index(out, "If it didn't"):strings.Index(out, "Ctrl-C")]
	if strings.Contains(urlSection, "…") {
		t.Fatal("OpenAI auth URL was truncated")
	}
	if got := strings.Join(wrapURL(url, lineWidthFor(m.width)-2), ""); got != url {
		t.Fatalf("wrapped URL changed: got %q, want %q", got, url)
	}
}

func TestWrapURL(t *testing.T) {
	for _, tt := range []struct {
		name  string
		url   string
		width int
		want  string
	}{
		{name: "short", url: "https://example.test", width: 80, want: "https://example.test"},
		{name: "exact chunks", url: "abcdefgh", width: 4, want: "abcdefgh"},
		{name: "narrow", url: "abcdef", width: 2, want: "abcdef"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Join(wrapURL(tt.url, tt.width), ""); got != tt.want {
				t.Fatalf("wrapped URL = %q, want %q", got, tt.want)
			}
		})
	}
}
