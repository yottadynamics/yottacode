package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"syscall"
	"time"
)

// installGoroutineDumpHandler arms SIGUSR1 as a non-destructive diagnostic:
// on receipt it writes every goroutine's full stack (including its wait
// reason — "chan receive", "semacquire", "IO wait", etc.) to a timestamped
// file under ~/.yottacode/debug/, then keeps running. Unlike SIGQUIT (the Go
// runtime's default stack-dump signal), this never kills the process — a
// session that appears hung can be inspected, and either resumed or killed
// deliberately afterward, without losing the one chance to see where it was
// stuck.
//
// `kill -USR1 <pid>` needs no special privileges (same-user signal, not
// ptrace), so it works even when a debugger attach is blocked by the host's
// yama ptrace_scope policy. Re-armed after every dump so a long session can
// be sampled more than once.
func installGoroutineDumpHandler() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			dumpGoroutineStacks()
		}
	}()
}

// dumpGoroutineStacks writes the full-detail goroutine profile (debug=2:
// one stack per goroutine, with state and wait reason) to
// ~/.yottacode/debug/goroutines-<timestamp>.txt and notes the path on
// stderr. Best-effort: a failure to resolve $HOME or create the file is
// reported on stderr rather than propagated — a debug aid must never crash
// the process it's inspecting.
func dumpGoroutineStacks() {
	path, err := goroutineDumpPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[debug] SIGUSR1: %v\n", err)
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[debug] SIGUSR1: create %s: %v\n", path, err)
		return
	}
	defer f.Close()
	if err := pprof.Lookup("goroutine").WriteTo(f, 2); err != nil {
		fmt.Fprintf(os.Stderr, "[debug] SIGUSR1: write stacks: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "[debug] SIGUSR1: goroutine stacks written to %s\n", path)
}

func goroutineDumpPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".yottacode", "debug")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	now := time.Now()
	return filepath.Join(dir, fmt.Sprintf("goroutines-%s-%d.txt", now.Format("20060102-150405.000"), now.UnixNano())), nil
}
