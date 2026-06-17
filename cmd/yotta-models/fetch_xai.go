package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/catalog"
)

// xAIChatModelRegex keeps the xAI catalog focused on text/chat models.
// xAI's OpenAI-compatible /models endpoint can also list image and video
// generation surfaces; those are not valid chat-completions targets for
// yottacode, so exclude the obvious media families below after matching the
// Grok namespace.
var xAIChatModelRegex = regexp.MustCompile(`^grok-`)

type xaiModel struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
}

type xaiResp struct {
	Data []xaiModel `json:"data"`
}

// fetchXAI queries xAI's OpenAI-compatible list-models endpoint. The response
// shape mirrors OpenAI's sparse {data:[{id, created}]} envelope, so most
// metadata is filled later from models.dev when available.
func fetchXAI(ctx context.Context, apiKey string) ([]catalog.Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.x.ai/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	var resp xaiResp
	if err := doJSON(req, &resp); err != nil {
		return nil, fmt.Errorf("xai: %w", err)
	}
	out := make([]catalog.Model, 0, len(resp.Data))
	for _, m := range resp.Data {
		if !isXAIChatModel(m.ID) {
			continue
		}
		row := catalog.Model{
			ID:       m.ID,
			Provider: "xai",
			// xAI's list endpoint doesn't currently surface context window,
			// max output, or per-model capabilities. The refresh flow backfills
			// context windows from models.dev where that catalog has a match.
		}
		if m.Created > 0 {
			row.ReleasedAt = time.Unix(m.Created, 0).UTC()
		}
		out = append(out, row)
	}
	return out, nil
}

func isXAIChatModel(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	if !xAIChatModelRegex.MatchString(id) {
		return false
	}
	return !strings.Contains(id, "image") &&
		!strings.Contains(id, "imagine") &&
		!strings.Contains(id, "video")
}
