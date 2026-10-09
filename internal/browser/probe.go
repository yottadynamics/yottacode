package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MinChromeMajor is a conservative floor, not a tested boundary: the tools use
// the new headless mode, Fetch interception and Emulation.setAutomationOverride,
// and builds older than this are untested and may lack some of those CDP
// methods. doctor warns below it.
const MinChromeMajor = 120

// ProbeResult is what `yottacode doctor` reports about browser readiness.
// Probing is cheap and side-effect free: it never launches a browser (it runs
// `chrome --version`, which exits immediately).
type ProbeResult struct {
	Binary     string
	Version    string // full dotted version, "" when it could not be read
	Major      int
	HasDisplay bool // a visible window (browser_handoff) is possible
	Root       bool // running as uid 0
	Container  bool // /.dockerenv or /run/.containerenv present
	Issues     []string
	Warnings   []string
	Hints      []string
}

// probeEnv is the set of host facts Probe reads, injectable for tests.
type probeEnv struct {
	findBinary func() (string, error)
	version    func(ctx context.Context, bin string) (string, error)
	display    func() bool
	euid       func() int
	exists     func(path string) bool
}

func defaultProbeEnv() probeEnv {
	return probeEnv{
		findBinary: findChromeBinary,
		version: func(ctx context.Context, bin string) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, bin, "--version").Output()
			return string(out), err
		},
		display: displayAvailable,
		euid:    os.Geteuid,
		exists:  func(p string) bool { _, err := os.Stat(p); return err == nil },
	}
}

// Probe inspects the host for browser readiness.
func Probe(ctx context.Context) ProbeResult { return probe(ctx, defaultProbeEnv()) }

func probe(ctx context.Context, env probeEnv) ProbeResult {
	var r ProbeResult
	r.Root = env.euid() == 0
	r.Container = env.exists("/.dockerenv") || env.exists("/run/.containerenv")
	r.HasDisplay = env.display()

	bin, err := env.findBinary()
	if err != nil {
		r.Issues = append(r.Issues, "no Chrome/Chromium binary found; the browser_* tools cannot launch")
		r.Hints = append(r.Hints, "install Google Chrome or Chromium (yottacode never downloads a browser)")
		return r
	}
	r.Binary = bin

	if out, err := env.version(ctx, bin); err != nil {
		r.Warnings = append(r.Warnings, fmt.Sprintf("could not read the browser version (%v)", err))
	} else if v := chromeVersionRE.FindString(out); v == "" {
		r.Warnings = append(r.Warnings, fmt.Sprintf("unrecognized browser version output %q", strings.TrimSpace(out)))
	} else {
		r.Version = v
		r.Major, _ = strconv.Atoi(strings.SplitN(v, ".", 2)[0])
		if r.Major < MinChromeMajor {
			r.Warnings = append(r.Warnings, fmt.Sprintf("browser version %s is older than the supported minimum (Chrome %d); some browser_* tools may misbehave", v, MinChromeMajor))
		}
	}

	if r.Root {
		r.Issues = append(r.Issues, "running as root: Chrome refuses to start its sandbox as uid 0, so browser launch will fail")
		r.Hints = append(r.Hints, "run yottacode as a non-root user (in a container, add a user and USER to the Dockerfile); yottacode does not pass --no-sandbox")
	} else if r.Container {
		r.Hints = append(r.Hints, "inside a container Chrome's sandbox needs user namespaces; if launch fails, allow them (e.g. --security-opt seccomp profile for Chrome) rather than disabling the sandbox")
	}
	if !r.HasDisplay {
		r.Hints = append(r.Hints, "no DISPLAY/WAYLAND_DISPLAY: headless works, but browser_handoff (a visible window for CAPTCHAs) is unavailable here")
	}
	return r
}

// launchHint is appended to launch failures with a likely, actionable cause.
func launchHint() string {
	if os.Geteuid() == 0 {
		return " (hint: Chrome's sandbox cannot start as root; run yottacode as a non-root user)"
	}
	return ""
}
