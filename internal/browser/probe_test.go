package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func fakeEnv(euid int, display bool, files ...string) probeEnv {
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
	}
	return probeEnv{
		findBinary: func() (string, error) { return "/usr/bin/chrome", nil },
		version:    func(context.Context, string) (string, error) { return "Google Chrome 154.0.8037.97 \n", nil },
		display:    func() bool { return display },
		euid:       func() int { return euid },
		exists:     func(p string) bool { return have[p] },
	}
}

func TestProbeHealthyHost(t *testing.T) {
	r := probe(context.Background(), fakeEnv(1000, true))
	if len(r.Issues) != 0 || len(r.Warnings) != 0 {
		t.Fatalf("healthy host reported problems: %+v", r)
	}
	if r.Version != "154.0.8037.97" || r.Major != 154 || r.Binary != "/usr/bin/chrome" {
		t.Errorf("version/binary parsed wrong: %+v", r)
	}
}

func TestProbeMissingBinaryIsAnIssue(t *testing.T) {
	env := fakeEnv(1000, true)
	env.findBinary = func() (string, error) { return "", ErrNoBinaryFound }
	r := probe(context.Background(), env)
	if len(r.Issues) != 1 || len(r.Hints) == 0 {
		t.Errorf("want one issue with an install hint, got %+v", r)
	}
}

func TestProbeRootIsAnIssueWithAHint(t *testing.T) {
	r := probe(context.Background(), fakeEnv(0, true, "/.dockerenv"))
	if !r.Root || !r.Container {
		t.Fatalf("root/container not detected: %+v", r)
	}
	if len(r.Issues) != 1 || !strings.Contains(r.Issues[0], "root") {
		t.Errorf("issues = %v", r.Issues)
	}
	if !strings.Contains(strings.Join(r.Hints, " "), "non-root") {
		t.Errorf("hints = %v", r.Hints)
	}
}

func TestProbeOldChromeWarns(t *testing.T) {
	env := fakeEnv(1000, true)
	env.version = func(context.Context, string) (string, error) { return "Chromium 99.0.4844.84", nil }
	r := probe(context.Background(), env)
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "older") {
		t.Errorf("warnings = %v", r.Warnings)
	}
}

func TestProbeUnreadableVersionWarnsNotFails(t *testing.T) {
	env := fakeEnv(1000, false)
	env.version = func(context.Context, string) (string, error) { return "", errors.New("boom") }
	r := probe(context.Background(), env)
	if len(r.Issues) != 0 || len(r.Warnings) != 1 {
		t.Errorf("%+v", r)
	}
	if !strings.Contains(strings.Join(r.Hints, " "), "browser_handoff") {
		t.Errorf("no-display hint missing: %v", r.Hints)
	}
}
