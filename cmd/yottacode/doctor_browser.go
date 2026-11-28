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

	// PersistentProfile is the opt-in [browser] persistent_profile setting;
	// ProfileDir/ProfileBytes describe the directory when it is on.
	PersistentProfile bool   `json:"persistent_profile"`
	ProfileDir        string `json:"profile_dir,omitempty"`
	ProfileBytes      int64  `json:"profile_bytes,omitempty"`
}

// addPersistentProfile fills in the persistent-profile facts. dirFn locates the
// directory (injectable for tests).
func addPersistentProfile(r *BrowserDoctorResult, cfg config.Config, dirFn func() (string, error)) {
	r.PersistentProfile = cfg.Browser.PersistentProfile
	if !r.PersistentProfile {
		return
	}
	dir, err := dirFn()
	if err != nil {
		r.Warnings = append(r.Warnings, "persistent_profile is on but its directory cannot be resolved: "+err.Error())
		r.Status = statusFromIssuesWarnings(r.Issues, r.Warnings)
		return
	}
	r.ProfileDir = dir
	_, r.ProfileBytes = browser.ProfileUsage(dir)
}

var probeBrowser = browser.Probe

// browserFeatureEnabled resolves the experimental `browser` flag exactly as a
// session does. cliNames already includes $YOTTACODE_EXPERIMENTAL (cli.Resolve
// merges it).
func browserFeatureEnabled(cfg config.Config, cliNames []string) bool {
	set := experimental.NewSet()
	for name, enabled := range cfg.Experimental {
		if enabled {
			set.Enable(name)
		}
	}
	for _, name := range cliNames {
		set.Enable(name)
	}
	return set.IsEnabled(experimental.Browser)
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
	if r.PersistentProfile {
		fmt.Fprintf(b, "  persistent profile: on (%s, %d KB; `/browser clear` empties it)\n", r.ProfileDir, r.ProfileBytes/1024)
	} else {
		b.WriteString("  persistent profile: off (a fresh throwaway profile per session)\n")
	}
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
