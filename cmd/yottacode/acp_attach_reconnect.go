package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	coderacp "github.com/coder/acp-go-sdk"
)

type attachOptions struct {
	serverURL string
	token     string
	role      string
	takeover  bool
	sessionID string
	afterSeq  uint64
	client    *terminalACPClient
}

func (o attachOptions) connect(ctx context.Context) (*coderacp.ClientSideConnection, io.ReadWriteCloser, uint64, error) {
	conn, err := dialWSS(ctx, o.serverURL, o.token)
	if err != nil {
		return nil, nil, o.afterSeq, err
	}
	acpConn := coderacp.NewClientSideConnection(o.client, conn, conn)
	if _, err := acpConn.Initialize(ctx, coderacp.InitializeRequest{ProtocolVersion: coderacp.ProtocolVersionNumber}); err != nil {
		conn.Close()
		return nil, nil, o.afterSeq, fmt.Errorf("initialize ACP connection: %w", err)
	}
	role := o.role
	if o.takeover {
		role = "controller"
	}
	params, _ := json.Marshal(map[string]any{"sessionId": o.sessionID, "role": role, "takeover": o.takeover})
	raw, err := acpConn.CallExtension(ctx, "_yottacode/session/attach", params)
	if err != nil {
		conn.Close()
		return nil, nil, o.afterSeq, fmt.Errorf("attach session %s: %w", o.sessionID, err)
	}
	var attached struct {
		LatestSeq uint64 `json:"latestSeq"`
	}
	_ = json.Unmarshal(raw, &attached)
	params, _ = json.Marshal(map[string]any{"sessionId": o.sessionID, "afterSeq": o.afterSeq})
	if _, err := acpConn.CallExtension(ctx, "_yottacode/session/replay", params); err != nil {
		conn.Close()
		return nil, nil, o.afterSeq, fmt.Errorf("replay session %s: %w", o.sessionID, err)
	}
	if attached.LatestSeq > o.afterSeq {
		o.afterSeq = attached.LatestSeq
	}
	return acpConn, conn, o.afterSeq, nil
}

func runAttach(ctx context.Context, input io.Reader, output io.Writer, opts attachOptions) error {
	attempt := 0
	var acpConn *coderacp.ClientSideConnection
	var conn io.ReadWriteCloser
	for {
		connected, stream, cursor, err := opts.connect(ctx)
		if err == nil {
			acpConn, conn, opts.afterSeq, attempt = connected, stream, cursor, 0
			break
		}
		if attempt >= 5 {
			return err
		}
		timer := time.NewTimer(reconnectBackoff(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		attempt++
	}
	defer conn.Close()
	fmt.Fprintln(output, "attached; enter prompts, Ctrl-D to exit")
	reader := bufio.NewReader(input)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		prompt := strings.TrimSpace(line)
		if prompt == "" && errors.Is(err, io.EOF) {
			return nil
		}
		if prompt == "" {
			continue
		}
		_, promptErr := acpConn.Prompt(ctx, coderacp.PromptRequest{SessionId: coderacp.SessionId(opts.sessionID), Prompt: []coderacp.ContentBlock{coderacp.TextBlock(prompt)}})
		if promptErr == nil {
			continue
		}
		_ = conn.Close()
		for attempt = 0; attempt < 5; attempt++ {
			timer := time.NewTimer(reconnectBackoff(attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			connected, stream, cursor, err := opts.connect(ctx)
			if err == nil {
				acpConn, conn, opts.afterSeq, attempt = connected, stream, cursor, 0
				break
			}
			if attempt == 4 {
				return fmt.Errorf("prompt failed and reconnect exhausted: %w", promptErr)
			}
		}
	}
}
