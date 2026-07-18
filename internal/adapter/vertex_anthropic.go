package adapter

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	vertexauth "github.com/yottadynamics/yottacode/internal/auth/vertex"
)

// vertexAnthropicVersion is the API contract Vertex expects in the body
// in place of the model field. Google versions the Anthropic surface
// separately from Anthropic's own anthropic-version header, and rejects
// requests that carry neither.
const vertexAnthropicVersion = "vertex-2023-10-16"

const vertexAnthropicBaseURLShape = "https://aiplatform.googleapis.com/v1/projects/PROJECT/locations/global"

// newVertexAnthropicAdapter builds the Claude-on-Vertex adapter.
//
// Vertex serves Claude over the byte-identical Anthropic Messages API —
// same request body, same SSE event stream — so this reuses
// anthropicAdapter wholesale and changes only how the request is
// addressed and signed:
//
//   - the model moves from the body into the URL
//   - anthropic_version replaces it in the body
//   - x-api-key becomes an ADC bearer, minted per request
//
// Everything downstream (splitForAnthropic, toAnthropicTools, thinking,
// cache control, usage) is untouched.
//
// Note this is NOT the OpenAI-compatible shim that serves Gemini
// (see vertex.go): that shim is Gemini-only and cannot serve Claude in
// any region.
func newVertexAnthropicAdapter(cfg Config) Client {
	return newVertexAnthropicAdapterFor(cfg, vertexauth.NewTokenSource())
}

func newVertexAnthropicAdapterFor(cfg Config, src vertexTokenSource) Client {
	cfg = pinVertexProvider(cfg, ProviderVertexAnthropic)
	profile := buildProfile(cfg, false)
	origin, pathPrefix, err := splitVertexBaseURL(cfg.BaseURL)
	if err != nil {
		// Fail at construction with the config error rather than letting
		// a malformed URL become a 404 from Google mid-turn.
		return newErroredAdapter(cfg, ProviderVertexAnthropic, err)
	}
	// Built from scratch rather than layered onto newAnthropicAdapter's
	// defaults: no API key belongs on this client, and the base URL is
	// only the origin — the publisher path is assembled per request,
	// since it embeds the model.
	opts := []option.RequestOption{
		option.WithBaseURL(origin),
		option.WithMiddleware(recordRateLimitMiddleware(profile.Provider)),
		option.WithMiddleware(vertexAnthropicMiddleware(pathPrefix)),
		option.WithMiddleware(vertexAuthMiddleware(src)),
	}
	return newAnthropicAdapterWith(cfg, profile, opts...)
}

// splitVertexBaseURL splits a Vertex base_url into the origin the SDK
// should dial and the project/location path prefix requests hang off.
// Only this kind needs it: the Gemini shim is a plain endpoint the SDK can
// use verbatim, whereas the publisher path here is assembled per request.
//
// yottacode carries project and location inside base_url rather than as
// separate config fields — one URL is all either kind needs, and the user
// pastes it straight from the GCP console. The cost is that a malformed
// URL is only caught here, so the errors are worth their length: a 404
// from Google names the resource, not the mistake.
func splitVertexBaseURL(raw string) (origin, pathPrefix string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("vertex-anthropic: base_url is required — expected %s", vertexAnthropicBaseURLShape)
	}
	u, parseErr := url.Parse(raw)
	if parseErr != nil {
		return "", "", fmt.Errorf("vertex-anthropic: base_url %q is not a valid URL: %w", raw, parseErr)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("vertex-anthropic: base_url %q needs a scheme and host — expected %s", raw, vertexAnthropicBaseURLShape)
	}
	p := strings.TrimRight(u.Path, "/")
	// The publisher path is built by appending to this prefix, so the
	// project and location have to already be in it. Checking here turns a
	// puzzling 404 into a config error that names the missing part.
	if !strings.Contains(p, "/projects/") || !strings.Contains(p, "/locations/") {
		return "", "", fmt.Errorf(
			"vertex-anthropic: base_url %q is missing the project/location path — expected %s",
			raw, vertexAnthropicBaseURLShape)
	}
	// Trailing slash matches what Google's own SDK passes as a base URL;
	// the SDKs join their relative request path onto it.
	return u.Scheme + "://" + u.Host + "/", p, nil
}

// vertexAnthropicMiddleware rewrites an Anthropic Messages request into
// the Vertex publisher-model shape.
//
// Modeled on the transform in anthropic-sdk-go's own vertex package. We
// reimplement rather than import it: that package panics on credential
// failure (this codebase surfaces config errors through newErroredAdapter
// instead) and pulls in the google.golang.org/api + gRPC tree for an
// auth path we already cover in ~10 lines of middleware.
//
// Unlike the SDK's version, the path comes from the configured base_url
// rather than a (region, project) pair, so a caller can point at any
// Vertex endpoint — including a test server — without this needing to
// know how Google composes hostnames.
func vertexAnthropicMiddleware(pathPrefix string) func(*http.Request, func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	return func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
		// The SDK addresses every completion at /v1/messages; anything
		// else (a future endpoint, a retry of an already-rewritten
		// request) passes through untouched.
		if req.Body == nil || req.Method != http.MethodPost || req.URL.Path != "/v1/messages" {
			return next(req)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("vertex-anthropic: read request body: %w", err)
		}
		req.Body.Close()

		model := gjson.GetBytes(body, "model").String()
		if model == "" {
			return nil, errors.New("vertex-anthropic: request carries no model")
		}
		// Vertex picks rawPredict vs streamRawPredict from the URL, not
		// from the body — but the body is where the SDK recorded the
		// caller's intent, so read it back out.
		specifier := "rawPredict"
		if gjson.GetBytes(body, "stream").Bool() {
			specifier = "streamRawPredict"
		}

		// Vertex rejects a body carrying model, and rejects one carrying
		// no anthropic_version. Both edits are on the raw JSON because
		// the SDK's typed params have no field for either.
		body, err = sjson.DeleteBytes(body, "model")
		if err != nil {
			return nil, fmt.Errorf("vertex-anthropic: strip model from body: %w", err)
		}
		if !gjson.GetBytes(body, "anthropic_version").Exists() {
			body, err = sjson.SetBytes(body, "anthropic_version", vertexAnthropicVersion)
			if err != nil {
				return nil, fmt.Errorf("vertex-anthropic: set anthropic_version: %w", err)
			}
		}

		req.URL.Path = fmt.Sprintf("%s/publishers/anthropic/models/%s:%s", pathPrefix, model, specifier)

		// Rewind: the SDK retries by calling GetBody, so a one-shot
		// reader here would make every retry send an empty body.
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		req.ContentLength = int64(len(body))
		return next(req)
	}
}
