package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	coderacp "github.com/coder/acp-go-sdk"
)

// TestACPSmoke drives a real, compiled `yottacode acp` subprocess over
// its actual stdio pipes — the one layer none of internal/acp's
// in-process wire-level tests can reach, since those wire a Server
// directly to io.Pipe rather than exercising cobra flag resolution, a
// real os/exec child, and real stdin/stdout framing. Skipped under
// -short (like internal/lsp's real-server smoke tests); run explicitly
// via `go test -run TestACPSmoke ./cmd/yottacode` — see
// .github/workflows/go.yml's acp-smoke job.
//
// Fully hermetic: builds its own binary into a temp dir and serves its
// own stub OpenAI-compatible backend, so it needs no network access or
// real provider credentials, and leaves nothing behind.
func TestACPSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess smoke test — run without -short (see .github/workflows/go.yml's acp-smoke job)")
	}

	bin := buildSmokeBinary(t)
	baseURL := startSmokeStubBackend(t)
	workdir := t.TempDir()

	cmd := exec.Command(bin, "acp", "--model", "stub-model", "--base-url", baseURL+"/v1", "--api-key", "sk-stub")
	cmd.Dir = workdir
	// The ACP subprocess must be fully hermetic: config discovery uses
	// os.UserHomeDir(), so isolate HOME as well as the working directory
	// or a developer's real ~/.yottacode/config.toml can change the
	// smoke test outcome before the stub backend is ever reached.
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s acp: %v", bin, err)
	}
	t.Cleanup(func() {
		if stderr.Len() > 0 {
			t.Logf("yottacode acp stderr:\n%s", stderr.String())
		}
	})

	client := &smokeClient{}
	clientConn := coderacp.NewClientSideConnection(client, stdin, stdout)
	clientConn.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --- initialize: capabilities/auth ---
	initResp, err := clientConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if initResp.ProtocolVersion != coderacp.ProtocolVersionNumber {
		t.Errorf("protocolVersion = %v, want %v", initResp.ProtocolVersion, coderacp.ProtocolVersionNumber)
	}
	if !initResp.AgentCapabilities.LoadSession {
		t.Error("agentCapabilities.loadSession = false, want true")
	}
	mcpCaps := initResp.AgentCapabilities.McpCapabilities
	if !mcpCaps.Http || !mcpCaps.Sse {
		t.Errorf("agentCapabilities.mcpCapabilities = %+v, want http=true sse=true", mcpCaps)
	}
	if len(initResp.AuthMethods) < 1 {
		t.Error("authMethods is empty — the ACP Registry requires at least one entry")
	}

	// --- session/new: sessionId + initial configOptions ---
	newResp, err := clientConn.NewSession(ctx, coderacp.NewSessionRequest{Cwd: workdir, McpServers: []coderacp.McpServer{}})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	sessionID := newResp.SessionId
	if sessionID == "" {
		t.Fatal("session/new returned an empty sessionId")
	}
	effortOpt := findEffortOption(newResp.ConfigOptions)
	if effortOpt == nil {
		t.Fatal("session/new's configOptions is missing the \"effort\" select option")
	}
	if effortOpt.CurrentValue != "default" {
		t.Errorf("effort configOption currentValue = %q, want \"default\"", effortOpt.CurrentValue)
	}

	// --- available_commands_update, pushed as a session/update right after session/new ---
	commandNames := commandNamesFromUpdates(client.updatesSince(0))
	if len(commandNames) != 9 {
		t.Errorf("available_commands_update carried %d commands, want 9: %v", len(commandNames), commandNames)
	}

	// --- session/set_config_option: effort -> high ---
	setResp, err := clientConn.SetSessionConfigOption(ctx, coderacp.SetSessionConfigOptionRequest{
		ValueId: &coderacp.SetSessionConfigOptionValueId{
			SessionId: sessionID,
			ConfigId:  "effort",
			Value:     "high",
		},
	})
	if err != nil {
		t.Fatalf("session/set_config_option: %v", err)
	}
	if got := findEffortOption(setResp.ConfigOptions); got == nil || got.CurrentValue != "high" {
		t.Errorf("session/set_config_option response effort currentValue = %+v, want \"high\"", got)
	}

	// --- session/prompt: streamed content + stopReason ---
	checkpoint := len(client.Updates())
	promptResp, err := clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("Say hello in one short sentence.")},
	})
	if err != nil {
		t.Fatalf("session/prompt: %v", err)
	}
	if promptResp.StopReason != coderacp.StopReasonEndTurn {
		t.Errorf("stopReason = %q, want %q", promptResp.StopReason, coderacp.StopReasonEndTurn)
	}
	streamed := agentTextFromUpdates(client.updatesSince(checkpoint))
	if streamed == "" {
		t.Error("no agent_message_chunk content streamed for the prompt")
	}

	// --- slash-macro substitution: /git-commit must NOT reach the model as literal text ---
	checkpoint = len(client.Updates())
	if _, err := clientConn.Prompt(ctx, coderacp.PromptRequest{
		SessionId: sessionID,
		Prompt:    []coderacp.ContentBlock{coderacp.TextBlock("/git-commit")},
	}); err != nil {
		t.Fatalf("session/prompt(/git-commit): %v", err)
	}
	macroText := agentTextFromUpdates(client.updatesSince(checkpoint))
	if strings.Contains(macroText, "'/git-commit'") {
		t.Errorf("literal \"/git-commit\" text reached the model — slash-macro substitution did not happen; got %q", macroText)
	}
	if !strings.Contains(macroText, "Commit the currently staged changes") {
		t.Errorf("expected the git-commit macro's built directive in the model's echoed input; got %q", macroText)
	}

	// --- session/close, then verify the subprocess actually exits ---
	if _, err := clientConn.CloseSession(ctx, coderacp.CloseSessionRequest{SessionId: sessionID}); err != nil {
		t.Fatalf("session/close: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Errorf("yottacode acp exited with an error after stdin closed: %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("yottacode acp did not shut down within 10s of stdin closing")
	}
}

// buildSmokeBinary compiles the current cmd/yottacode package (`go test`
// already runs with this package's directory as its working directory)
// into a throwaway temp binary.
func buildSmokeBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "yottacode-acp-smoketest")
	build := exec.Command("go", "build", "-o", bin, ".")
	out, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// startSmokeStubBackend serves a minimal OpenAI-compatible /models +
// streaming /chat/completions pair — enough for agentruntime.Builder's
// preflight probe and a real (fake) streamed reply, no real provider or
// network access required. The reply echoes the incoming user message
// back (truncated) so the test can assert on what actually reached the
// "model" — the same technique scripts/smoke_test_acp.py used before
// this file replaced it.
func startSmokeStubBackend(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"stub-model"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		var userText string
		for _, m := range payload.Messages {
			if m.Role == "user" {
				userText = m.Content
			}
		}
		if len(userText) > 200 {
			userText = userText[:200]
		}
		reply := fmt.Sprintf("Smoke test OK — I received: %q", userText)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		created := time.Now().Unix()
		send := func(delta map[string]any, finish *string) {
			chunk := map[string]any{
				"id": "chatcmpl-smoketest", "object": "chat.completion.chunk",
				"created": created, "model": "stub-model",
				"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
			}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			if flusher != nil {
				flusher.Flush()
			}
		}
		send(map[string]any{"role": "assistant", "content": ""}, nil)
		for word := range strings.FieldsSeq(reply) {
			send(map[string]any{"content": word + " "}, nil)
		}
		stop := "stop"
		send(map[string]any{}, &stop)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func findEffortOption(options []coderacp.SessionConfigOption) *coderacp.SessionConfigOptionSelect {
	for _, o := range options {
		if o.Select != nil && o.Select.Id == "effort" {
			return o.Select
		}
	}
	return nil
}

func commandNamesFromUpdates(updates []coderacp.SessionNotification) []string {
	var names []string
	for _, u := range updates {
		if u.Update.AvailableCommandsUpdate != nil {
			for _, c := range u.Update.AvailableCommandsUpdate.AvailableCommands {
				names = append(names, c.Name)
			}
		}
	}
	return names
}

func agentTextFromUpdates(updates []coderacp.SessionNotification) string {
	var b strings.Builder
	for _, u := range updates {
		if u.Update.AgentMessageChunk != nil && u.Update.AgentMessageChunk.Content.Text != nil {
			b.WriteString(u.Update.AgentMessageChunk.Content.Text.Text)
		}
	}
	return b.String()
}

// smokeClient is a minimal coderacp.Client for driving a real yottacode
// acp subprocess — trimmed down from internal/acp's own in-process
// fakeClient test fixture (server_test.go) to what this smoke test
// needs: record session/update notifications, and auto-approve any
// permission request so a stray approval-gated tool call can't deadlock
// the run instead of failing it visibly.
type smokeClient struct {
	mu      sync.Mutex
	updates []coderacp.SessionNotification
}

var _ coderacp.Client = (*smokeClient)(nil)

func (c *smokeClient) Updates() []coderacp.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]coderacp.SessionNotification(nil), c.updates...)
}

// updatesSince returns the updates recorded after index n — the same
// "checkpoint, then diff" pattern used to scope streamed content to one
// specific session/prompt call when the client's update log spans
// several calls on the same session.
func (c *smokeClient) updatesSince(n int) []coderacp.SessionNotification {
	all := c.Updates()
	if n >= len(all) {
		return nil
	}
	return all[n:]
}

func (c *smokeClient) ReadTextFile(context.Context, coderacp.ReadTextFileRequest) (coderacp.ReadTextFileResponse, error) {
	return coderacp.ReadTextFileResponse{}, coderacp.NewMethodNotFound("fs/read_text_file")
}

func (c *smokeClient) WriteTextFile(context.Context, coderacp.WriteTextFileRequest) (coderacp.WriteTextFileResponse, error) {
	return coderacp.WriteTextFileResponse{}, coderacp.NewMethodNotFound("fs/write_text_file")
}

func (c *smokeClient) RequestPermission(_ context.Context, params coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	for _, opt := range params.Options {
		if opt.Kind == coderacp.PermissionOptionKindAllowOnce {
			return coderacp.RequestPermissionResponse{Outcome: coderacp.RequestPermissionOutcome{
				Selected: &coderacp.RequestPermissionOutcomeSelected{OptionId: opt.OptionId},
			}}, nil
		}
	}
	return coderacp.RequestPermissionResponse{}, nil
}

func (c *smokeClient) SessionUpdate(_ context.Context, params coderacp.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, params)
	c.mu.Unlock()
	return nil
}

func (c *smokeClient) CreateTerminal(context.Context, coderacp.CreateTerminalRequest) (coderacp.CreateTerminalResponse, error) {
	return coderacp.CreateTerminalResponse{}, coderacp.NewMethodNotFound("terminal/create")
}

func (c *smokeClient) KillTerminal(context.Context, coderacp.KillTerminalRequest) (coderacp.KillTerminalResponse, error) {
	return coderacp.KillTerminalResponse{}, coderacp.NewMethodNotFound("terminal/kill")
}

func (c *smokeClient) TerminalOutput(context.Context, coderacp.TerminalOutputRequest) (coderacp.TerminalOutputResponse, error) {
	return coderacp.TerminalOutputResponse{}, coderacp.NewMethodNotFound("terminal/output")
}

func (c *smokeClient) ReleaseTerminal(context.Context, coderacp.ReleaseTerminalRequest) (coderacp.ReleaseTerminalResponse, error) {
	return coderacp.ReleaseTerminalResponse{}, coderacp.NewMethodNotFound("terminal/release")
}

func (c *smokeClient) WaitForTerminalExit(context.Context, coderacp.WaitForTerminalExitRequest) (coderacp.WaitForTerminalExitResponse, error) {
	return coderacp.WaitForTerminalExitResponse{}, coderacp.NewMethodNotFound("terminal/wait_for_exit")
}
