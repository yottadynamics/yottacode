package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/version"
)

func TestDoctorCmd_JSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-5"},{"id":"gpt-4.1"}]}`)
	}))
	t.Cleanup(srv.Close)

	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"doctor",
		"--json",
		"--provider", "openai",
		"--model", "gpt-5",
		"--base-url", srv.URL,
		"--api-key", "sk-test",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor execute: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(out.String()), &raw); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out.String())
	}
	for _, key := range []string{"endpoint_reachable", "auth_ok", "model_visible", "available_models", "profile"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("doctor json missing %q:\n%s", key, out.String())
		}
	}
	var got struct {
		EndpointReachable bool     `json:"endpoint_reachable"`
		AuthOK            bool     `json:"auth_ok"`
		ModelVisible      bool     `json:"model_visible"`
		AvailableModels   []string `json:"available_models"`
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatalf("decode typed json: %v\n%s", err, out.String())
	}
	if !got.EndpointReachable || !got.AuthOK || !got.ModelVisible {
		t.Fatalf("unexpected doctor result: %+v", got)
	}
	if len(got.AvailableModels) != 2 {
		t.Fatalf("available models = %+v", got.AvailableModels)
	}
}

func TestDoctorCmd_ReturnsErrorWhenModelInvisible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-4.1"}]}`)
	}))
	t.Cleanup(srv.Close)

	cmd := newCLI()
	cmd.SilenceUsage = true
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"doctor",
		"--provider", "openai",
		"--model", "gpt-5",
		"--base-url", srv.URL,
		"--api-key", "sk-test",
	})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected doctor to return an error")
	}
	if !strings.Contains(out.String(), `issue: model "gpt-5" not listed by /models`) {
		t.Fatalf("doctor output missing model issue:\n%s", out.String())
	}
}

func TestRootCmd_VersionFlag(t *testing.T) {
	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("version flag execute: %v", err)
	}

	want := fmt.Sprintf("yottacode version %s\n", version.Full())
	if out.String() != want {
		t.Fatalf("version flag output = %q, want %q", out.String(), want)
	}
}

func TestVersionCmd(t *testing.T) {
	cmd := newCLI()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("version command execute: %v", err)
	}

	want := fmt.Sprintf("yottacode version %s\n", version.Full())
	if out.String() != want {
		t.Fatalf("version command output = %q, want %q", out.String(), want)
	}
}

// shouldRunUpdateCheck has three skip paths. The tty check naturally
// returns false under `go test` (stdin is a pipe), which gives us the
// "non-tty" branch coverage for free; t.Setenv covers the env-var
// branch. Skipping goroutine + network coverage here keeps the test
// offline and deterministic.

func TestShouldRunUpdateCheck_OptOut(t *testing.T) {
	t.Setenv("YOTTACODE_NO_UPDATE_CHECK", "1")
	if shouldRunUpdateCheck() {
		t.Fatalf("expected false when YOTTACODE_NO_UPDATE_CHECK=1")
	}
}

func TestShouldRunUpdateCheck_NonTTY(t *testing.T) {
	t.Setenv("YOTTACODE_NO_UPDATE_CHECK", "")
	if shouldRunUpdateCheck() {
		t.Fatalf("expected false under go test (stdin is not a tty)")
	}
}

func TestIsTerminal_DevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open /dev/null: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if isTerminal(f) {
		t.Fatalf("/dev/null should not be a terminal")
	}
}

func TestMaybeStartUpdateCheck_NilWhenOptedOut(t *testing.T) {
	t.Setenv("YOTTACODE_NO_UPDATE_CHECK", "1")
	if ch := maybeStartUpdateCheck(context.Background()); ch != nil {
		t.Fatalf("expected nil channel when opted out, got %v", ch)
	}
}
