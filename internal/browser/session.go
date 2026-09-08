package browser

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// NavigateResult is the outcome of a successful browser_navigate call.
type NavigateResult struct {
	URL   string
	Title string
}

// pageSession is the browser-package-internal surface Manager drives.
// *session is the real go-rod/CDP implementation; Manager's tests inject a
// fake to exercise lifecycle/serialization/error-mapping without spawning
// a real Chrome process.
type pageSession interface {
	navigate(ctx context.Context, url, waitUntil string) (NavigateResult, error)
	screenshot(ctx context.Context, selector string, fullPage bool) ([]byte, error)
	inspect(ctx context.Context, selector string) (string, error)
	click(ctx context.Context, selector string) error
	typeText(ctx context.Context, selector, text string, submit bool) error
	hotkey(ctx context.Context, keys string) error
	scroll(ctx context.Context, selector string, deltaX, deltaY float64) error
	wait(ctx context.Context, selector, text string, networkIdle bool, timeout time.Duration) error
	currentURL() string
	close() error
	// alive reports whether the underlying browser process is still
	// running — see Manager.ensureLocked's crash-recovery check.
	alive() bool
	// forceCleanup best-effort tears down the OS process and launcher
	// state without any CDP round-trip — used instead of close() when
	// alive() is already false, since a graceful Browser.Close() over a
	// connection to a process that's already dead has nothing to
	// negotiate with and could hang rather than fail fast.
	forceCleanup()
}

// session is a thin wrapper over one launched *rod.Browser and its single
// active *rod.Page. It owns no lifecycle policy (lazy launch, closed-once
// guard, mutex) — that's Manager's job; session only knows how to perform
// one already-launched browser's actions.
type session struct {
	launcher *launcher.Launcher
	browser  *rod.Browser
	page     *rod.Page
}

// launchSession starts a headless Chromium/Chrome using the given binary
// and isolated profile dir, and opens its single active page. bin and
// profileDir are always explicit (see findChromeBinary/newProfileDir) so
// rod never falls back to auto-downloading its own pinned build.
//
// ctx bounds only the launch itself (a binary that starts but never opens
// its debug port would otherwise hang Launch forever — see Launcher.Context).
// It is deliberately NOT threaded onto the returned Browser/Page: those are
// reused across every later action for the rest of the session, each of
// which binds its own per-call context (see every session method's
// `s.page.Context(ctx)`), so tying the long-lived connection itself to one
// call's short deadline would cancel it out from under every later call.
func launchSession(ctx context.Context, bin, profileDir string) (pageSession, error) {
	l := launcher.New().
		Bin(bin).
		Headless(true).
		UserDataDir(profileDir).
		Leakless(true).
		Context(ctx)

	u, err := l.Launch()
	if err != nil {
		l.Cleanup()
		return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
	}

	br := rod.New().ControlURL(u)
	if err := br.Connect(); err != nil {
		l.Kill()
		l.Cleanup()
		return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
	}

	pg, err := br.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		_ = br.Close()
		l.Kill()
		l.Cleanup()
		return nil, fmt.Errorf("%w: %v", ErrLaunchFailed, err)
	}

	// A JS-initiated dialog (alert/confirm/prompt/beforeunload) blocks the
	// page's own execution — and every browser_* call that touches it —
	// until something answers Page.handleJavaScriptDialog. There is no
	// human here to click it, so auto-dismiss every one instead of letting
	// a tool call hang until its timeout. Dismiss (not accept) is the
	// safer default: the triggering click/navigate was already
	// approval-gated, but a confirm() can gate its own separate
	// destructive action (e.g. "Are you sure you want to delete this?"),
	// and silently accepting that goes beyond what the click's approval
	// covered. This is rod's own documented idiom for the purpose (see
	// Page.EachEvent's doc example) and needs no explicit teardown: the
	// subscription ends on its own once the page/browser closes.
	go pg.EachEvent(func(e *proto.PageJavascriptDialogOpening) {
		_ = proto.PageHandleJavaScriptDialog{Accept: false}.Call(pg)
	})()

	return &session{launcher: l, browser: br, page: pg}, nil
}

func (s *session) close() error {
	err := s.browser.Close()
	s.launcher.Kill()
	s.launcher.Cleanup()
	return err
}

func (s *session) currentURL() string {
	info, err := s.page.Info()
	if err != nil {
		return ""
	}
	return info.URL
}

// alive checks whether the launched OS process is still running, via a
// zero-signal existence probe (see kill(2) — signal 0 sends nothing, it
// only reports ESRCH if the pid is gone). This is deliberately a process
// check, not a CDP round-trip: it's what lets Manager.ensureLocked notice
// a crashed browser (OOM-killed, killed externally, or just crashed)
// cheaply, before attempting to reuse a connection that's already dead.
//
// PID reuse by the OS could in principle make a long-dead process look
// alive again, but that requires the OS to cycle back to the exact same
// pid within one session's lifetime — a risk this crash check accepts as
// negligible in exchange for not needing rod's private launch-exit
// channel (not part of Launcher's public API).
func (s *session) alive() bool {
	pid := s.launcher.PID()
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// forceCleanup skips the CDP-level Browser.Close() — there's no live
// connection to negotiate a graceful shutdown with — and only reaps the
// OS process and launcher state.
func (s *session) forceCleanup() {
	s.launcher.Kill()
	s.launcher.Cleanup()
}

// classifyErr wraps a raw rod/CDP error with the taxonomy sentinel that
// best describes it, so tool callers can use errors.Is regardless of the
// underlying error's concrete type. fallback is used for a plain
// context-deadline error, which carries no type information of its own.
func classifyErr(err error, fallback error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*rod.ElementNotFoundError](err); ok {
		return fmt.Errorf("%w: %v", ErrSelectorNotFound, err)
	}
	if _, ok := errors.AsType[*rod.NavigationError](err); ok {
		return fmt.Errorf("%w: %v", ErrNavigationTimeout, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", fallback, err)
	}
	return err
}

func (s *session) element(ctx context.Context, selector string) (*rod.Element, error) {
	el, err := s.page.Context(ctx).Element(selector)
	if err != nil {
		return nil, classifyErr(err, ErrSelectorNotFound)
	}
	return el, nil
}

// networkIdleWindow is how long the network must stay quiet before
// "networkidle" is considered satisfied — a fixed heuristic quiet-period
// (the same order of magnitude Playwright's own networkidle uses), not an
// overall deadline. The overall deadline for the call is whatever ctx
// already carries (see Manager.withActionTimeout / Manager.Wait).
const networkIdleWindow = 500 * time.Millisecond

func waitUntilCondition(pg *rod.Page, waitUntil string) error {
	switch strings.ToLower(strings.TrimSpace(waitUntil)) {
	case "networkidle":
		return pg.WaitIdle(networkIdleWindow)
	case "domcontentloaded":
		return pg.WaitDOMStable(300*time.Millisecond, 0)
	default: // "", "load"
		return pg.WaitLoad()
	}
}

func (s *session) navigate(ctx context.Context, url, waitUntil string) (NavigateResult, error) {
	pg := s.page.Context(ctx)
	if err := pg.Navigate(url); err != nil {
		return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
	}
	if err := waitUntilCondition(pg, waitUntil); err != nil {
		return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
	}
	info, err := pg.Info()
	if err != nil {
		return NavigateResult{}, classifyErr(err, ErrNavigationTimeout)
	}
	return NavigateResult{URL: info.URL, Title: info.Title}, nil
}

func (s *session) screenshot(ctx context.Context, selector string, fullPage bool) ([]byte, error) {
	pg := s.page.Context(ctx)
	if strings.TrimSpace(selector) != "" {
		el, err := s.element(ctx, selector)
		if err != nil {
			return nil, err
		}
		data, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
		if err != nil {
			return nil, fmt.Errorf("browser screenshot: %w", err)
		}
		return data, nil
	}
	data, err := pg.Screenshot(fullPage, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		return nil, fmt.Errorf("browser screenshot: %w", err)
	}
	return data, nil
}

func (s *session) inspect(ctx context.Context, selector string) (string, error) {
	pg := s.page.Context(ctx)
	_ = proto.AccessibilityEnable{}.Call(pg)

	if strings.TrimSpace(selector) == "" {
		res, err := proto.AccessibilityGetFullAXTree{}.Call(pg)
		if err != nil {
			return "", fmt.Errorf("browser inspect: %w", err)
		}
		return renderAXTree(res.Nodes), nil
	}

	el, err := s.element(ctx, selector)
	if err != nil {
		return "", err
	}
	res, err := proto.AccessibilityGetPartialAXTree{ObjectID: el.Object.ObjectID, FetchRelatives: true}.Call(pg)
	if err != nil {
		return "", fmt.Errorf("browser inspect: %w", err)
	}
	return renderAXTree(res.Nodes), nil
}

func (s *session) click(ctx context.Context, selector string) error {
	el, err := s.element(ctx, selector)
	if err != nil {
		return err
	}
	if _, err := el.WaitInteractable(); err != nil {
		return classifyErr(err, ErrSelectorNotFound)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("browser click: %w", err)
	}
	return nil
}

func (s *session) typeText(ctx context.Context, selector, text string, submit bool) error {
	el, err := s.element(ctx, selector)
	if err != nil {
		return err
	}
	if _, err := el.WaitInteractable(); err != nil {
		return classifyErr(err, ErrSelectorNotFound)
	}
	if err := el.Input(text); err != nil {
		return fmt.Errorf("browser type: %w", err)
	}
	if submit {
		if err := el.Type(input.Enter); err != nil {
			return fmt.Errorf("browser type: submit: %w", err)
		}
	}
	return nil
}

func (s *session) hotkey(ctx context.Context, keys string) error {
	combo, err := parseHotkey(keys)
	if err != nil {
		return err
	}
	ka := s.page.Context(ctx).KeyActions()
	for _, k := range combo.modifiers {
		ka = ka.Press(k)
	}
	ka = ka.Type(combo.main)
	for i := len(combo.modifiers) - 1; i >= 0; i-- {
		ka = ka.Release(combo.modifiers[i])
	}
	if err := ka.Do(); err != nil {
		return fmt.Errorf("browser hotkey %q: %w", keys, err)
	}
	return nil
}

func (s *session) scroll(ctx context.Context, selector string, deltaX, deltaY float64) error {
	pg := s.page.Context(ctx)
	if strings.TrimSpace(selector) != "" {
		el, err := s.element(ctx, selector)
		if err != nil {
			return err
		}
		if err := el.ScrollIntoView(); err != nil {
			return fmt.Errorf("browser scroll: %w", err)
		}
		return nil
	}

	metrics, err := proto.PageGetLayoutMetrics{}.Call(pg)
	if err != nil {
		return fmt.Errorf("browser scroll: %w", err)
	}
	var cx, cy float64
	if metrics.CSSLayoutViewport != nil {
		cx = float64(metrics.CSSLayoutViewport.ClientWidth) / 2
		cy = float64(metrics.CSSLayoutViewport.ClientHeight) / 2
	}
	if err := pg.Mouse.MoveTo(proto.NewPoint(cx, cy)); err != nil {
		return fmt.Errorf("browser scroll: %w", err)
	}
	if err := pg.Mouse.Scroll(deltaX, deltaY, 1); err != nil {
		return fmt.Errorf("browser scroll: %w", err)
	}
	return nil
}

// wait assumes the caller (Manager.Wait) has already resolved timeout to
// a positive value — the "built-in timeout" the tool docs promise, not
// left unbounded.
func (s *session) wait(ctx context.Context, selector, text string, networkIdle bool, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pg := s.page.Context(ctx)

	if networkIdle {
		if err := pg.WaitIdle(networkIdleWindow); err != nil {
			return classifyErr(err, ErrNavigationTimeout)
		}
	}
	if strings.TrimSpace(selector) != "" {
		el, err := pg.Element(selector)
		if err != nil {
			return classifyErr(err, ErrSelectorNotFound)
		}
		if err := el.WaitVisible(); err != nil {
			return classifyErr(err, ErrSelectorNotFound)
		}
	}
	if strings.TrimSpace(text) != "" {
		if _, err := pg.ElementR("*", regexp.QuoteMeta(text)); err != nil {
			return classifyErr(err, ErrSelectorNotFound)
		}
	}
	return nil
}
