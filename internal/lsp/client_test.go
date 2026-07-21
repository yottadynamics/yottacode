package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestClientJSONRPCFramingAndRequests(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{
		stdin:  clientWriter,
		stdout: bufio.NewReader(clientReader),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		readServerRequest(t, serverReader, "workspace/symbol")
		writeServerResponse(t, serverWriter, 1, []map[string]any{{
			"name":          "Thing",
			"kind":          12,
			"containerName": "pkg",
			"location": map[string]any{
				"uri":   "file:///tmp/main.go",
				"range": map[string]any{"start": map[string]any{"line": 4, "character": 2}},
			},
		}})
	}()
	symbols, err := c.WorkspaceSymbols(context.Background(), "Thing")
	if err != nil {
		t.Fatalf("WorkspaceSymbols: %v", err)
	}
	<-done
	if len(symbols) != 1 || symbols[0].Name != "Thing" || symbols[0].Kind != "function" || symbols[0].Location.Line != 4 {
		t.Fatalf("unexpected symbols: %+v", symbols)
	}
}

func TestClientLocationResponseAcceptsSingleLocation(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{stdin: clientWriter, stdout: bufio.NewReader(clientReader)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		readServerRequest(t, serverReader, "textDocument/definition")
		writeServerResponse(t, serverWriter, 1, map[string]any{
			"uri":   "file:///tmp/main.go",
			"range": map[string]any{"start": map[string]any{"line": 1, "character": 7}},
		})
	}()
	locs, err := c.Definition(context.Background(), "/tmp/main.go", Position{})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	<-done
	if len(locs) != 1 || locs[0].Path != "/tmp/main.go" || locs[0].Line != 1 || locs[0].Character != 7 {
		t.Fatalf("unexpected locations: %+v", locs)
	}
}

func TestClientServerErrorSurfaces(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	c := &Client{stdin: clientWriter, stdout: bufio.NewReader(clientReader)}
	go func() {
		readServerRequest(t, serverReader, "workspace/symbol")
		body := `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`
		fmt.Fprintf(serverWriter, "Content-Length: %d\r\n\r\n%s", len(body), body)
	}()
	_, err := c.WorkspaceSymbols(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "server error -32601: nope") {
		t.Fatalf("expected server error, got %v", err)
	}
}

func TestPathURIConversion(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	uri := pathToURI(cwd)
	if !strings.HasPrefix(uri, "file://") {
		t.Fatalf("pathToURI should produce file URI, got %q", uri)
	}
	if got := uriToPath(uri); got == "" {
		t.Fatalf("uriToPath returned empty path")
	}
}

func readServerRequest(t *testing.T, r io.Reader, wantMethod string) {
	t.Helper()
	br := bufio.NewReader(r)
	length := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			if _, err := fmt.Sscanf(line, "Content-Length: %d", &length); err != nil {
				t.Fatalf("scan length: %v", err)
			}
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	var msg struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if msg.Method != wantMethod {
		t.Fatalf("method = %q, want %q; body=%s", msg.Method, wantMethod, body)
	}
}

func writeServerResponse(t *testing.T, w io.Writer, id int, result any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
}
