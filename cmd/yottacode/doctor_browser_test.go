package main

import (
	"context"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/browser"
	"github.com/yottadynamics/yottacode/internal/config"
)

func TestBrowserFeatureEnabledResolution(t *testing.T) {
	if browserFeatureEnabled(config.Default(), nil) {
		t.Error("browser must be off by default")
	}
	if !browserFeatureEnabled(config.Default(), []string{"browser"}) {
		t.Error("--experimental browser should enable it")
	}
	cfg := config.Default()
	cfg.Experimental = map[string]bool{"browser": true}
	if !browserFeatureEnabled(cfg, nil) {
		t.Error("[experimental] browser = true should enable it")
	}
	// $YOTTACODE_EXPERIMENTAL reaches here already merged into the CLI names.
	if !browserFeatureEnabled(config.Default(), []string{"dispatch", "browser"}) {
		t.Error("a merged env/CLI list containing browser should enable it")
	}
}

func TestDoctorReportOmitsBrowserUnlessEnabled(t *testing.T) {
	summary := newDoctorSummary(adapter.ProbeResult{}, GitHubProbeResult{Status: doctorStatusSkipped, Skipped: true}, LSPDoctorResult{}, MediaDoctorResult{}, SandboxDoctorResult{})
	out := formatDoctorReport(summary, adapter.ProbeResult{}, GitHubProbeResult{Skipped: true}, LSPDoctorResult{}, MediaDoctorResult{}, SandboxDoctorResult{})
	if strings.Contains(out, "rowser") {
		t.Errorf("browser must not appear when the feature is off:\n%s", out)
	}
}

func TestDoctorReportShowsBrowserSection(t *testing.T) {
	old := probeBrowser
	t.Cleanup(func() { probeBrowser = old })
	probeBrowser = func(context.Context) browser.ProbeResult {
		return browser.ProbeResult{
			Binary: "/usr/bin/chrome", Version: "154.0.1.2", Root: true,
			Issues: []string{"running as root"}, Hints: []string{"run as non-root"},
		}
	}
	br := probeBrowserDoctor(context.Background())
	if br.Status != doctorStatusIssue {
		t.Fatalf("status = %q, want issue", br.Status)
	}
	summary := newDoctorSummary(adapter.ProbeResult{}, GitHubProbeResult{Skipped: true}, LSPDoctorResult{}, MediaDoctorResult{}, SandboxDoctorResult{})
	summary.Browser, summary.BrowserDetail = br.Status, &br
	out := formatDoctorReport(summary, adapter.ProbeResult{}, GitHubProbeResult{Skipped: true}, LSPDoctorResult{}, MediaDoctorResult{}, SandboxDoctorResult{})
	for _, want := range []string{"- browser: issue", "Browser (experimental):", "/usr/bin/chrome", "154.0.1.2", "issue: running as root", "hint: run as non-root"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}
