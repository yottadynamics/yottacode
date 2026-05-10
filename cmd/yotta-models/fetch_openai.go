package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/yottadynamics/yottacode/internal/catalog"
)

// fetchOpenAI queries https://api.openai.com/v1/models. The response
// is intentionally minimal — only id, created, owned_by — so the
// resulting catalog rows are sparse. The endpoint also returns every
// model on the platform: chat, embeddings, moderation, tts, image,
// audio, fine-tunes. We filter to chat-capable IDs via prefix regex.
//
// The regex below is the "soft contract" with future model families.
// When OpenAI ships a new prefix (gpt-6, o5, …) this is a one-line
// widen. Any false-negative is recoverable via free-text input in the
// wizard, so we err on the side of strict matching.
var openAIChatModelRegex = regexp.MustCompile(`^(gpt-|o[134](-|$)|chatgpt-)`)

type openaiModel struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
}

type openaiResp struct {
	Data []openaiModel `json:"data"`
}

func fetchOpenAI(ctx context.Context, apiKey string) ([]catalog.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.openai.com/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	var resp openaiResp
	if err := doJSON(req, &resp); err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	out := make([]catalog.Model, 0, len(resp.Data))
	for _, m := range resp.Data {
		if !openAIChatModelRegex.MatchString(m.ID) {
			continue
		}
		out = append(out, catalog.Model{
			ID:         m.ID,
			Provider:   "openai",
			ReleasedAt: time.Unix(m.Created, 0).UTC(),
			// DisplayName, ContextWindow, MaxOutput, capabilities all
			// left zero/nil — OpenAI's list endpoint doesn't surface
			// any of them. The picker renders "—" for missing fields.
		})
	}
	return out, nil
}
