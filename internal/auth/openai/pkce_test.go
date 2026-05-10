package openai

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestNewPKCE(t *testing.T) {
	for i := 0; i < 8; i++ {
		p, err := NewPKCE()
		if err != nil {
			t.Fatalf("NewPKCE: %v", err)
		}
		if len(p.Verifier) < 43 || len(p.Verifier) > 128 {
			t.Errorf("verifier length %d out of RFC 7636 range 43-128", len(p.Verifier))
		}
		// Re-derive the challenge from the verifier; it must match
		// what NewPKCE returned.
		sum := sha256.Sum256([]byte(p.Verifier))
		want := base64.RawURLEncoding.EncodeToString(sum[:])
		if p.Challenge != want {
			t.Errorf("challenge mismatch:\n have %s\n want %s", p.Challenge, want)
		}
	}
}

func TestNewPKCEUnique(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 64; i++ {
		p, err := NewPKCE()
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[p.Verifier]; dup {
			t.Fatalf("duplicate verifier on iteration %d", i)
		}
		seen[p.Verifier] = struct{}{}
	}
}

func TestNewState(t *testing.T) {
	s, err := NewState()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) < 32 {
		t.Errorf("state too short: %d", len(s))
	}
}
