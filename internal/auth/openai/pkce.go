// Package openai implements the OAuth 2.0 Authorization Code + PKCE
// flow that OpenAI's Codex CLI uses to authenticate to ChatGPT
// subscriber backends. This is the "Sign in with ChatGPT" flow —
// distinct from API-key auth, and intentionally separate from
// internal/adapter so it can be reused by a future cobra subcommand,
// the wizard, and a `/login` slash command without circular imports.
//
// The package is raw net/http only, no vendor SDKs — it follows the
// same template internal/adapter/gemini.go uses.
package openai

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PKCE holds an RFC 7636 code_verifier + S256 code_challenge pair.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE returns a fresh PKCE pair: 32 random bytes encoded as
// URL-safe base64 (no padding) for the verifier — 43 chars, well
// inside RFC 7636's 43–128 range — and SHA-256(verifier) likewise
// URL-safe-base64-encoded for the challenge.
func NewPKCE() (PKCE, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return PKCE{}, fmt.Errorf("pkce: read random: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

// NewState returns a random URL-safe state value used to defend
// against CSRF on the loopback redirect. 32 bytes is more entropy
// than the OAuth spec requires, but cheap.
func NewState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("state: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
