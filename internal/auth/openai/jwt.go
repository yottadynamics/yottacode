package openai

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// Claims is the subset of an OpenAI access-token JWT payload we care
// about. Subject/Email are surfaced for human-friendly output.
// ChatGPTAccountID is NOT just identity — it's a required input to
// the `chatgpt-account-id` header the codex backend needs on every
// /backend-api/codex/* call to resolve which ChatGPT account (and
// therefore which billing/entitlement) the request runs under. The
// token also carries iat/auth_time/etc. which we ignore.
type Claims struct {
	Subject          string
	Email            string
	AuthMethod       string
	Audience         []string
	ExpiresAt        int64
	ChatGPTAccountID string
}

// DecodeClaims parses a JWT's payload segment without verifying its
// signature. The bearer token's authority comes from being presented
// to OpenAI's resource server, not from local verification, so a
// signature check here would just add a JWKS-fetch dependency for no
// benefit.
func DecodeClaims(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("jwt: token does not have three segments")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some implementations emit padded base64 in the wire format;
		// fall back transparently before giving up.
		body, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return Claims{}, err
		}
	}
	// "aud" can be a string or a []string per RFC 7519. Use a
	// json.RawMessage and try both shapes rather than a custom
	// UnmarshalJSON, which would over-engineer this.
	//
	// email and chatgpt_account_id are not top-level claims on the
	// access token — OpenAI's Auth0 tenant nests them under two
	// custom namespaced claims: "https://api.openai.com/profile"
	// (email/name) and "https://api.openai.com/auth"
	// (chatgpt_account_id/chatgpt_plan_type/...). The id_token does
	// carry a top-level "email", so both shapes are checked.
	var raw struct {
		Subject    string          `json:"sub"`
		Email      string          `json:"email"`
		AuthMethod string          `json:"auth_method,omitempty"`
		Audience   json.RawMessage `json:"aud,omitempty"`
		ExpiresAt  int64           `json:"exp,omitempty"`
		Profile    struct {
			Email string `json:"email"`
		} `json:"https://api.openai.com/profile"`
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Claims{}, err
	}
	email := raw.Email
	if email == "" {
		email = raw.Profile.Email
	}
	c := Claims{
		Subject:          raw.Subject,
		Email:            email,
		AuthMethod:       raw.AuthMethod,
		ExpiresAt:        raw.ExpiresAt,
		ChatGPTAccountID: raw.Auth.ChatGPTAccountID,
	}
	if len(raw.Audience) > 0 {
		var single string
		if err := json.Unmarshal(raw.Audience, &single); err == nil {
			c.Audience = []string{single}
		} else {
			_ = json.Unmarshal(raw.Audience, &c.Audience)
		}
	}
	return c, nil
}
