package browser

import (
	"context"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// stealthLauncher removes the launch flags that announce automation and
// gives the window a plain desktop size. Called only when Options.Stealth
// is set.
//
// rod adds --enable-automation by default (it sets navigator.webdriver and
// shows the "controlled by automated software" infobar); Blink's
// AutomationControlled feature is the other source of the same signal.
func stealthLauncher(l *launcher.Launcher) *launcher.Launcher {
	return l.
		Delete("enable-automation").
		Set("disable-blink-features", "AutomationControlled").
		Set("lang", "en-US").
		Set("window-size", "1280,800")
}

// stealthProfile is the identity a stealth session presents: a user agent,
// client hints and platform that all match the real browser build, so the
// combination is at least self-consistent. rod's default device emulation —
// a fixed Mac laptop on an old Chrome — is what stealth mode replaces
// (see launchSession's NoDefaultDevice).
type stealthProfile struct {
	userAgent string
	platform  string // navigator.platform
	meta      *proto.EmulationUserAgentMetadata
}

// buildStealthProfile derives the profile from the running browser's own
// Browser.getVersion answer, so it tracks whatever Chrome is installed.
// headless Chrome advertises itself as "HeadlessChrome" in both the user
// agent and the client-hint brand list; those are the two strings replaced.
func buildStealthProfile(v *proto.BrowserGetVersionResult) stealthProfile {
	full := v.Product // "Chrome/153.0.7000.0" or "HeadlessChrome/…"
	if i := strings.Index(full, "/"); i >= 0 {
		full = full[i+1:]
	}
	major, _, _ := strings.Cut(full, ".")

	ua := stealthUserAgent(full)
	if ua == "" {
		ua = strings.ReplaceAll(v.UserAgent, "HeadlessChrome", "Chrome")
	}
	sp := stealthProfile{userAgent: ua}
	arch := "x86"
	if runtime.GOARCH == "arm64" {
		arch = "arm"
	}
	platformName, platformVersion := "Linux", "6.5.0"
	sp.platform = "Linux x86_64"
	if runtime.GOOS == "darwin" {
		platformName, platformVersion = "macOS", "14.0.0"
		sp.platform = "MacIntel"
	}
	sp.meta = &proto.EmulationUserAgentMetadata{
		Brands: []*proto.EmulationUserAgentBrandVersion{
			{Brand: "Chromium", Version: major},
			{Brand: "Google Chrome", Version: major},
			{Brand: "Not_A Brand", Version: "24"},
		},
		FullVersionList: []*proto.EmulationUserAgentBrandVersion{
			{Brand: "Chromium", Version: full},
			{Brand: "Google Chrome", Version: full},
			{Brand: "Not_A Brand", Version: "24.0.0.0"},
		},
		Platform:        platformName,
		PlatformVersion: platformVersion,
		Architecture:    arch,
		Bitness:         "64",
		Model:           "",
		Mobile:          false,
	}
	return sp
}

// chromeVersionRE picks the dotted version out of `chrome --version` output
// ("Google Chrome 153.0.7000.12 ", "Chromium 153.0.7000.12 built on Debian").
var chromeVersionRE = regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)

// stealthUserAgent is the desktop Chrome user agent for a full version
// string, in Chrome's reduced-UA form (major version, then .0.0.0) and for
// this host's OS. Empty when fullVersion has no major version.
func stealthUserAgent(fullVersion string) string {
	major, _, _ := strings.Cut(strings.TrimSpace(fullVersion), ".")
	if major == "" || strings.Trim(major, "0123456789") != "" {
		return ""
	}
	osPart := "X11; Linux x86_64"
	if runtime.GOOS == "darwin" {
		osPart = "Macintosh; Intel Mac OS X 10_15_7"
	}
	return "Mozilla/5.0 (" + osPart + ") AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + major + ".0.0.0 Safari/537.36"
}

// launchUserAgent asks the binary its version and derives the stealth user
// agent from it, so it can go on the command line. That matters because the
// per-page override (stealthProfile.apply) is applied asynchronously: a popup's
// first document can commit before it lands and would announce HeadlessChrome.
// A launch flag covers every page, worker and frame from the first request.
// Empty when the version can't be read; the per-page override still applies.
func launchUserAgent(ctx context.Context, bin string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	return stealthUserAgent(chromeVersionRE.FindString(string(out)))
}

// apply installs the stealth profile on one page: the user agent, client
// hints and platform override replace the headless ones. Best effort — a page
// that can't take an override still works, just less convincingly.
//
// There is deliberately no injected JavaScript. An earlier version ran the
// puppeteer-extra evasion bundle (via go-rod/stealth) on every page; measured
// against Chrome 153 with the launch flags and this override already in place
// it changed nothing useful, and two of its edits *created* contradictions a
// detector could flag: a WebGL renderer claiming a Mac GPU under a Linux user
// agent, and a notifications permission query answering "denied" while
// Notification.permission said "default". Patching JS-visible properties by
// hand also means chasing every Chrome release, which is exactly how such a
// script goes stale. What the browser can be configured to report truthfully,
// it is; what it can't (software-rendered WebGL, a small screen) stays honest.
//
// AcceptLanguage is the bare list: Chrome derives navigator.languages by
// splitting it on commas and adds the q-values to the header itself, so a
// "…;q=0.9" here would surface as a bogus "en;q=0.9" language.
func (sp stealthProfile) apply(pg *rod.Page) {
	_ = pg.SetUserAgent(&proto.NetworkSetUserAgentOverride{
		UserAgent:         sp.userAgent,
		AcceptLanguage:    "en-US,en",
		Platform:          sp.platform,
		UserAgentMetadata: sp.meta,
	})
}

// stealthInit returns the per-page initializer for a stealth session, or nil
// if the browser's version can't be read (the session then runs with only
// the launch flags, which is still an improvement on rod's defaults).
func stealthInit(br *rod.Browser) func(*rod.Page) {
	v, err := proto.BrowserGetVersion{}.Call(br)
	if err != nil {
		return nil
	}
	return buildStealthProfile(v).apply
}
