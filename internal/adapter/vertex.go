package adapter

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/openai/openai-go/option"

	vertexauth "github.com/yottadynamics/yottacode/internal/auth/vertex"
)

// Google Vertex AI serves Gemini and Claude from the user's own GCP
// project. Both families share a host, a project path, and an access
// token, but nothing else: Gemini rides an OpenAI-compatible chat shim
// mounted at .../endpoints/openapi, while Claude rides :streamRawPredict
// speaking the native Anthropic Messages API. So there are two kinds, and
// each one reuses the adapter that already speaks its wire format —
// chatAdapter here, anthropicAdapter in vertex_anthropic.go. Neither
// needs new streaming code.
//
// This file holds what the two share — the token-source seam and the auth
// middleware — plus the Gemini kind itself.

// vertexTokenSource is the slice of *vertexauth.TokenSource the adapters
// need. An interface rather than the concrete type so tests can inject a
// canned token without touching Application Default Credentials — the
// same seam newCopilotAdapterFor uses.
type vertexTokenSource interface {
	Token(ctx context.Context) (string, error)
}

// newVertexAdapter builds the Gemini-on-Vertex adapter: a plain
// OpenAI-compatible chat client pointed at the project's openapi shim,
// with the static bearer swapped for a per-request ADC token.
//
// The shim carries reasoning_effort and reports
// completion_tokens_details.reasoning_tokens, both of which chatAdapter
// already handles, so there is nothing Gemini-specific to do here.
func newVertexAdapter(cfg Config) Client {
	return newVertexAdapterFor(cfg, vertexauth.NewTokenSource())
}

func newVertexAdapterFor(cfg Config, src vertexTokenSource) Client {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return newErroredAdapter(cfg, ProviderVertex, fmt.Errorf(
			"vertex: base_url is required — expected %s", vertexGeminiBaseURLShape))
	}
	cfg = pinVertexProvider(cfg, ProviderVertex)
	cfg.Model = normalizeVertexGeminiModel(cfg.Model)
	return newChatAdapter(cfg, option.WithMiddleware(vertexAuthMiddleware(src)))
}

func normalizeVertexGeminiModel(model string) string {
	model = strings.TrimSpace(model)
	if strings.HasPrefix(model, "gemini") {
		return "google/" + model
	}
	return model
}

// pinVertexProvider stamps the resolved kind onto the config so the
// adapter's profile can't disagree with the adapter you actually got.
//
// Both Vertex kinds delegate to a constructor that rebuilds the profile
// from cfg, and buildProfile falls back to sniffing base_url. Reaching
// this function means the router already decided — re-deriving from the
// URL can only lose that, and does whenever the host isn't literally
// aiplatform.googleapis.com: a private endpoint, a test server, or a
// future regional form. The profile drives the status-bar label, usage
// reporting, and whether reasoning effort is sent at all, so a wrong
// answer here is silent, not loud.
func pinVertexProvider(cfg Config, p Provider) Config {
	cfg.ProviderOverride = p
	return cfg
}

const vertexGeminiBaseURLShape = "https://us-central1-aiplatform.googleapis.com/v1/projects/PROJECT/locations/us-central1/endpoints/openapi"

// vertexAuthMiddleware stamps a fresh ADC bearer on every request.
//
// This is why Vertex cannot be an openai-compatible provider: that kind
// resolves its credential once at construction via option.WithAPIKey,
// but an ADC token expires in ~1h, so a long session would start 401-ing
// mid-conversation. Middleware runs per request with the live
// *http.Request, so the token is minted at send time instead.
//
// The func type deliberately matches both openai-go's and
// anthropic-sdk-go's option.Middleware — each is an alias of this same
// signature — so one definition wires into both Vertex kinds. See
// recordRateLimitMiddleware for the same trick.
func vertexAuthMiddleware(src vertexTokenSource) func(*http.Request, func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	return func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		tok, err := src.Token(req.Context())
		if err != nil {
			return nil, err
		}
		// Both SDKs render an API key into a header of their own from
		// cfg.APIKey or their vendor env var ($ANTHROPIC_API_KEY,
		// $OPENAI_API_KEY). A user who has those set for other providers
		// would otherwise send them to Google on every Vertex call.
		req.Header.Del("X-Api-Key")
		req.Header.Set("Authorization", "Bearer "+tok)
		return next(req)
	}
}

// (splitVertexBaseURL lives in vertex_anthropic.go — only that kind
// assembles its own request path.)
