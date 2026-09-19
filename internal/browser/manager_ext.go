package browser

import "context"

// Eval evaluates a JavaScript expression in the active page and returns
// its JSON-rendered result, lazily launching the browser like every other
// action call. See session.eval for the semantics.
func (m *Manager) Eval(ctx context.Context, expression string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return "", err
	}
	return s.eval(ctx, expression)
}

// Back navigates the active page to its previous history entry.
func (m *Manager) Back(ctx context.Context) (NavigateResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return NavigateResult{}, err
	}
	return s.back(ctx)
}

// ScreenshotAnnotated captures the page with a numbered box drawn over every
// interactive control, where box N is element ref @eN — see
// session.screenshotAnnotated. It refreshes the page's refs as a side effect.
func (m *Manager) ScreenshotAnnotated(ctx context.Context, fullPage bool) (AnnotatedShot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.withActionTimeout(ctx)
	defer cancel()
	s, err := m.ensureLocked(ctx)
	if err != nil {
		return AnnotatedShot{}, err
	}
	return s.screenshotAnnotated(ctx, fullPage)
}

// SetDialogPolicy sets how JS dialogs (alert/confirm/prompt/beforeunload)
// are answered: accepted (with promptText as a prompt() answer; empty
// means the prompt's own default) or dismissed. It never launches a
// browser — the policy is remembered and applied to whatever session
// exists now or is launched later, including a relaunch after a crash.
func (m *Manager) SetDialogPolicy(accept bool, promptText string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrActionDenied
	}
	m.dialogAccept, m.dialogPrompt = accept, promptText
	if m.sess != nil {
		m.sess.setDialogPolicy(accept, promptText)
	}
	return nil
}

// DialogPolicy reports the current policy, for browser_status.
func (m *Manager) DialogPolicy() (accept bool, promptText string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dialogAccept, m.dialogPrompt
}
