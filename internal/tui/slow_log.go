package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// slowUpdateLog is an opt-in diagnostic for UI stalls: with
// YOTTACODE_TUI_SLOW_LOG=<ms> set, every Update (and View) slower than the
// threshold is appended to ~/.yottacode/tui-slow.log with the message type,
// so a "cursor keeps moving after I release the key" report can be traced to
// the handler that blocked the loop. Disabled (one nil check) when the
// variable is unset or invalid.
//
// Nothing is opened at package init: the variable is only parsed into a
// threshold, and the file is opened, appended to and closed per slow event
// (rare by definition), so there is no descriptor to leak and no file is
// created unless a stall actually happens.
type slowUpdateLog struct {
	threshold time.Duration
}

const maxSlowLogMS = 60_000

var slowLog = newSlowUpdateLog(os.Getenv("YOTTACODE_TUI_SLOW_LOG"))

func newSlowUpdateLog(env string) *slowUpdateLog {
	var ms int
	if _, err := fmt.Sscanf(env, "%d", &ms); err != nil || ms <= 0 {
		return nil
	}
	ms = min(ms, maxSlowLogMS) // also keeps the Duration multiply from overflowing
	return &slowUpdateLog{threshold: time.Duration(ms) * time.Millisecond}
}

// record logs what took d when it crossed the threshold. what is a
// description of the work, e.g. "update tea.KeyPressMsg".
func (l *slowUpdateLog) record(what string, d time.Duration, rows int) {
	if l == nil || d < l.threshold {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".yottacode")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "tui-slow.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %-40s %8.1fms rows=%d\n", time.Now().Format("15:04:05.000"), what, float64(d.Microseconds())/1000, rows)
}
