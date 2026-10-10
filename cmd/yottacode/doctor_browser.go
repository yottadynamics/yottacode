package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/yottadynamics/yottacode/internal/browser"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/experimental"
)

// BrowserDoctorResult reports readiness for the experimental browser_* tools.
// It is only probed — and only shown — when the `browser` experimental feature
// is enabled, so the report is unchanged for everyone else.
type BrowserDoctorResult struct {
	Status     doctorStatus `json:"status"`
	Binary     string       `json:"binary,omitempty"`
	Version    string       `json:"version,omitempty"`
	MinVersion int          `json:"min_major_version"`
	Display    bool         `json:"display"`
	Root       bool         `json:"root,omitempty"`
	Container  bool         `json:"container,omitempty"`
	Issues     []string     `json:"issues,omitempty"`
	Warnings   []string     `json:"warnings,omitempty"`
	Hints      []string     `json:"hints,omitempty"`
}

var probeBrowser = browser.Probe

// browserFeatureEnabled resolves the experimental `browser` flag exactly as a
// session does. cliNames already includes $YOTTACODE_EXPERIMENTAL (cli.Resolve
// merges it).
func browserFeatureEnabled(cfg config.Config, cliNames []string) bool {
	return experimental.Resolve(cfg.Experimental, cliNames).IsEnabled(experimental.Browser)
}

func probeBrowserDoctor(ctx context.Context) BrowserDoctorResult {
	p := probeBrowser(ctx)
	return BrowserDoctorResult{
		Status:     statusFromIssuesWarnings(p.Issues, p.Warnings),
		Binary:     p.Binary,
		Version:    p.Version,
		MinVersion: browser.MinChromeMajor,
		Display:    p.HasDisplay,
		Root:       p.Root,
		Container:  p.Container,
		Issues:     p.Issues,
		Warnings:   p.Warnings,
		Hints:      p.Hints,
	}
}

func renderBrowserSection(b *strings.Builder, r BrowserDoctorResult) {
	b.WriteString("\nBrowser (experimental):\n")
	fmt.Fprintf(b, "  status: %s\n", r.Status)
	if r.Binary != "" {
		fmt.Fprintf(b, "  binary: %s\n", r.Binary)
	}
	if r.Version != "" {
		fmt.Fprintf(b, "  version: %s (warns below Chrome %d)\n", r.Version, r.MinVersion)
	}
	fmt.Fprintf(b, "  visible window (browser_handoff): %t\n", r.Display)
	for _, issue := range r.Issues {
		fmt.Fprintf(b, "  issue: %s\n", issue)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(b, "  warning: %s\n", w)
	}
	for _, h := range r.Hints {
		fmt.Fprintf(b, "  hint: %s\n", h)
	}
}
