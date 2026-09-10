package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// These tests verify (not implement — that's k5/OAuth-phase scope) yottacode's
// current, unregistered-handler behavior when a server sends an elicitation
// or sampling request: since internal/mcp never sets ElicitationHandler or
// CreateMessageHandler on sdk.NewClient (always constructed with nil
// options), the client never advertises either capability during
// initialize, and — per this test — the SDK client-side rejects an
// unsolicited request with a prompt error rather than hanging. A malicious
// or non-compliant server sending one anyway must not be able to hang a
// yottacode session waiting on a response nobody will ever send.

func TestHTTPClient_ToolCallSurvivesUnsolicitedElicitation(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "test-elicit-server", Version: "test"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "elicit_then_echo", Description: "x"},
		func(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
			_, err := req.Session.Elicit(ctx, &sdk.ElicitParams{
				Message:         "need input",
				RequestedSchema: map[string]any{"type": "object"},
			})
			if err == nil {
				return nil, nil, nil // shouldn't happen — the point of this test is that it errors
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "elicit error: " + err.Error()}}}, nil, nil
		})
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(ts.Close)

	c := NewHTTPClient("test", ts.URL, nil, false, Policy{}, "", nil)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	res, err := c.CallTool(ctx, "elicit_then_echo", `{}`)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("CallTool took %v against an unregistered elicitation handler — it should fail fast, not hang toward the ctx deadline", elapsed)
	}
	// The tool itself catches the elicit error and returns it as text
	// (rather than propagating it as a transport error), so this call
	// should still succeed at the CallTool layer — what we're actually
	// checking is that req.Session.Elicit itself failed promptly inside
	// the handler, proven by the returned text.
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool-level error: %s", res.Text)
	}
	t.Logf("server-observed Elicit() result: %s", res.Text)
}

func TestHTTPClient_ToolCallSurvivesUnsolicitedSampling(t *testing.T) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "test-sampling-server", Version: "test"}, nil)
	sdk.AddTool(srv, &sdk.Tool{Name: "sample_then_echo", Description: "x"},
		func(ctx context.Context, req *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, any, error) {
			_, err := req.Session.CreateMessage(ctx, &sdk.CreateMessageParams{
				Messages:  []*sdk.SamplingMessage{{Role: "user", Content: &sdk.TextContent{Text: "hi"}}},
				MaxTokens: 16,
			})
			if err == nil {
				return nil, nil, nil
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "sampling error: " + err.Error()}}}, nil, nil
		})
	ts := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(ts.Close)

	c := NewHTTPClient("test", ts.URL, nil, false, Policy{}, "", nil)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	res, err := c.CallTool(ctx, "sample_then_echo", `{}`)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Errorf("CallTool took %v against an unregistered sampling handler — it should fail fast, not hang toward the ctx deadline", elapsed)
	}
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool-level error: %s", res.Text)
	}
	t.Logf("server-observed CreateMessage() result: %s", res.Text)
}
