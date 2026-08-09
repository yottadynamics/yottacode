package acp

import (
	"context"
	"fmt"
	"strings"
	"time"

	coderacp "github.com/coder/acp-go-sdk"

	openaiauth "github.com/yottadynamics/yottacode/internal/auth/openai"
	vertexauth "github.com/yottadynamics/yottacode/internal/auth/vertex"
	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/cli"
)

// Auth method ids, referenced by both authMethods (the advertised list)
// and Authenticate (the dispatch switch) — kept as constants so the two
// can't drift out of sync with each other.
const (
	authMethodOpenAIChatGPT = "openai-chatgpt"
	authMethodGitHubCopilot = "github-copilot"
	authMethodVertexADC     = "vertex-adc"
)

// authMethods is the fixed AuthMethods list advertised in Initialize.
// The ACP Registry requires at least one method of type "agent" or
// "terminal", CI-verified — see roadmap/acp-adapter.md's Phase 2 notes.
// yottacode offers three:
//
//   - openai-chatgpt is Agent Auth: the whole flow (browser + local
//     OAuth callback server) runs inside the blocking Authenticate RPC
//     call — see authenticateOpenAIChatGPT below.
//   - github-copilot is Terminal Auth: per the registry's
//     AUTHENTICATION.md, this type never calls Authenticate at all —
//     the client re-invokes the agent *binary* with the declared Args,
//     replacing the defaults, and lets it run as an ordinary
//     interactive terminal program. `yottacode copilot-auth login`
//     already *is* that program, so this is pure declaration — no new
//     runtime logic needed.
//   - vertex-adc is also Agent Auth, but a different shape from
//     openai-chatgpt: Vertex has no yottacode-owned login flow at all
//     (internal/auth/vertex/source.go's own doc comment: "Unlike every
//     other provider yottacode talks to, Vertex has no API key").
//     Credential acquisition happens entirely outside yottacode, via
//     `gcloud auth application-default login` or a service-account
//     key file. What the blocking RPC call actually does is verify
//     those already-ambient Application Default Credentials work and
//     scan which configured Vertex models they can reach — the same
//     work `yottacode provider scan` does from the CLI. See
//     authenticateVertex below.
func authMethods() []coderacp.AuthMethod {
	return []coderacp.AuthMethod{
		{Agent: &coderacp.AuthMethodAgent{
			Id:          authMethodOpenAIChatGPT,
			Name:        "Sign in with ChatGPT",
			Description: coderacp.Ptr("OAuth via OpenAI's Sign-in-with-ChatGPT flow; opens your browser."),
		}},
		{Terminal: &coderacp.AuthMethodTerminalInline{
			Id:          authMethodGitHubCopilot,
			Name:        "Sign in with GitHub Copilot",
			Description: coderacp.Ptr("Interactive device-code flow in your terminal."),
			Args:        []string{"copilot-auth", "login"},
		}},
		{Agent: &coderacp.AuthMethodAgent{
			Id:          authMethodVertexADC,
			Name:        "Verify Google Cloud (Vertex AI) access",
			Description: coderacp.Ptr("Checks Application Default Credentials and scans configured Vertex model access; " + vertexauth.SetupHint + " first if this fails."),
		}},
	}
}

// Authenticate dispatches by MethodId. openai-chatgpt and vertex-adc are
// the two Agent Auth methods actually expected to arrive here —
// github-copilot uses Terminal Auth, which bypasses this RPC by design
// (see authMethods above); a client that calls it anyway gets an
// explanatory error rather than a silent no-op.
func (s *Server) Authenticate(ctx context.Context, params coderacp.AuthenticateRequest) (coderacp.AuthenticateResponse, error) {
	switch params.MethodId {
	case authMethodOpenAIChatGPT:
		if err := s.authenticateOpenAI(ctx); err != nil {
			return coderacp.AuthenticateResponse{}, coderacp.NewInternalError(map[string]any{"error": err.Error()})
		}
		return coderacp.AuthenticateResponse{}, nil
	case authMethodVertexADC:
		if err := s.authenticateVertex(ctx); err != nil {
			return coderacp.AuthenticateResponse{}, coderacp.NewInternalError(map[string]any{"error": err.Error()})
		}
		return coderacp.AuthenticateResponse{}, nil
	case authMethodGitHubCopilot:
		return coderacp.AuthenticateResponse{}, coderacp.NewInvalidParams(map[string]any{
			"error": "github-copilot uses ACP Terminal Auth — the client should relaunch the agent binary with the declared args (copilot-auth login), not call authenticate",
		})
	default:
		return coderacp.AuthenticateResponse{}, coderacp.NewInvalidParams(map[string]any{"error": "unknown auth method: " + params.MethodId})
	}
}

// authenticateOpenAIChatGPT is the production implementation of the
// "openai-chatgpt" Agent Auth method: the same three steps
// `yottacode openai-auth login` runs (internal/auth/openai already
// exports all three — no new function needed), replicated here because
// newOpenAIAuthAdapter (internal/adapter/openai_auth.go) requires the
// models file the third step produces to exist before it will construct
// successfully. A session built right after this call must find both
// the token store and the models file already in place.
func authenticateOpenAIChatGPT(ctx context.Context) error {
	storePath, err := openaiauth.DefaultStorePath()
	if err != nil {
		return fmt.Errorf("resolve token store path: %w", err)
	}
	ts, err := openaiauth.Login(ctx, openaiauth.LoginOptions{})
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if err := openaiauth.Save(storePath, ts); err != nil {
		return fmt.Errorf("save tokens: %w", err)
	}
	if _, err := openaiauth.ScanAndPersistWithOptions(ctx, ts.AccessToken, openaiauth.ScanOptions{}); err != nil {
		return fmt.Errorf("scan available models: %w", err)
	}
	return nil
}

// authenticateVertex is the production implementation of the
// "vertex-adc" Agent Auth method. Unlike authenticateOpenAIChatGPT,
// there is no credential to acquire here — Vertex authenticates via
// Application Default Credentials that live entirely outside
// yottacode (gcloud, a service-account key, or the GCE/Cloud Run
// metadata server). "Authenticating" therefore means: prove those
// ambient credentials actually work, and persist a scan of which of
// this session's configured Vertex models they can reach — the exact
// same work `yottacode provider scan NAME` does from the CLI
// (cmd/yottacode/provider.go's newProviderScanCmd), just driven by
// this process's --provider-kind/--base-url flags instead of a named
// config.toml provider entry, since that's all a `yottacode acp`
// process has.
//
// A scan that finds zero accessible models is still a SUCCESSFUL
// authentication — vertexauth.Scan only fails when the credential
// itself can't be obtained (TokenSource.Token, wrapping
// vertexauth.SetupHint into the error), not when IAM denies every
// candidate model. Per-model access is a separate, later concern.
func authenticateVertex(ctx context.Context, opts cli.ChatOptions) error {
	kind := strings.TrimSpace(opts.ProviderKind)
	if kind != vertexauth.ProviderVertex && kind != vertexauth.ProviderVertexAnthropic {
		return fmt.Errorf("this session is not configured for a Vertex provider (--provider-kind is %q, want %q or %q) — nothing to authenticate",
			opts.ProviderKind, vertexauth.ProviderVertex, vertexauth.ProviderVertexAnthropic)
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return fmt.Errorf("this session has no --base-url configured — Vertex requires the project/location endpoint")
	}

	entries := catalog.Curated(kind)
	candidates := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e.ID) != "" {
			candidates = append(candidates, e.ID)
		}
	}
	if len(candidates) == 0 {
		return fmt.Errorf("no curated Vertex models to scan for provider kind %q", kind)
	}

	// Same per-candidate budget newProviderScanCmd uses, derived from
	// the RPC's own ctx rather than a fresh background one so a client
	// that cancels the authenticate call still aborts promptly.
	scanCtx, cancel := context.WithTimeout(ctx, time.Duration(len(candidates))*30*time.Second)
	defer cancel()

	_, _, err := vertexauth.ScanAndPersist(scanCtx, vertexauth.ScanOptions{
		Provider:   kind,
		BaseURL:    opts.BaseURL,
		Candidates: candidates,
	})
	return err
}
