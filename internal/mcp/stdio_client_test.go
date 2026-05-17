package mcp_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/mcp"
)

// echoServerBin builds the testdata/echo_server fixture once per test
// process and returns its absolute path. Subsequent calls reuse the
// same binary — building the fixture per-test would inflate the test
// run by ~1s each invocation.
var (
	echoOnce sync.Once
	echoBin  string
	echoErr  error
)

func buildEchoServer(t *testing.T) string {
	t.Helper()
	echoOnce.Do(func() {
		dir, err := os.MkdirTemp("", "echo-server-bin")
		if err != nil {
			echoErr = err
			return
		}
		echoBin = filepath.Join(dir, "echo-server")
		cmd := exec.Command("go", "build", "-o", echoBin, "./testdata/echo_server")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			echoErr = err
			return
		}
	})
	if echoErr != nil {
		t.Fatalf("build fixture: %v", echoErr)
	}
	return echoBin
}

func newEchoClient(t *testing.T) *mcp.StdioClient {
	t.Helper()
	bin := buildEchoServer(t)
	c := mcp.NewStdioClient("echo", bin, nil, nil)
	t.Cleanup(func() { _ = c.Stop(context.Background()) })
	return c
}

func TestStdioClient_StartListsAdvertisedTools(t *testing.T) {
	c := newEchoClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	tools, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]mcp.ToolDescriptor{}
	for _, td := range tools {
		got[td.Name] = td
	}

	for _, want := range []string{"echo", "slow", "flaky", "crash"} {
		if _, ok := got[want]; !ok {
			t.Errorf("tool %q not advertised; got %+v", want, tools)
		}
	}
	if !got["echo"].ReadOnlyHint {
		t.Errorf("echo should be read-only-hinted; got %+v", got["echo"])
	}
	if got["flaky"].ReadOnlyHint {
		t.Errorf("flaky should not be read-only-hinted; got %+v", got["flaky"])
	}
}

func TestStdioClient_CallToolHappyPath(t *testing.T) {
	c := newEchoClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	res, err := c.CallTool(ctx, "echo", `{"text":"hello"}`)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Errorf("expected success, got IsError=true; text=%q", res.Text)
	}
	if res.Text != "echo:hello" {
		t.Errorf("text = %q, want %q", res.Text, "echo:hello")
	}
}

func TestStdioClient_CallToolErrorEnvelope(t *testing.T) {
	c := newEchoClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	res, err := c.CallTool(ctx, "flaky", `{}`)
	if err != nil {
		t.Fatalf("CallTool returned transport-level error %v; expected envelope success with IsError=true", err)
	}
	if !res.IsError {
		t.Errorf("want IsError=true; got %+v", res)
	}
	if !strings.Contains(res.Text, "intentional failure") {
		t.Errorf("error text = %q, want it to mention 'intentional failure'", res.Text)
	}
}

func TestStdioClient_CallToolHonorsCancellation(t *testing.T) {
	c := newEchoClient(t)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := c.CallTool(ctx, "slow", `{"ms": 5000}`)
	if err == nil {
		t.Fatalf("expected ctx cancellation error; got nil")
	}
}

func TestStdioClient_StopIsIdempotent(t *testing.T) {
	c := newEchoClient(t)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Errorf("first Stop returned %v", err)
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Errorf("second Stop returned %v", err)
	}
}

func TestStdioClient_StartFailsWhenCommandMissing(t *testing.T) {
	c := mcp.NewStdioClient("missing", "/no/such/binary/yottacode-test", nil, nil)
	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("expected error for missing command")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found'; got %v", err)
	}
}

func TestStdioClient_CallToolBeforeStart(t *testing.T) {
	bin := buildEchoServer(t)
	c := mcp.NewStdioClient("echo", bin, nil, nil)
	_, err := c.CallTool(context.Background(), "echo", `{"text":"x"}`)
	if err == nil {
		t.Fatal("expected ErrNotStarted; got nil")
	}
}
