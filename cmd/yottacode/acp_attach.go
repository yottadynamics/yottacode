package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	coderacp "github.com/coder/acp-go-sdk"
	"github.com/gobwas/ws"
	"github.com/spf13/cobra"

	"github.com/yottadynamics/yottacode/internal/acp/transport"
)

// newAcpAttachCmd creates the minimal terminal recovery client. It uses the
// standard ACP client connection and the same WSS framing adapter as the
// server, so attach does not create a second agent runtime.
func newAcpAttachCmd() *cobra.Command {
	var serverURL, token string
	cmd := &cobra.Command{
		Use:   "attach <session-id>",
		Short: "Attach a terminal ACP client to an existing session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(token) == "" {
				return errors.New("acp attach: --token is required")
			}
			conn, err := dialWSS(cmd.Context(), serverURL, token)
			if err != nil {
				return err
			}
			defer conn.Close()
			client := &terminalACPClient{out: cmd.OutOrStdout()}
			acpConn := coderacp.NewClientSideConnection(client, conn, conn)
			ctx := cmd.Context()
			if _, err := acpConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
				return fmt.Errorf("initialize ACP connection: %w", err)
			}
			if _, err := acpConn.LoadSession(ctx, coderacp.LoadSessionRequest{SessionId: coderacp.SessionId(args[0]), Cwd: "."}); err != nil {
				return fmt.Errorf("load session %s: %w", args[0], err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "attached; enter prompts, Ctrl-D to exit")
			for {
				var prompt string
				if _, err := fmt.Fscanln(cmd.InOrStdin(), &prompt); err != nil {
					if errors.Is(err, io.EOF) {
						return nil
					}
					return err
				}
				if strings.TrimSpace(prompt) == "" {
					continue
				}
				if _, err := acpConn.Prompt(ctx, coderacp.PromptRequest{SessionId: coderacp.SessionId(args[0]), Prompt: []coderacp.ContentBlock{coderacp.TextBlock(prompt)}}); err != nil {
					return err
				}
			}
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "wss://127.0.0.1:8443/acp", "WSS ACP server URL")
	cmd.Flags().StringVar(&token, "token", os.Getenv("YOTTACODE_ACP_TOKEN"), "ACP bearer token")
	return cmd
}

type terminalACPClient struct{ out io.Writer }

func (c *terminalACPClient) ReadTextFile(context.Context, coderacp.ReadTextFileRequest) (coderacp.ReadTextFileResponse, error) {
	return coderacp.ReadTextFileResponse{}, coderacp.NewMethodNotFound("fs/read_text_file")
}
func (c *terminalACPClient) WriteTextFile(context.Context, coderacp.WriteTextFileRequest) (coderacp.WriteTextFileResponse, error) {
	return coderacp.WriteTextFileResponse{}, coderacp.NewMethodNotFound("fs/write_text_file")
}
func (c *terminalACPClient) RequestPermission(_ context.Context, p coderacp.RequestPermissionRequest) (coderacp.RequestPermissionResponse, error) {
	fmt.Fprintf(c.out, "permission required: %s\n", *p.ToolCall.Title)
	return coderacp.RequestPermissionResponse{Outcome: coderacp.NewRequestPermissionOutcomeCancelled()}, nil
}
func (c *terminalACPClient) SessionUpdate(_ context.Context, p coderacp.SessionNotification) error {
	if p.Update.AgentMessageChunk != nil && p.Update.AgentMessageChunk.Content.Text != nil {
		_, _ = io.WriteString(c.out, p.Update.AgentMessageChunk.Content.Text.Text)
	}
	return nil
}
func (c *terminalACPClient) CreateTerminal(context.Context, coderacp.CreateTerminalRequest) (coderacp.CreateTerminalResponse, error) {
	return coderacp.CreateTerminalResponse{}, coderacp.NewMethodNotFound("terminal/create")
}
func (c *terminalACPClient) KillTerminal(context.Context, coderacp.KillTerminalRequest) (coderacp.KillTerminalResponse, error) {
	return coderacp.KillTerminalResponse{}, coderacp.NewMethodNotFound("terminal/kill")
}
func (c *terminalACPClient) TerminalOutput(context.Context, coderacp.TerminalOutputRequest) (coderacp.TerminalOutputResponse, error) {
	return coderacp.TerminalOutputResponse{}, coderacp.NewMethodNotFound("terminal/output")
}
func (c *terminalACPClient) ReleaseTerminal(context.Context, coderacp.ReleaseTerminalRequest) (coderacp.ReleaseTerminalResponse, error) {
	return coderacp.ReleaseTerminalResponse{}, coderacp.NewMethodNotFound("terminal/release")
}
func (c *terminalACPClient) WaitForTerminalExit(context.Context, coderacp.WaitForTerminalExitRequest) (coderacp.WaitForTerminalExitResponse, error) {
	return coderacp.WaitForTerminalExitResponse{}, coderacp.NewMethodNotFound("terminal/wait_for_exit")
}

func dialWSS(ctx context.Context, rawURL, token string) (io.ReadWriteCloser, error) {
	if !strings.HasPrefix(rawURL, "wss://") {
		return nil, errors.New("acp attach: --server must use the wss:// scheme")
	}
	dialer := ws.Dialer{
		Timeout:   10 * time.Second,
		Protocols: []string{transport.ACPSubprotocol},
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		Header:    ws.HandshakeHeaderString(fmt.Sprintf("Authorization: Bearer %s\r\n", token)),
	}
	conn, buffered, _, err := dialer.Dial(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("dial WSS server: %w", err)
	}
	if buffered != nil {
		return transport.NewClientWebSocketStream(&bufferedConn{conn: conn, buffered: buffered}), nil
	}
	return transport.NewClientWebSocketStream(conn), nil
}

type bufferedConn struct {
	conn     net.Conn
	buffered *bufio.Reader
}

func (s *bufferedConn) Read(p []byte) (int, error)  { return s.buffered.Read(p) }
func (s *bufferedConn) Write(p []byte) (int, error) { return s.conn.Write(p) }
func (s *bufferedConn) Close() error                { return s.conn.Close() }

var _ coderacp.Client = (*terminalACPClient)(nil)
