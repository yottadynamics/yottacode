package copilot

import "strings"

// IsChatModel returns true if the model ID looks like a chat/completion
// model rather than an internal routing entry, embedding model, or
// other non-chat artifact. The Copilot /models endpoint returns
// everything the account has access to, including entries like
// "accounts/msft/routers/...", "text-embedding-*", and internal tool
// names that aren't usable for chat completions.
func IsChatModel(id string) bool {
	if strings.Contains(id, "/") {
		return false
	}
	if strings.HasPrefix(id, "text-embedding") {
		return false
	}
	if strings.HasPrefix(id, "oswe-") {
		return false
	}
	return true
}
