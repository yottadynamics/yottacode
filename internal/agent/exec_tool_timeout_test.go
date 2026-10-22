//go:build unix

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pidAlive reports whether pid still names a live process. A zombie counts as
// dead for our purposes: once reaped-or-killed the command is gone.
func pidAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if _, perr := os.Stat("/proc/self"); perr != nil {
			return true // no procfs (non-Linux): kill(0) succeeding is all we know
		}
		return false // procfs exists but the entry vanished: reaped between the two checks
	}
	// State is the field after the ")" closing the comm.
	s := string(b)
	if i := strings.LastIndex(s, ")"); i >= 0 && len(s) > i+2 {
		return s[i+2] != 'Z'
	}
	// A process being torn down can yield an empty or truncated stat read even
	// though the open succeeded. There is no live state to report, so it is gone.
	return false
}

func TestRunBashTimeout_Resolution(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	cases := []struct {
		name string
		in   *int64
		want time.Duration
	}{
		{"absent uses default", nil, runBashDefaultTimeout},
		{"zero uses default", i64(0), runBashDefaultTimeout},
		{"negative uses default", i64(-5), runBashDefaultTimeout},
		{"explicit value honored", i64(1500), 1500 * time.Millisecond},
		{"at cap", i64(runBashMaxTimeout.Milliseconds()), runBashMaxTimeout},
		{"above cap clamped", i64(runBashMaxTimeout.Milliseconds() + 1), runBashMaxTimeout},
		{"huge value clamped without overflow", i64(1 << 62), runBashMaxTimeout},
	}
	for _, tc := range cases {
		if got := runBashTimeout(tc.in); got != tc.want {
			t.Errorf("%s: runBashTimeout = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRunBashTool_TimeoutKillsCommandAndReportsPartialOutput(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	start := time.Now()
	out, err := tool.Execute(context.Background(), `{"command":"echo partial; sleep 30","timeout_ms":300}`)
	if err != nil {
		t.Fatalf("a timeout must be a recoverable result, got error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Execute took %v; timeout did not fire promptly", elapsed)
	}
	for _, want := range []string{"exit=124", "timed out after 300ms", "partial"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

// The failure that motivated the timeout: a shell blocked opening a FIFO with
// no writer (grep -R reaching a named pipe) never produces output or exits.
func TestRunBashTool_TimeoutUnblocksFIFORead(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	tool := &RunBashTool{Cwd: NewCwdRef(dir)}
	start := time.Now()
	out, err := tool.Execute(context.Background(), `{"command":"cat pipe | head -1","timeout_ms":300}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("FIFO read was not unblocked: took %v", elapsed)
	}
	if !strings.Contains(out, "timed out after") {
		t.Errorf("expected timeout result, got %q", out)
	}
}

// A backgrounded grandchild must die with the group, not outlive the call.
func TestRunBashTool_TimeoutKillsGrandchildProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	cmd := fmt.Sprintf("sleep 60 & echo $! > %s; wait", pidFile)
	tool := &RunBashTool{Cwd: NewCwdRef(dir)}
	if _, err := tool.Execute(context.Background(), fmt.Sprintf(`{"command":%q,"timeout_ms":500}`, cmd)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("grandchild never recorded its pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("bad pid %q: %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// Wait until the grandchild is observed dead at least once. Death is
	// monotonic, so this cannot flap the way "poll until dead, then re-check
	// alive" can while a killed process is mid-teardown.
	eventually(t, fmt.Sprintf("grandchild %d to die with the process group", pid), func() bool { return !pidAlive(pid) })
}

func TestRunBashTool_DefaultTimeoutAppliesWhenUnspecified(t *testing.T) {
	prev := runBashDefaultTimeout
	runBashDefaultTimeout = 300 * time.Millisecond
	t.Cleanup(func() { runBashDefaultTimeout = prev })

	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	out, err := tool.Execute(context.Background(), `{"command":"sleep 30"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "timed out after 300ms") {
		t.Errorf("default timeout not applied: %q", out)
	}
}

func TestRunBashTool_FastCommandUnaffectedByTimeout(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	out, err := tool.Execute(context.Background(), `{"command":"echo ok","timeout_ms":5000}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "exit=0") || strings.Contains(out, "timed out") {
		t.Errorf("fast command misreported: %q", out)
	}
}

// A caller cancel (Esc) must not be mislabeled as a timeout.
func TestRunBashTool_ParentCancelIsNotReportedAsTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	out, _ := tool.Execute(ctx, `{"command":"sleep 30","timeout_ms":60000}`)
	if strings.Contains(out, "timed out") {
		t.Errorf("parent cancel reported as timeout: %q", out)
	}
}

func TestRunBashTool_SchemaAndDescriptionAdvertiseTimeout(t *testing.T) {
	tool := &RunBashTool{}
	props, ok := tool.Schema()["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties map")
	}
	if _, ok := props["timeout_ms"]; !ok {
		t.Errorf("schema missing timeout_ms")
	}
	if req, _ := tool.Schema()["required"].([]string); len(req) != 1 || req[0] != "command" {
		t.Errorf("timeout_ms must stay optional; required = %v", req)
	}
	if d := tool.Description(); !strings.Contains(d, "timeout_ms") {
		t.Errorf("description does not mention timeout_ms: %q", d)
	}
}
