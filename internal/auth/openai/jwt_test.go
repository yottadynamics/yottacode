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

// TestDecodeClaimsNestedAuthAndProfile mirrors the real access-token
// shape (verified against an actual OpenAI-issued token): email and
// chatgpt_account_id are not top-level claims, they're nested under
// the "https://api.openai.com/profile" and "https://api.openai.com/auth"
// namespaces. Before this, DecodeClaims only looked at the top level,
// which is why logged-in users saw "<no email claim>" and every
// per-account model probe 401'd for lack of the account id.
func TestDecodeClaimsNestedAuthAndProfile(t *testing.T) {
	tok := makeJWT(t, map[string]any{
		"sub": "google-oauth2|123",
		"https://api.openai.com/profile": map[string]any{
			"email": "p@example.com",
			"name":  "P",
		},
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "351075c9-e0b6-4e50-9233-c48f444eeb98",
			"chatgpt_plan_type":  "pro",
		},
	})
	c, err := DecodeClaims(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "p@example.com" {
		t.Errorf("Email = %q, want p@example.com", c.Email)
	}
	if c.ChatGPTAccountID != "351075c9-e0b6-4e50-9233-c48f444eeb98" {
		t.Errorf("ChatGPTAccountID = %q, want 351075c9-e0b6-4e50-9233-c48f444eeb98", c.ChatGPTAccountID)
	}
}

// TestDecodeClaimsTopLevelEmailWins covers the id_token shape, which
// carries a top-level "email" claim (no nested profile). Top-level
// must not be overridden by an absent nested claim.
func TestDecodeClaimsTopLevelEmailWins(t *testing.T) {
	tok := makeJWT(t, map[string]any{
		"sub":   "google-oauth2|123",
		"email": "top@example.com",
	})
	c, err := DecodeClaims(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "top@example.com" {
		t.Errorf("Email = %q, want top@example.com", c.Email)
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
