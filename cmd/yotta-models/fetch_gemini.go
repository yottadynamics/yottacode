package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/yottadynamics/yottacode/internal/catalog"
)

// fetchGemini queries https://generativelanguage.googleapis.com/v1beta/models.
// Auth via x-goog-api-key header (not the ?key= query param) so the
// secret doesn't end up in proxy or curl-history logs. The endpoint
// returns generative + embedding + countTokens-only entries together;
// we filter to those whose supportedGenerationMethods contains
// "generateContent" — the API's own signal for "this is a chat
// model," not our heuristic.
//
// Pagination via pageToken; we cap iterations the same way as
// Anthropic (5 pages safety).
const geminiMaxPages = 5

type geminiModel struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
	OutputTokenLimit           int      `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	Thinking                   bool     `json:"thinking"`
}

type geminiResp struct {
	Models        []geminiModel `json:"models"`
	NextPageToken string        `json:"nextPageToken"`
}

func fetchGemini(ctx context.Context, apiKey string) ([]catalog.Model, error) {
	out := make([]catalog.Model, 0, 32)
	pageToken := ""
	for page := 0; page < geminiMaxPages; page++ {
		u, _ := url.Parse("https://generativelanguage.googleapis.com/v1beta/models")
		q := u.Query()
		q.Set("pageSize", "200")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-goog-api-key", apiKey)

		var resp geminiResp
		if err := doJSON(req, &resp); err != nil {
			return nil, fmt.Errorf("gemini: %w", err)
		}
		for _, m := range resp.Models {
			if !supportsGenerate(m.SupportedGenerationMethods) {
				continue
			}
			out = append(out, mapGeminiModel(m))
		}
		if resp.NextPageToken == "" {
			return out, nil
		}
		pageToken = resp.NextPageToken
	}
	return out, fmt.Errorf("gemini: pagination cap (%d) hit — bump geminiMaxPages", geminiMaxPages)
}

func supportsGenerate(methods []string) bool {
	for _, m := range methods {
		if m == "generateContent" {
			return true
		}
	}
	return false
}

func mapGeminiModel(m geminiModel) catalog.Model {
	id := strings.TrimPrefix(m.Name, "models/")
	return catalog.Model{
		ID:            id,
		DisplayName:   m.DisplayName,
		Provider:      "gemini",
		ContextWindow: m.InputTokenLimit,
		MaxOutput:     m.OutputTokenLimit,
		Description:   m.Description,
		Capabilities: catalog.Capabilities{
			Thinking: boolPtr(m.Thinking),
			// Vision/PDF/StructuredOutputs/Tools left nil — Gemini's
			// listing doesn't carry per-model flags for those, and
			// supportedGenerationMethods is too coarse to derive them
			// reliably (a method named "generateContent" doesn't tell
			// us whether the model accepts images). The /model info
			// view will show "—" rather than guessing.
		},
	}
}
