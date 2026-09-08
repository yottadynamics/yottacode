package mcp

import "regexp"

var (
	reAuthHeader  = regexp.MustCompile(`(?i)(authorization|x-api-key|api-key|x-auth-token):\s*[^\r\n]+`)
	reBearer      = regexp.MustCompile(`(?i)\bbearer\s+\S+`)
	reSecretParam = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|token|password|secret)=([^&\s]+)`)
	reSetCookie   = regexp.MustCompile(`(?i)set-cookie:\s*[^\r\n]+`)
	reUserInfo    = regexp.MustCompile(`(?i)(https?://)([^/@\s]+):([^/@\s]+)@`)
)

// Redact rewrites common secret-bearing patterns — Bearer tokens,
// Authorization headers, token= query params, Set-Cookie values — to ***
// before a string reaches /mcp logs, a start-error message, or the
// transcript. It's a narrow, pattern-based scrub matched to this phase's
// bar ("secrets never appear in logs or start-error text"), not a general
// secret scanner.
//
// Order matters: the Authorization-header pattern runs first and consumes
// the rest of its line (including any "Bearer <token>" it introduces), so
// the standalone Bearer pattern only fires on a bare "Bearer <token>" that
// wasn't already part of a redacted Authorization line.
func Redact(s string) string {
	s = reAuthHeader.ReplaceAllString(s, "$1: ***")
	s = reBearer.ReplaceAllString(s, "Bearer ***")
	s = reSecretParam.ReplaceAllString(s, "$1=***")
	s = reSetCookie.ReplaceAllString(s, "Set-Cookie: ***")
	s = reUserInfo.ReplaceAllString(s, "${1}***:***@")
	return s
}
