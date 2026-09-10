package tui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	mcppkg "github.com/yottadynamics/yottacode/internal/mcp"
)

// mcpOAuthURLMsg is sent by startMCPOAuthLoginCmd once the loopback
// listener is up and the authorize URL is built. Mirrors
// inlineOpenAIAuthURLMsg's role: the model surfaces the URL to the
// transcript before dispatching the blocking Wait cmd, so a failed
// browser launch doesn't strand the user with no way to copy it.
type mcpOAuthURLMsg struct {
	serverName string
	pending    *mcppkg.PendingOAuthLogin
	err        error
}

// mcpOAuthDoneMsg is sent by waitMCPOAuthLoginCmd when the user
// finishes the browser flow (or it errors out). The token is already
// persisted to ~/.yottacode/mcp-auth/<name>.json on success — see
// internal/mcp.persistingTokenSource.
type mcpOAuthDoneMsg struct {
	// pending identifies which sign-in produced this result, the same
	// way inlineOpenAIAuthDoneMsg.pending does — a superseded attempt's
	// late completion must not clobber a newer one's state.
	pending    *mcppkg.PendingOAuthLogin
	serverName string
	err        error
}

// mcpOAuthLoginTimeout bounds how long an inline `/mcp auth` sign-in may
// stay open. Matches inlineOpenAIAuthLoginTimeout's rationale: long
// enough for a real login (password, 2FA, consent), finite so a
// walked-away-from attempt eventually releases the fixed loopback port.
const mcpOAuthLoginTimeout = 10 * time.Minute

// startMCPOAuthLoginCmd kicks off the synchronous prep phase of the
// OAuth flow for name/serverURL/opts and returns once the authorize URL
// is known. ctx must already carry the mcpOAuthLoginTimeout deadline
// (see cmdMCP's "auth" case, which also stashes ctx's cancel func on
// the model so handleMCPOAuthDone can release it once Wait completes).
func startMCPOAuthLoginCmd(ctx context.Context, name, serverURL string, opts mcppkg.OAuthOptions) tea.Cmd {
	return func() tea.Msg {
		pending, err := mcppkg.StartOAuthLogin(ctx, name, serverURL, opts, &http.Client{})
		return mcpOAuthURLMsg{serverName: name, pending: pending, err: err}
	}
}

// waitMCPOAuthLoginCmd blocks on the user's browser sign-in. The token
// is already persisted by the time Wait returns successfully.
func waitMCPOAuthLoginCmd(ctx context.Context, name string, pending *mcppkg.PendingOAuthLogin) tea.Cmd {
	return func() tea.Msg {
		_, err := pending.Wait(ctx)
		return mcpOAuthDoneMsg{pending: pending, serverName: name, err: err}
	}
}

// handleMCPOAuthURL surfaces the auth URL to the transcript and
// dispatches the blocking wait cmd.
func handleMCPOAuthURL(m Model, msg mcpOAuthURLMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLine(styleError.Render(fmt.Sprintf("[mcp] %s: sign-in failed to start: %s", msg.serverName, mcppkg.Redact(msg.err.Error()))))
		return m, nil
	}
	m.mcpOAuthPending = msg.pending
	m.mcpOAuthPendingName = msg.serverName
	m.appendLine(styleMCPOK.Render(fmt.Sprintf("[mcp] %s: opening browser to sign in", msg.serverName)))
	m.appendLine(styleMCPMeta.Render(fmt.Sprintf("(sign-in expires in %s if left incomplete)", mcpOAuthLoginTimeout)))
	m.appendLine(styleMCPMeta.Render("if it didn't open, paste this URL into your browser:"))
	m.appendLine(stylePathHeader.Render(msg.pending.AuthURL))
	return m, waitMCPOAuthLoginCmd(m.parentCtx, msg.serverName, msg.pending)
}

// handleMCPOAuthDone surfaces the sign-in result. On success the token
// is already on disk; the user still needs `/mcp restart <name>` (or a
// tool call, which lazily triggers auth via the transport's 401 path
// too) to pick it up on an already-running client.
func handleMCPOAuthDone(m Model, msg mcpOAuthDoneMsg) (Model, tea.Cmd) {
	if msg.pending != m.mcpOAuthPending {
		// A superseded/abandoned attempt completing late — ignore.
		return m, nil
	}
	if m.mcpOAuthCancel != nil {
		m.mcpOAuthCancel()
	}
	m.mcpOAuthPending = nil
	m.mcpOAuthPendingName = ""
	m.mcpOAuthCancel = nil
	if msg.err != nil {
		m.appendLine(styleMCPFail.Render(fmt.Sprintf("[mcp] %s: sign-in failed: %s", msg.serverName, mcppkg.Redact(msg.err.Error()))))
		return m, nil
	}
	m.appendLine(styleMCPOK.Render(fmt.Sprintf("[mcp] %s: signed in", msg.serverName)))
	mgr := m.mcpManager
	if mgr != nil && mgr.Client(msg.serverName) != nil {
		m.appendLine(styleMCPMeta.Render(fmt.Sprintf("  run /mcp restart %s to use the new credential on the running connection", msg.serverName)))
	}
	return m, nil
}

// mcpOAuthOptionsFrom resolves a config.MCPServer's oauth_client_id/
// oauth_client_secret $VAR references against the process env — the
// same expansion NewHTTPClient does at connect time — so `/mcp auth`
// sends the real credential, not the literal "$VAR" string.
func mcpOAuthOptionsFrom(scopes []string, clientID, clientSecret string) mcppkg.OAuthOptions {
	return mcppkg.OAuthOptions{
		ClientID:     os.Expand(clientID, os.Getenv),
		ClientSecret: os.Expand(clientSecret, os.Getenv),
		Scopes:       scopes,
	}
}
