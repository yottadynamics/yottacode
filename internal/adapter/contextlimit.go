package adapter

import "strings"

// contextOverflowMarkers are lowercase substrings that identify a
// provider rejecting a request because the input exceeds the model's
// context window. Sources: the OpenAI family ("context_length_exceeded"
// error code, "maximum context length" in chat-completions phrasing),
// the ChatGPT Codex backend ("exceeds the context window", probed
// 2026-06-10), and Anthropic ("prompt is too long"). OpenAI-compatible
// servers (vLLM, NIM, proxies) echo the OpenAI phrasings.
var contextOverflowMarkers = []string{
	"context_length_exceeded",
	"exceeds the context window",
	"maximum context length",
	"prompt is too long",
	"input length and `max_tokens` exceed context limit",
}

// IsContextOverflow reports whether err is a provider-side rejection
// for exceeding the model's context window — the signal passive
// window-drift correction listens for. Conservative by design: an
// error that matches no known marker is never classified as overflow,
// because a false positive shrinks a window pin that then has to be
// re-proven upward by live traffic.
func IsContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range contextOverflowMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
