package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/yottadynamics/yottacode/internal/catalog"
)

// fetchAnthropic queries https://api.anthropic.com/v1/models. Auth is
// x-api-key + the anthropic-version header (a fixed date string, not
// a real version negotiation — Anthropic's docs are explicit about
// this). The response is the richest of the three providers: display
// names, token limits, and a tree of capability flags. We map it 1:1
// onto our common schema.
//
// Pagination is forward-only via after_id. We follow up to maxPages
// for safety; production accounts have well under that today.
const anthropicMaxPages = 5

type anthropicCap struct {
	Supported bool `json:"supported"`
}

type anthropicCaps struct {
	Thinking          anthropicCap `json:"thinking"`
	ImageInput        anthropicCap `json:"image_input"`
	PDFInput          anthropicCap `json:"pdf_input"`
	StructuredOutputs anthropicCap `json:"structured_outputs"`
}

type anthropicModel struct {
	ID             string        `json:"id"`
	Type           string        `json:"type"`
	DisplayName    string        `json:"display_name"`
	CreatedAt      string        `json:"created_at"`
	MaxInputTokens int           `json:"max_input_tokens"`
	MaxTokens      int           `json:"max_tokens"`
	Capabilities   anthropicCaps `json:"capabilities"`
}

type anthropicResp struct {
	Data    []anthropicModel `json:"data"`
	HasMore bool             `json:"has_more"`
	LastID  string           `json:"last_id"`
}

func fetchAnthropic(ctx context.Context, apiKey string) ([]catalog.Model, error) {
	out := make([]catalog.Model, 0, 16)
	cursor := ""
	for page := 0; page < anthropicMaxPages; page++ {
		u, _ := url.Parse("https://api.anthropic.com/v1/models")
		q := u.Query()
		q.Set("limit", "100")
		if cursor != "" {
			q.Set("after_id", cursor)
		}
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		var resp anthropicResp
		if err := doJSON(req, &resp); err != nil {
			return nil, fmt.Errorf("anthropic: %w", err)
		}
		for _, m := range resp.Data {
			out = append(out, mapAnthropicModel(m))
		}
		if !resp.HasMore || resp.LastID == "" {
			return out, nil
		}
		cursor = resp.LastID
	}
	return out, fmt.Errorf("anthropic: pagination cap (%d) hit — bump anthropicMaxPages", anthropicMaxPages)
}

func mapAnthropicModel(m anthropicModel) catalog.Model {
	released, _ := time.Parse(time.RFC3339, m.CreatedAt)
	return catalog.Model{
		ID:            m.ID,
		DisplayName:   m.DisplayName,
		Provider:      "anthropic",
		ContextWindow: m.MaxInputTokens,
		MaxOutput:     m.MaxTokens,
		ReleasedAt:    released.UTC(),
		Capabilities: catalog.Capabilities{
			Thinking:          boolPtr(m.Capabilities.Thinking.Supported),
			Vision:            boolPtr(m.Capabilities.ImageInput.Supported),
			PDF:               boolPtr(m.Capabilities.PDFInput.Supported),
			StructuredOutputs: boolPtr(m.Capabilities.StructuredOutputs.Supported),
			// Anthropic doesn't advertise tool support per-model in the
			// list response — every chat-capable Anthropic model has
			// supported tool use today, so leave it nil rather than
			// asserting either way.
		},
	}
}
