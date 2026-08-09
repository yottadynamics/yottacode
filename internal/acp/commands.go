package acp

import (
	"context"
	"strings"

	coderacp "github.com/coder/acp-go-sdk"

	"github.com/yottadynamics/yottacode/internal/promptmacros"
)

// availableCommands translates every internal/promptmacros.Macro into
// an ACP AvailableCommand — the same 9 bucket-A slash commands
// internal/tui registers in commands.go's allSlash, now advertised to
// an ACP client via session/update so its own command palette (e.g.
// Zed's "/" menu) can offer them.
func availableCommands() []coderacp.AvailableCommand {
	macros := promptmacros.All()
	out := make([]coderacp.AvailableCommand, 0, len(macros))
	for _, m := range macros {
		cmd := coderacp.AvailableCommand{Name: m.Name, Description: m.Description}
		if m.ArgHint != "" {
			cmd.Input = &coderacp.AvailableCommandInput{Unstructured: &coderacp.UnstructuredCommandInput{Hint: m.ArgHint}}
		}
		out = append(out, cmd)
	}
	return out
}

// sendAvailableCommands notifies the client which prompt-macro slash
// commands this session supports. Called once right after
// NewSession/LoadSession registers the session, before the response
// goes back — mirrors how CurrentModeUpdate is pushed via
// session/update rather than folded into the session/new response
// itself (see modes.go).
func (s *Server) sendAvailableCommands(ctx context.Context, sessionID string) {
	if s.conn == nil {
		return
	}
	_ = s.conn.SessionUpdate(ctx, coderacp.SessionNotification{
		SessionId: coderacp.SessionId(sessionID),
		Update: coderacp.SessionUpdate{
			AvailableCommandsUpdate: &coderacp.SessionAvailableCommandsUpdate{
				AvailableCommands: availableCommands(),
			},
		},
	})
}

// matchMacroCommand checks whether text is a `/<name> [args...]`
// invocation of a registered promptmacros.Macro. Only an exact,
// registered macro name after the slash matches — plain text that
// happens to start with "/" for some other reason (a path, a
// site-relative URL fragment the model was asked about) falls through
// unchanged, same as internal/tui's slash dispatcher only recognizing
// registered command names.
func matchMacroCommand(text string) (promptmacros.Macro, []string, bool) {
	if !strings.HasPrefix(text, "/") {
		return promptmacros.Macro{}, nil, false
	}
	fields := strings.Fields(text[1:])
	if len(fields) == 0 {
		return promptmacros.Macro{}, nil, false
	}
	m, ok := promptmacros.Get(fields[0])
	if !ok {
		return promptmacros.Macro{}, nil, false
	}
	return m, fields[1:], true
}
