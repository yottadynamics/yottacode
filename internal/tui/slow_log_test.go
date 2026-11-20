package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSlowUpdateLogRecordsOnlyAboveThreshold(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".yottacode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if newSlowUpdateLog("") != nil || newSlowUpdateLog("0") != nil {
		t.Fatal("unset/zero threshold must disable the log")
	}
	if got := newSlowUpdateLog("999999999999"); got == nil || got.threshold != maxSlowLogMS*time.Millisecond {
		t.Fatalf("huge threshold must clamp, got %+v", got)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".yottacode", "tui-slow.log")); err == nil {
		t.Fatal("constructing the log must not create the file")
	}
	l := newSlowUpdateLog("20")
	l.record("update fast", 5*time.Millisecond, 1)
	l.record("update slow", 30*time.Millisecond, 7)
	b, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".yottacode", "tui-slow.log"))
	if strings.Contains(string(b), "fast") || !strings.Contains(string(b), "update slow") || !strings.Contains(string(b), "rows=7") {
		t.Fatalf("log = %q", b)
	}
}
