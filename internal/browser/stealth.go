package browser

import (
	"context"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// stealthLaunchOptions removes the launch-time signals that announce
// automation and gives the window a plain desktop size, so a launched
// session looks like an ordinary Chrome install rather than a fresh,
// anonymous automation rig. Applied to every session, headless and headed:
// none of this changes what the browser can do, it just stops volunteering
// "I am automated" for free. It never solves or works around a real
// human-verification challenge — browser_handoff stays the answer for that.
func stealthLaunchOptions(ctx context.Context, bin string, headless bool) []chromedp.ExecAllocatorOption {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("lang", "en-US"),
	}
	if headless {
		// A fixed, plain desktop size only matters for the session nobody is
		// looking at — it's what stands in for "what size is this browser"
		// in the fingerprint. "new" headless mode renders through the same
		// code path as a normal window and matches a real Chrome's behavior
		// far more closely than relying on whatever a bare --headless
		// defaults to on the installed Chrome version.
		opts = append(opts, chromedp.WindowSize(1280, 800), chromedp.Flag("headless", "new"))
	} else {
		// The handoff window is for a human to actually see and use — start
		// it maximized so it's impossible to miss or mistake for a small,
		// easy-to-overlook popup, regardless of the real screen's
		// resolution. (A fixed 1280x800 here was a real regression: it made
		// the one window a person needs to notice and act on smaller than
		// it should be.)
		opts = append(opts, chromedp.Flag("start-maximized", true))
	}
	// The per-page override in stealthProfile.apply lands after the page
	// context exists, which loses the race against a same-gesture popup's
	// first request — that request goes out, HeadlessChrome UA and all,
	// before the override can be applied. A launch flag covers every page,
	// worker and frame from their first byte.
	if ua := launchUserAgent(ctx, bin); ua != "" {
		opts = append(opts, chromedp.UserAgent(ua))
	}
	return opts
}

// waitMaximizedViewport blocks briefly for a freshly maximized headed
// window's viewport to actually reflect the maximized size before control
// returns to whatever acts on the page next. The OS window-manager resize
// --start-maximized triggers is asynchronous relative to Chrome's own
// page-load lifecycle, so window.innerWidth/innerHeight can still read 0 (or
// a stale pre-maximize value) right after "load" fires. Best effort: if it's
// still not settled after timeout, the caller gets a working session, just
// not yet visually settled — not worth failing launch over.
func waitMaximizedViewport(ctx context.Context, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var w, h int
		evalCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		err := chromedp.Run(evalCtx, chromedp.Evaluate(`window.innerWidth`, &w), chromedp.Evaluate(`window.innerHeight`, &h))
		cancel()
		if err == nil && w > 0 && h > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// stealthProfile is the browser identity a session presents on top of the
// launch flags: a user agent, client hints and platform that all match the
// real installed Chrome, so the combination stays self-consistent instead of
// contradicting itself (e.g. a "HeadlessChrome" UA next to a desktop window
// size).
type stealthProfile struct {
	userAgent string
	platform  string // navigator.platform
	meta      *emulation.UserAgentMetadata
}

// chromeVersionRE picks the dotted version out of `chrome --version` output
// ("Google Chrome 153.0.7000.12 ", "Chromium 153.0.7000.12 built on Debian").
var chromeVersionRE = regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)

// stealthUserAgent is the desktop Chrome user agent for a full version
// string, in Chrome's reduced-UA form (major version, then .0.0.0), for this
// host's OS. Empty when fullVersion has no leading version number.
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
// agent from it, so it can go on the command line before the browser (and
// any CDP connection to it) exists. Empty when the version can't be read;
// the per-page override still applies in that case.
func launchUserAgent(ctx context.Context, bin string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	return stealthUserAgent(chromeVersionRE.FindString(string(out)))
}

// buildStealthProfile derives the profile from the running browser's own
// Browser.getVersion answer, so it tracks whatever Chrome is actually
// installed rather than a hardcoded version. Headless Chrome advertises
// itself as "HeadlessChrome" in both the user agent and the client-hint
// brand list; those are the two strings this replaces.
func buildStealthProfile(product, userAgent string) stealthProfile {
	full := product // "Chrome/153.0.7000.0" or "HeadlessChrome/…"
	if i := strings.Index(full, "/"); i >= 0 {
		full = full[i+1:]
	}
	major, _, _ := strings.Cut(full, ".")

	ua := stealthUserAgent(full)
	if ua == "" {
		ua = strings.ReplaceAll(userAgent, "HeadlessChrome", "Chrome")
	}
	sp := stealthProfile{userAgent: ua, platform: "Linux x86_64"}
	arch := "x86"
	if runtime.GOARCH == "arm64" {
		arch = "arm"
	}
	platformName, platformVersion := "Linux", "6.5.0"
	if runtime.GOOS == "darwin" {
		platformName, platformVersion = "macOS", "14.0.0"
		sp.platform = "MacIntel"
	}
	sp.meta = &emulation.UserAgentMetadata{
		Brands: []*emulation.UserAgentBrandVersion{
			{Brand: "Chromium", Version: major},
			{Brand: "Google Chrome", Version: major},
			{Brand: "Not_A Brand", Version: "24"},
		},
		FullVersionList: []*emulation.UserAgentBrandVersion{
			{Brand: "Chromium", Version: full},
			{Brand: "Google Chrome", Version: full},
			{Brand: "Not_A Brand", Version: "24.0.0.0"},
		},
		Platform:        platformName,
		PlatformVersion: platformVersion,
		Architecture:    arch,
		Bitness:         "64",
	}
	return sp
}

// actions are the CDP actions that install the stealth profile on one page:
// Emulation.setAutomationOverride is Chrome's own supported way to turn off
// the "controlled by automated software" signal (the source of
// navigator.webdriver) — see
// https://chromedevtools.github.io/devtools-protocol/tot/Emulation/#method-setAutomationOverride
// — used here belt-and-suspenders with the disable-blink-features launch
// flag. The user agent, accept-language and client hints overrides replace
// the headless ones. Best effort: a page that rejects the override still
// works, just less convincingly.
//
// Callers must run these in the same chromedp.Run call (or otherwise
// serialize them) as any other first action on a freshly created target
// context: chromedp.Context lazily attaches to a new target on its first
// Run, and that attach is not safe to race — issuing it from two goroutines
// concurrently (e.g. this and attachPageCapture's own Enable-domains call)
// corrupts chromedp's internal bookkeeping, caught by go test -race.
//
// There is deliberately no injected JavaScript here. An earlier version of
// this code ran a puppeteer-extra-style evasion bundle on every page;
// measured against a real Chrome with the launch flags and this override
// already in place, it changed nothing useful, and two of its edits created
// contradictions a detector could flag: a WebGL renderer claiming a GPU
// under a software-rendered headless context, and a notifications-permission
// query that disagreed with Notification.permission. Patching JS-visible
// properties by hand also means chasing every Chrome release, which is
// exactly how such a script goes stale. What the browser can be configured
// to report truthfully, it is; what it can't (software-rendered WebGL, a
// fixed viewport with no real display behind it), stays honest.
//
// AcceptLanguage is the bare list: Chrome derives navigator.languages by
// splitting it on commas and adds the q-values to the header itself, so a
// "…;q=0.9" here would surface as a bogus "en;q=0.9" language.
func (sp stealthProfile) actions() []chromedp.Action {
	return []chromedp.Action{
		emulation.SetAutomationOverride(false),
		emulation.SetUserAgentOverride(sp.userAgent).
			WithAcceptLanguage("en-US,en").
			WithPlatform(sp.platform).
			WithUserAgentMetadata(sp.meta),
	}
}

// stealthInit reads the running browser's version once and derives its
// stealth profile. ok is false if the version can't be read; callers should
// treat that as "run with only the launch flags", not a fatal error — those
// alone are still a real improvement over chromedp's defaults.
func stealthInit(ctx context.Context, exec cdp.Executor) (profile stealthProfile, ok bool) {
	_, product, _, userAgent, _, err := browser.GetVersion().Do(cdp.WithExecutor(ctx, exec))
	if err != nil {
		return stealthProfile{}, false
	}
	return buildStealthProfile(product, userAgent), true
}
