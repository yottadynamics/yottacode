package openai

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// makeJWT produces an unsigned JWT for testing — header.payload.sig
// where sig is fake. DecodeClaims doesn't verify signatures.
func makeJWT(t *testing.T, payload any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
}

func TestDecodeClaimsBasic(t *testing.T) {
	tok := makeJWT(t, map[string]any{
		"sub":   "user_abc",
		"email": "p@example.com",
		"aud":   "chatgpt-backend",
		"exp":   1_777_000_000,
	})
	c, err := DecodeClaims(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "user_abc" || c.Email != "p@example.com" {
		t.Errorf("got %+v", c)
	}
	if len(c.Audience) != 1 || c.Audience[0] != "chatgpt-backend" {
		t.Errorf("audience: %+v", c.Audience)
	}
	if c.ExpiresAt != 1_777_000_000 {
		t.Errorf("exp: %d", c.ExpiresAt)
	}
}

func TestDecodeClaimsAudienceArray(t *testing.T) {
	tok := makeJWT(t, map[string]any{
		"sub": "u",
		"aud": []string{"a", "b"},
	})
	c, err := DecodeClaims(tok)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Audience) != 2 || c.Audience[0] != "a" || c.Audience[1] != "b" {
		t.Errorf("audience: %+v", c.Audience)
	}
}

func TestDecodeClaimsBadFormat(t *testing.T) {
	for _, s := range []string{"", "abc.def", "not.a.jwt.at.all"} {
		if _, err := DecodeClaims(s); err == nil {
			t.Errorf("DecodeClaims(%q) returned no error", s)
		}
	}
}

func TestDecodeClaimsBadBase64(t *testing.T) {
	if _, err := DecodeClaims("h.@@@.s"); err == nil {
		t.Error("expected error on undecodable payload")
	}
}
