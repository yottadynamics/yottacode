package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/browser"
)

func lastHistory(m Model) string {
	if len(m.historyLines) == 0 {
		return ""
	}
	return m.historyLines[len(m.historyLines)-1]
}

func TestBrowserCommandStates(t *testing.T) {
	m := newTestModel(t)
	m.browserManager = nil
	m, _ = cmdBrowser(m, nil)
	if !strings.Contains(lastHistory(m), "not available") {
		t.Errorf("nil manager: %q", lastHistory(m))
	}

	m = newTestModel(t)
	m.browserManager = browser.NewManager()
	m, _ = cmdBrowser(m, nil)
	if got := lastHistory(m); !strings.Contains(got, "persistent profile is off") || !strings.Contains(got, "persistent_profile = true") {
		t.Errorf("off: %q", got)
	}
	m, _ = cmdBrowser(m, []string{"clear"})
	if got := lastHistory(m); !strings.Contains(got, "persistent profile is off") {
		t.Errorf("clear while off must not delete anything and must explain: %q", got)
	}
}

func TestBrowserCommandStatusAndClear(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	mgr := browser.NewManager()
	if err := mgr.SetPersistentProfile(dir); err != nil {
		t.Fatal(err)
	}
	cookies := filepath.Join(dir, "Default", "Cookies")
	if err := os.MkdirAll(filepath.Dir(cookies), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cookies, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t)
	m.browserManager = mgr

	m, _ = cmdBrowser(m, nil)
	if got := lastHistory(m); !strings.Contains(got, dir) || !strings.Contains(got, "/browser clear") {
		t.Errorf("status: %q", got)
	}
	m, _ = cmdBrowser(m, []string{"clear"})
	if got := lastHistory(m); !strings.Contains(got, "cleared the persistent profile") {
		t.Errorf("clear: %q", got)
	}
	if _, err := os.Stat(cookies); !os.IsNotExist(err) {
		t.Errorf("cookies survived /browser clear: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the profile directory itself must remain usable: %v", err)
	}
	m, _ = cmdBrowser(m, []string{"bogus"})
	if got := lastHistory(m); !strings.Contains(got, "usage: /browser") {
		t.Errorf("usage: %q", got)
	}
}
