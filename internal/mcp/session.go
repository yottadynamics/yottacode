package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yottadynamics/yottacode/internal/version"
)

const clientName = "yottacode"

// clientVersion is a variable so tests can inject a deterministic handshake
// value while production builds use the same version surface as yottacode
// --version.
var clientVersion = version.Full

const defaultMaxResultBytes = 262144

// sessionOps contains the transport-agnostic MCP JSON-RPC operations shared by
// stdio, streamable HTTP, and SSE clients. Transports own connect/stop; this
// helper owns tools/list, tools/call, schema mapping, and result flattening.
type sessionOps struct {
	mu             sync.RWMutex
	session        *sdk.ClientSession
	started        bool
	starting       bool
	tools          []ToolDescriptor
	maxResultBytes int
}

func (s *sessionOps) bind(session *sdk.ClientSession) {
	s.mu.Lock()
	s.session = session
	s.started = session != nil
	s.starting = false
	if s.maxResultBytes <= 0 {
		s.maxResultBytes = defaultMaxResultBytes
	}
	s.mu.Unlock()
}

func (s *sessionOps) clear() {
	s.mu.Lock()
	s.session = nil
	s.started = false
	s.starting = false
	s.tools = nil
	s.mu.Unlock()
}

func (s *sessionOps) listTools() ([]ToolDescriptor, error) {
	if !s.started {
		return nil, ErrNotStarted
	}
	out := make([]ToolDescriptor, len(s.tools))
	copy(out, s.tools)
	return out, nil
}

func (s *sessionOps) fetchTools(ctx context.Context) ([]ToolDescriptor, error) {
	s.mu.RLock()
	session := s.session
	started := s.started
	s.mu.RUnlock()
	if !started || session == nil {
		return nil, ErrNotStarted
	}
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]ToolDescriptor, 0, len(res.Tools))
	for _, t := range res.Tools {
		if t == nil {
			continue
		}
		td, err := mapTool(t)
		if err != nil {
			return nil, err
		}
		out = append(out, td)
	}
	s.mu.Lock()
	if s.session == session && s.started {
		s.tools = out
	}
	s.mu.Unlock()
	copied := make([]ToolDescriptor, len(out))
	copy(copied, out)
	return copied, nil
}

func (s *sessionOps) callTool(ctx context.Context, server, toolName, argsJSON string) (CallResult, error) {
	s.mu.RLock()
	session := s.session
	started := s.started
	maxResultBytes := s.maxResultBytes
	s.mu.RUnlock()
	return callToolWithSession(ctx, server, toolName, argsJSON, session, started, maxResultBytes)
}

func callToolWithSession(ctx context.Context, server, toolName, argsJSON string, session *sdk.ClientSession, started bool, maxResultBytes int) (CallResult, error) {
	if !started || session == nil {
		return CallResult{}, ErrNotStarted
	}

	var args map[string]any
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return CallResult{}, fmt.Errorf("mcp(%s/%s): invalid argument JSON: %w", server, toolName, err)
		}
	}

	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: toolName, Arguments: args})
	if err != nil {
		return CallResult{}, fmt.Errorf("mcp(%s/%s): %w", server, toolName, err)
	}
	return CallResult{Text: flattenResult(res, maxResultBytes), IsError: res.IsError}, nil
}

func mapTool(t *sdk.Tool) (ToolDescriptor, error) {
	if strings.Contains(t.Name, "/") {
		return ToolDescriptor{}, fmt.Errorf("mcp: tool name %q contains '/' and cannot be namespaced safely", t.Name)
	}
	input, _ := toSchemaMap(t.InputSchema)
	output, _ := toSchemaMap(t.OutputSchema)
	td := ToolDescriptor{
		Name:         t.Name,
		Title:        t.Title,
		Description:  t.Description,
		InputSchema:  input,
		OutputSchema: output,
		Destructive:  true,
		OpenWorld:    true,
	}
	if t.Annotations != nil {
		td.ReadOnlyHint = t.Annotations.ReadOnlyHint
		td.Idempotent = t.Annotations.IdempotentHint
		td.Title = firstNonEmpty(td.Title, t.Annotations.Title)
		if t.Annotations.DestructiveHint != nil {
			td.Destructive = *t.Annotations.DestructiveHint
		}
		if t.Annotations.OpenWorldHint != nil {
			td.OpenWorld = *t.Annotations.OpenWorldHint
		}
	}
	return td, nil
}

// flattenResult converts a CallToolResult to the string result the agent tool
// contract expects. structuredContent is preferred because it preserves typed
// output, non-text content produces explicit omission markers, and the final
// payload is capped before it can enter model context.
func flattenResult(res *sdk.CallToolResult, maxBytes int) string {
	if res == nil {
		return ""
	}
	parts := make([]string, 0, len(res.Content)+1)
	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	if res.StructuredContent == nil {
		for _, c := range res.Content {
			switch tc := c.(type) {
			case *sdk.TextContent:
				if tc != nil {
					parts = append(parts, tc.Text)
				}
			case *sdk.ImageContent:
				if tc != nil {
					parts = append(parts, fmt.Sprintf("[image omitted: %s, %d bytes — yottacode tools-only bridge passes text only]", orDefault(tc.MIMEType, "image/unknown"), len(tc.Data)))
				}
			case *sdk.AudioContent:
				if tc != nil {
					parts = append(parts, fmt.Sprintf("[audio omitted: %s, %d bytes — yottacode tools-only bridge passes text only]", orDefault(tc.MIMEType, "audio/unknown"), len(tc.Data)))
				}
			case *sdk.ResourceLink:
				if tc != nil {
					parts = append(parts, fmt.Sprintf("[resource link omitted: %s (%s) — yottacode does not fetch MCP resources unless resources are explicitly enabled]", tc.URI, orDefault(tc.MIMEType, "unknown")))
				}
			case *sdk.EmbeddedResource:
				if tc != nil {
					uri := "<no uri>"
					if tc.Resource != nil {
						uri = tc.Resource.URI
					}
					parts = append(parts, fmt.Sprintf("[embedded resource omitted: %s — yottacode does not fetch MCP resources unless resources are explicitly enabled]", uri))
				}
			default:
				if c != nil {
					parts = append(parts, fmt.Sprintf("[unsupported content type %T omitted]", c))
				}
			}
		}
		return capResult(strings.Join(parts, "\n"), maxBytes)
	}
	for _, c := range res.Content {
		switch tc := c.(type) {
		case *sdk.TextContent:
			// Text is ignored when structuredContent is present; structured output is
			// the primary payload in C3/C0 shared flattening.
		case *sdk.ImageContent:
			if tc != nil {
				parts = append(parts, fmt.Sprintf("[image omitted: %s, %d bytes — yottacode tools-only bridge passes text only]", orDefault(tc.MIMEType, "image/unknown"), len(tc.Data)))
			}
		case *sdk.AudioContent:
			if tc != nil {
				parts = append(parts, fmt.Sprintf("[audio omitted: %s, %d bytes — yottacode tools-only bridge passes text only]", orDefault(tc.MIMEType, "audio/unknown"), len(tc.Data)))
			}
		case *sdk.ResourceLink:
			if tc != nil {
				parts = append(parts, fmt.Sprintf("[resource link omitted: %s (%s) — yottacode does not fetch MCP resources unless resources are explicitly enabled]", tc.URI, orDefault(tc.MIMEType, "unknown")))
			}
		case *sdk.EmbeddedResource:
			if tc != nil {
				uri := "<no uri>"
				if tc.Resource != nil {
					uri = tc.Resource.URI
				}
				parts = append(parts, fmt.Sprintf("[embedded resource omitted: %s — yottacode does not fetch MCP resources unless resources are explicitly enabled]", uri))
			}
		default:
			if c != nil {
				parts = append(parts, fmt.Sprintf("[unsupported content type %T omitted]", c))
			}
		}
	}
	return capResult(strings.Join(parts, "\n"), maxBytes)
}

// extractText preserves the old test seam while production call paths use
// flattenResult with the configured cap.
func extractText(res *sdk.CallToolResult) string { return flattenResult(res, defaultMaxResultBytes) }

func capResult(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + fmt.Sprintf("\n[truncated: result was %d bytes; yottacode kept the first %d bytes]", len(s), maxBytes)
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// toSchemaMap normalizes the SDK's any-typed schemas into the map shape the
// adapter layer expects.
func toSchemaMap(raw any) (map[string]any, bool) {
	if raw == nil {
		return map[string]any{}, false
	}
	switch v := raw.(type) {
	case map[string]any:
		return v, true
	case json.RawMessage:
		var m map[string]any
		if err := json.Unmarshal(v, &m); err == nil {
			return m, true
		}
	case []byte:
		var m map[string]any
		if err := json.Unmarshal(v, &m); err == nil {
			return m, true
		}
	}
	b, err := json.Marshal(raw)
	if err == nil {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err == nil {
			return m, true
		}
	}
	return map[string]any{}, false
}
