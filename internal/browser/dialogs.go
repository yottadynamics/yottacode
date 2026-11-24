package browser

import (
	"fmt"
	"time"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// dialogArmTTL bounds how long an armed answer waits for its dialog, so an
// "accept" the agent armed for one click can never answer an unrelated
// confirmation much later.
const dialogArmTTL = 60 * time.Second

// dialogAnswer is a one-shot answer for the next confirm or prompt dialog.
// All open tabs share one value, so whichever tab raises a dialog first takes
// it and the others see it used.
type dialogAnswer struct {
	mu     syncutil.Mutex
	accept bool
	text   string
	until  time.Time
	used   bool
}

// take consumes the answer if it is still unused and unexpired.
func (a *dialogAnswer) take(now time.Time) (accept bool, text string, ok bool) {
	if a == nil {
		return false, "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.used || now.After(a.until) {
		return false, "", false
	}
	a.used = true
	return a.accept, a.text, true
}

func (p *trackedPage) setDialogArm(a *dialogAnswer) {
	p.mu.Lock()
	p.dialogArm = a
	p.mu.Unlock()
}

func (p *trackedPage) currentDialogArm() *dialogAnswer {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dialogArm
}

// answerFor decides how a dialog is answered. alert and beforeunload keep the
// fixed policy (they only inform; refusing beforeunload would block
// navigation). confirm and prompt are cancelled unless an armed answer is
// waiting. The returned note says who decided, for the console record.
func (p *trackedPage) answerFor(kind string, now time.Time) (accept bool, text, note string) {
	switch kind {
	case "alert", "beforeunload":
		return true, "", "automatically"
	}
	if a, t, ok := p.currentDialogArm().take(now); ok {
		return a, t, "by browser_dialog"
	}
	return false, "", "automatically"
}

// armDialog sets (or, with clear, removes) the one-shot answer for the next
// confirm/prompt on every open tab.
func (s *session) armDialog(accept bool, text string, clear bool) error {
	var a *dialogAnswer
	if !clear {
		a = &dialogAnswer{accept: accept, text: text, until: time.Now().Add(dialogArmTTL)}
	}
	s.mu.Lock()
	pages := append([]*trackedPage(nil), s.pages...)
	s.mu.Unlock()
	if len(pages) == 0 {
		return fmt.Errorf("no open page to arm a dialog answer on")
	}
	for _, p := range pages {
		p.setDialogArm(a)
	}
	return nil
}
