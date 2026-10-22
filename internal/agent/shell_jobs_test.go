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

	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// jobsHarness is a ShellJobs wired to a real task registry plus a recorder for
// the completion events the TUI would receive.
type jobsHarness struct {
	jobs   *ShellJobs
	tasks  *subagents.Registry
	mu     syncutil.Mutex
	events []SubagentBackgroundDone
}

func newJobsHarness(t *testing.T) *jobsHarness {
	t.Helper()
	h := &jobsHarness{tasks: subagents.NewRegistry()}
	a := &AgentTool{Tasks: h.tasks, AllowBackground: true}
	a.SetBackgroundDoneCallback(func(ev SubagentBackgroundDone) {
		h.mu.Lock()
		h.events = append(h.events, ev)
		h.mu.Unlock()
	})
	h.jobs = NewShellJobs(a)
	t.Cleanup(func() { _ = h.jobs.Cleanup(context.Background()) })
	return h
}

func (h *jobsHarness) eventsFor(id string) []SubagentBackgroundDone {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []SubagentBackgroundDone
	for _, e := range h.events {
		if e.TaskID == id {
			out = append(out, e)
		}
	}
	return out
}

func (h *jobsHarness) start(t *testing.T, command string, notify bool) *shellJob {
	t.Helper()
	job, err := h.jobs.Start(HostSandbox{}, command, t.TempDir(), notify)
	if err != nil {
		t.Fatalf("Start(%q): %v", command, err)
	}
	return job
}

func waitJob(t *testing.T, j *shellJob) {
	t.Helper()
	select {
	case <-j.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("job %s did not finish", j.id)
	}
}

// eventually polls cond because the completion event is published just after
// the job's done channel closes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestJobBuffer_CursorAndDroppedAccounting(t *testing.T) {
	b := newJobBuffer(10)
	_, _ = b.Write([]byte("hello"))
	got, next, lost := b.readFrom(0, 100)
	if got != "hello" || next != 5 || lost != 0 {
		t.Fatalf("first read = %q,%d,%d", got, next, lost)
	}
	_, _ = b.Write([]byte(" world"))
	got, next, lost = b.readFrom(next, 100)
	if got != " world" || next != 11 || lost != 0 {
		t.Fatalf("incremental read = %q,%d,%d", got, next, lost)
	}
}

func TestJobBuffer_RetainsOnlyNewestAndReportsLoss(t *testing.T) {
	b := newJobBuffer(10)
	for i := range 100 {
		_, _ = fmt.Fprintf(b, "%02d|", i)
	}
	// 300 bytes written; only the newest 10 are readable.
	got, next, lost := b.readFrom(0, 1000)
	if next != 300 {
		t.Fatalf("next = %d, want 300", next)
	}
	if len(got) != 10 || !strings.HasSuffix(got, "99|") {
		t.Fatalf("retained window = %q", got)
	}
	if lost != 290 {
		t.Fatalf("lost = %d, want 290", lost)
	}
	if n := len(b.data); n > 2*b.limit {
		t.Fatalf("backing slice grew to %d, want <= %d", n, 2*b.limit)
	}
}

func TestJobBuffer_ReadMaxKeepsNewestAndCountsSkipped(t *testing.T) {
	b := newJobBuffer(1000)
	_, _ = b.Write([]byte("0123456789"))
	got, next, lost := b.readFrom(0, 4)
	if got != "6789" || next != 10 || lost != 6 {
		t.Fatalf("capped read = %q,%d,%d, want 6789,10,6", got, next, lost)
	}
}

func TestJobBuffer_TailLines(t *testing.T) {
	b := newJobBuffer(1000)
	_, _ = b.Write([]byte("a\nb\nc\nd\n"))
	got, cut := b.tailLines(2, 1000)
	if got != "c\nd\n" || !cut {
		t.Fatalf("tailLines = %q,%v", got, cut)
	}
	empty := newJobBuffer(10)
	if got, _ := empty.tailLines(5, 100); got != "(none)\n" {
		t.Fatalf("empty tail = %q", got)
	}
}

func TestShellJobs_RunsToCompletionAndWaitReturnsOutput(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "echo first; sleep 0.2; echo second; echo oops >&2", false)

	out, err := h.jobs.Wait(context.Background(), job.id, 10*time.Second)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	for _, want := range []string{"status: exited (exit=0)", "first", "second", "oops", "session_id: " + job.id} {
		if !strings.Contains(out, want) {
			t.Errorf("Wait output missing %q:\n%s", want, out)
		}
	}
	task, ok := h.tasks.Get(job.id)
	if !ok || task.AgentType != ShellTaskType || !task.NonAgent || !task.Background {
		t.Fatalf("task not registered as a background shell task: %+v", task)
	}
	eventually(t, "task terminal", func() bool { t2, _ := h.tasks.Get(job.id); return t2.Status == subagents.TaskCompleted })
}

func TestShellJobs_NonZeroExitIsErrored(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "echo bad; exit 3", true)
	waitJob(t, job)
	eventually(t, "completion event", func() bool { return len(h.eventsFor(job.id)) == 1 })
	ev := h.eventsFor(job.id)[0]
	if !ev.Errored || !strings.Contains(ev.Result, "exit=3") || !strings.Contains(ev.Result, "bad") {
		t.Fatalf("event = %+v", ev)
	}
	if !ev.NotifyOnDone || ev.AgentType != ShellTaskType {
		t.Fatalf("a notify job nobody collected must request a wake: %+v", ev)
	}
}

func TestShellJobs_PollReturnsOnlyNewOutput(t *testing.T) {
	h := newJobsHarness(t)
	dir := t.TempDir()
	gate := filepath.Join(dir, "go")
	// The markers are arithmetic so they appear only in the output, never in
	// the command line echoed back in the status header.
	job, err := h.jobs.Start(HostSandbox{}, "echo $((40+2)); while [ ! -e "+gate+" ]; do sleep 0.05; done; echo $((50+5))", dir, false)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "first output", func() bool {
		out, _ := h.jobs.Poll(job.id)
		return strings.Contains(out, "\n42\n")
	})
	out, _ := h.jobs.Poll(job.id)
	if strings.Contains(out, "\n42\n") || !strings.Contains(out, "(none)") || !strings.Contains(out, "status: running") {
		t.Fatalf("second poll must show no new output while running:\n%s", out)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitJob(t, job)
	out, _ = h.jobs.Poll(job.id)
	if !strings.Contains(out, "\n55\n") || strings.Contains(out, "\n42\n") {
		t.Fatalf("final poll should carry only the new output:\n%s", out)
	}
}

func TestShellJobs_WaitTimesOutWhileStillRunning(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "sleep 30", false)
	start := time.Now()
	out, err := h.jobs.Wait(context.Background(), job.id, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Wait overran its timeout")
	}
	if !strings.Contains(out, "status: running") {
		t.Fatalf("expected still running:\n%s", out)
	}
}

func TestShellJobs_WaitHonorsContextCancel(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "sleep 30", false)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	if _, err := h.jobs.Wait(ctx, job.id, time.Minute); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Wait ignored context cancellation")
	}
}

func TestShellJobs_WaiterReceivesCompletionSoNoWake(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "sleep 0.3; echo done", true)
	if _, err := h.jobs.Wait(context.Background(), job.id, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	eventually(t, "completion event", func() bool { return len(h.eventsFor(job.id)) == 1 })
	if h.eventsFor(job.id)[0].NotifyOnDone {
		t.Fatalf("model already collected the result via wait; a wake would duplicate it")
	}
}

func TestShellJobs_PolledFinishedJobDoesNotWakeLater(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "echo hi", false)
	waitJob(t, job)
	if out, _ := h.jobs.Poll(job.id); !strings.Contains(out, "exited (exit=0)") {
		t.Fatalf("poll after exit:\n%s", out)
	}
	j, _ := h.jobs.lookup(job.id)
	j.mu.Lock()
	consumed := j.consumed
	j.mu.Unlock()
	if !consumed {
		t.Fatalf("poll of a finished job must mark the outcome as seen")
	}
}

func TestShellJobs_KillStopsProcessAndSuppressesWake(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "sleep 60", true)
	out, err := h.jobs.Kill(context.Background(), job.id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "status: stopped") {
		t.Fatalf("kill result:\n%s", out)
	}
	eventually(t, "completion event", func() bool { return len(h.eventsFor(job.id)) == 1 })
	if h.eventsFor(job.id)[0].NotifyOnDone {
		t.Fatalf("the model killed it itself; no wake")
	}
	eventually(t, "process gone", func() bool { return !pidAlive(job.pid) })
	task, _ := h.tasks.Get(job.id)
	if task.Status != subagents.TaskCanceled {
		t.Fatalf("task status = %v, want canceled", task.Status)
	}
}

func TestShellJobs_KillAlreadyFinishedJobIsHarmless(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "true", false)
	waitJob(t, job)
	out, err := h.jobs.Kill(context.Background(), job.id)
	if err != nil || !strings.Contains(out, "exited (exit=0)") {
		t.Fatalf("Kill on finished job = %q, %v", out, err)
	}
}

// Kill racing a natural exit must publish exactly one terminal event.
func TestShellJobs_KillRacingNaturalExitFinishesOnce(t *testing.T) {
	h := newJobsHarness(t)
	for range 25 {
		job := h.start(t, "true", true)
		_, _ = h.jobs.Kill(context.Background(), job.id)
		waitJob(t, job)
		eventually(t, "event", func() bool { return len(h.eventsFor(job.id)) >= 1 })
	}
	time.Sleep(100 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]int{}
	for _, e := range h.events {
		seen[e.TaskID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("job %s published %d terminal events, want 1", id, n)
		}
	}
}

func TestShellJobs_StopViaRegistryCancelsTheProcess(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "sleep 60", true)
	if !h.tasks.Cancel(job.id) {
		t.Fatalf("registry refused to cancel a running shell task")
	}
	waitJob(t, job)
	eventually(t, "process gone", func() bool { return !pidAlive(job.pid) })
	if task, _ := h.tasks.Get(job.id); task.Status != subagents.TaskCanceled || !task.CanceledByUser {
		t.Fatalf("task = %+v, want canceled by user (/subagents stop)", task)
	}
}

func TestShellJobs_CleanupKillsRunningJobsAndRefusesNewOnes(t *testing.T) {
	h := newJobsHarness(t)
	a := h.start(t, "sleep 60", false)
	b := h.start(t, "sleep 60", false)
	if err := h.jobs.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	for _, j := range []*shellJob{a, b} {
		if pidAlive(j.pid) {
			t.Errorf("job %s (pid %d) survived Cleanup", j.id, j.pid)
		}
	}
	if _, err := h.jobs.Start(HostSandbox{}, "true", t.TempDir(), false); err == nil {
		t.Fatalf("Start after Cleanup must be refused")
	}
}

func TestShellJobs_KillsWholeProcessGroup(t *testing.T) {
	h := newJobsHarness(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	job, err := h.jobs.Start(HostSandbox{}, "sleep 60 & echo $! > "+pidFile+"; wait", dir, false)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "child pid file", func() bool { b, _ := os.ReadFile(pidFile); return strings.TrimSpace(string(b)) != "" })
	raw, _ := os.ReadFile(pidFile)
	child, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })

	if _, err := h.jobs.Kill(context.Background(), job.id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "grandchild gone", func() bool { return !pidAlive(child) })
}

func TestShellJobs_OutputBeyondTheCapIsDroppedAndReported(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "head -c 700000 /dev/zero | tr '\\0' x; echo END", false)
	waitJob(t, job)
	out, _ := h.jobs.Poll(job.id)
	if !strings.Contains(out, "earlier bytes were dropped") {
		t.Fatalf("expected a dropped-bytes notice")
	}
	if !strings.Contains(out, "END") {
		t.Fatalf("poll must keep the newest output")
	}
	if len(out) > shellPollMaxBytes+1024 {
		t.Fatalf("poll returned %d bytes, want <= ~%d", len(out), shellPollMaxBytes)
	}
}

func TestShellJobs_LogDoesNotMoveThePollCursor(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "printf 'a\\nb\\nc\\n'", false)
	waitJob(t, job)
	logOut, err := h.jobs.Log(job.id, 2)
	if err != nil || !strings.Contains(logOut, "b\nc") || strings.Contains(logOut, "\na\n") {
		t.Fatalf("Log(2) = %q, %v", logOut, err)
	}
	if out, _ := h.jobs.Poll(job.id); !strings.Contains(out, "a\nb\nc") {
		t.Fatalf("Poll after Log must still return everything:\n%s", out)
	}
}

func TestShellJobs_LookupByPrefixAndErrors(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "true", false)
	if _, err := h.jobs.lookup(job.id[:6]); err != nil {
		t.Fatalf("unambiguous prefix rejected: %v", err)
	}
	if _, err := h.jobs.lookup(job.id[:2]); err == nil {
		t.Fatalf("a 2-character prefix must not match")
	}
	if _, err := h.jobs.lookup(""); err == nil {
		t.Fatalf("empty id must be rejected")
	}
	if _, err := h.jobs.lookup("deadbeefdeadbeef"); err == nil || !strings.Contains(err.Error(), "unknown session_id") {
		t.Fatalf("unknown id error = %v", err)
	}
}

func TestShellJobs_FinishedJobsExpireAfterTTL(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "true", false)
	waitJob(t, job)
	if _, err := h.jobs.lookup(job.id); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	h.jobs.now = func() time.Time { return base.Add(shellJobFinishedTTL + time.Minute) }
	if _, err := h.jobs.lookup(job.id); err == nil {
		t.Fatalf("finished job should have expired")
	}
}

func TestShellJobs_CapRefusesWhenAllRunningAndEvictsOldestFinished(t *testing.T) {
	prev := shellJobMaxTracked
	shellJobMaxTracked = 2
	t.Cleanup(func() { shellJobMaxTracked = prev })

	h := newJobsHarness(t)
	quick := h.start(t, "true", false)
	waitJob(t, quick)
	long := h.start(t, "sleep 60", false)
	// Full (1 finished + 1 running): the finished one is evicted to admit a new job.
	other := h.start(t, "sleep 60", false)
	if _, err := h.jobs.lookup(quick.id); err == nil {
		t.Fatalf("oldest finished job should have been evicted")
	}
	// Full of running jobs: refuse.
	if _, err := h.jobs.Start(HostSandbox{}, "true", t.TempDir(), false); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("expected a cap refusal, got %v", err)
	}
	_ = long
	_ = other
}

func TestShellJobs_UnavailableWhenSessionCannotHostBackground(t *testing.T) {
	a := &AgentTool{Tasks: subagents.NewRegistry(), AllowBackground: false}
	jobs := NewShellJobs(a)
	if jobs.Available() {
		t.Fatalf("Available() must be false when background is not allowed")
	}
	if _, err := jobs.Start(HostSandbox{}, "true", t.TempDir(), false); err != ErrShellJobsUnavailable {
		t.Fatalf("Start = %v, want ErrShellJobsUnavailable", err)
	}
	var nilJobs *ShellJobs
	if nilJobs.Available() {
		t.Fatalf("nil ShellJobs must be unavailable")
	}
}

func TestShellJobs_SurvivesTurnContextCancellation(t *testing.T) {
	h := newJobsHarness(t)
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Jobs: h.jobs}
	ctx, cancel := context.WithCancel(context.Background())
	out, err := tool.Execute(ctx, `{"command":"sleep 60","background":true}`)
	if err != nil {
		t.Fatal(err)
	}
	id := sessionIDFrom(t, out)
	cancel() // Esc ends the turn
	time.Sleep(300 * time.Millisecond)
	j, _ := h.jobs.lookup(id)
	if !pidAlive(j.pid) {
		t.Fatalf("a background job must outlive the turn that started it")
	}
}

func sessionIDFrom(t *testing.T, out string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "session_id: "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("no session_id in %q", out)
	return ""
}

func TestRunBashTool_BackgroundStartResultAndProcessTool(t *testing.T) {
	h := newJobsHarness(t)
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Jobs: h.jobs}
	proc := &ProcessTool{Jobs: h.jobs}

	out, err := tool.Execute(context.Background(), `{"command":"echo bg-output; sleep 0.2","background":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "started background job") || !strings.Contains(out, "notify_on_complete: false") {
		t.Fatalf("start result:\n%s", out)
	}
	id := sessionIDFrom(t, out)

	waited, err := proc.Execute(context.Background(), fmt.Sprintf(`{"action":"wait","session_id":%q,"timeout_ms":10000}`, id))
	if err != nil || !strings.Contains(waited, "bg-output") || !strings.Contains(waited, "exited (exit=0)") {
		t.Fatalf("process wait = %q, %v", waited, err)
	}
}

func TestRunBashTool_NotifyOnCompleteStartResultSaysSo(t *testing.T) {
	h := newJobsHarness(t)
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Jobs: h.jobs}
	out, err := tool.Execute(context.Background(), `{"command":"true","background":true,"notify_on_complete":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "notify_on_complete: true") || !strings.Contains(out, "do not poll") {
		t.Fatalf("start result:\n%s", out)
	}
}

func TestRunBashTool_NotifyOnCompleteRequiresBackground(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	if _, err := tool.Execute(context.Background(), `{"command":"true","notify_on_complete":true}`); err == nil {
		t.Fatalf("notify_on_complete on a foreground call must be an error")
	}
}

func TestRunBashTool_TimeoutAboveCapIsPromotedToBackground(t *testing.T) {
	h := newJobsHarness(t)
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Jobs: h.jobs}
	over := runBashMaxTimeout.Milliseconds() + 1
	out, err := tool.Execute(context.Background(), fmt.Sprintf(`{"command":"echo promoted","timeout_ms":%d}`, over))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"promoted to a background job", "Do not re-run it", "notify_on_complete: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("promotion result missing %q:\n%s", want, out)
		}
	}
	id := sessionIDFrom(t, out)
	j, _ := h.jobs.lookup(id)
	waitJob(t, j)
	eventually(t, "wake event", func() bool { return len(h.eventsFor(id)) == 1 })
	if !h.eventsFor(id)[0].NotifyOnDone {
		t.Fatalf("a promoted job notifies on completion")
	}
}

func TestRunBashTool_TimeoutAboveCapClampsWhereJobsUnavailable(t *testing.T) {
	prev := runBashMaxTimeout
	runBashMaxTimeout = 300 * time.Millisecond
	t.Cleanup(func() { runBashMaxTimeout = prev })

	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())} // no Jobs: oneshot / dispatch worker
	start := time.Now()
	out, err := tool.Execute(context.Background(), `{"command":"sleep 30","timeout_ms":999999}`)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second || !strings.Contains(out, "exit=124") {
		t.Fatalf("expected a clamped foreground timeout, got %q", out)
	}
}

func TestRunBashTool_BackgroundWhereUnavailableIsRecoverable(t *testing.T) {
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	out, err := tool.Execute(context.Background(), `{"command":"sleep 1","background":true}`)
	if err != nil {
		t.Fatalf("must be a recoverable result, got error %v", err)
	}
	if !strings.Contains(out, "not available") {
		t.Fatalf("result = %q", out)
	}
}

func TestRunBashTool_BackgroundStillPassesHardlineChecks(t *testing.T) {
	h := newJobsHarness(t)
	tool := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Jobs: h.jobs}
	out, err := tool.Execute(context.Background(), `{"command":"sudo true","background":true}`)
	if err != nil || !strings.Contains(out, "BLOCKED") {
		t.Fatalf("background must not bypass the execution-time floor: %q, %v", out, err)
	}
}

func TestRunBashTool_SchemaOffersBackgroundOnlyWhenAvailable(t *testing.T) {
	plain := &RunBashTool{}
	props := plain.Schema()["properties"].(map[string]any)
	if _, ok := props["background"]; ok {
		t.Errorf("background must not be offered without a job host")
	}
	if strings.Contains(plain.Description(), "background") {
		t.Errorf("description must not advertise background without a job host")
	}

	h := newJobsHarness(t)
	rich := &RunBashTool{Jobs: h.jobs}
	props = rich.Schema()["properties"].(map[string]any)
	for _, k := range []string{"background", "notify_on_complete"} {
		if _, ok := props[k]; !ok {
			t.Errorf("schema missing %s", k)
		}
	}
	if req, _ := rich.Schema()["required"].([]string); len(req) != 1 || req[0] != "command" {
		t.Errorf("required = %v", req)
	}
	if !strings.Contains(rich.Description(), "background=true") {
		t.Errorf("description must explain background")
	}
	if got := rich.PreviewCall(`{"command":"make","background":true}`); !strings.HasSuffix(got, "(background)") {
		t.Errorf("preview = %q", got)
	}
}

func TestRunBashTool_TimeoutHintMentionsBackgroundOnlyWhenOffered(t *testing.T) {
	prev := runBashDefaultTimeout
	runBashDefaultTimeout = 200 * time.Millisecond
	t.Cleanup(func() { runBashDefaultTimeout = prev })

	plain := &RunBashTool{Cwd: NewCwdRef(t.TempDir())}
	out, _ := plain.Execute(context.Background(), `{"command":"sleep 30"}`)
	if strings.Contains(out, "background") {
		t.Errorf("no jobs here, hint must not mention background: %q", out)
	}
	h := newJobsHarness(t)
	rich := &RunBashTool{Cwd: NewCwdRef(t.TempDir()), Jobs: h.jobs}
	out, _ = rich.Execute(context.Background(), `{"command":"sleep 30"}`)
	if !strings.Contains(out, "background=true") {
		t.Errorf("hint should offer background: %q", out)
	}
}

// The motivating failure: a command blocked on a FIFO with no writer. In the
// foreground it times out; as a background job the model can kill it.
func TestShellJobs_BlockedFIFOCommandCanBeKilled(t *testing.T) {
	h := newJobsHarness(t)
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	job, err := h.jobs.Start(HostSandbox{}, "cat pipe | head -1", dir, false)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if out, _ := h.jobs.Poll(job.id); !strings.Contains(out, "status: running") {
		t.Fatalf("expected the FIFO read to be blocked:\n%s", out)
	}
	if _, err := h.jobs.Kill(context.Background(), job.id); err != nil {
		t.Fatal(err)
	}
	waitJob(t, job)
}

func TestProcessTool_Validation(t *testing.T) {
	h := newJobsHarness(t)
	proc := &ProcessTool{Jobs: h.jobs}

	if _, err := proc.Execute(context.Background(), `{"action":"explode","session_id":"x"}`); err == nil {
		t.Errorf("unknown action must be an error")
	}
	if _, err := proc.Execute(context.Background(), `{bad json`); err == nil {
		t.Errorf("bad JSON must be an error")
	}
	out, err := proc.Execute(context.Background(), `{"action":"poll","session_id":"deadbeefdeadbeef"}`)
	if err != nil || !strings.Contains(out, "unknown session_id") {
		t.Errorf("unknown id must be a recoverable result: %q, %v", out, err)
	}
	if _, err := (&ProcessTool{}).Execute(context.Background(), `{"action":"poll","session_id":"abcd"}`); err == nil {
		t.Errorf("a tool with no job host must error")
	}
	if proc.RequiresApproval(`{"action":"kill","session_id":"x"}`) {
		t.Errorf("process only manages jobs the session already approved; it must not prompt")
	}
	if got := proc.PreviewCall(`{"action":"wait","session_id":"abc"}`); got != "process: wait abc" {
		t.Errorf("preview = %q", got)
	}
}

func TestProcessTool_WaitDurationClamp(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	cases := []struct {
		in   *int64
		want time.Duration
	}{
		{nil, processWaitDefault},
		{i64(0), processWaitDefault},
		{i64(-1), processWaitDefault},
		{i64(1500), 1500 * time.Millisecond},
		{i64(processWaitMax.Milliseconds() + 1), processWaitMax},
		{i64(1 << 62), processWaitMax},
	}
	for _, c := range cases {
		if got := processWaitDuration(c.in); got != c.want {
			t.Errorf("processWaitDuration(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestProcessTool_ImplementsCleanupToolAndStopsJobs(t *testing.T) {
	h := newJobsHarness(t)
	job := h.start(t, "sleep 60", false)
	reg := NewRegistry()
	reg.Register(&ProcessTool{Jobs: h.jobs})
	if err := CleanupRegistryTools(context.Background(), reg); err != nil {
		t.Fatalf("CleanupRegistryTools: %v", err)
	}
	if pidAlive(job.pid) {
		t.Fatalf("runtime shutdown left a background job running")
	}
}

// A shell job must expose a transcript path so Enter on its dock/picker row
// opens something instead of failing with "stat : no such file".
func TestShellJobs_OutputMirroredToTranscriptFile(t *testing.T) {
	h := newJobsHarness(t)
	h.jobs.agent.TranscriptDir = filepath.Join(t.TempDir(), "transcripts")
	job := h.start(t, "echo hello-log", false)
	waitJob(t, job)

	task, ok := h.tasks.Get(job.id)
	if !ok || task.TranscriptPath == "" {
		t.Fatalf("shell task has no TranscriptPath: %+v", task)
	}
	body, err := os.ReadFile(task.TranscriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	for _, want := range []string{"$ echo hello-log", "hello-log"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("transcript missing %q:\n%s", want, body)
		}
	}
}

// Logs expire with the job record: once a finished job ages out of the table
// its log file is deleted so output does not pile up on disk.
func TestShellJobs_LogRemovedWhenJobExpires(t *testing.T) {
	h := newJobsHarness(t)
	h.jobs.agent.TranscriptDir = t.TempDir()
	job := h.start(t, "echo bye", false)
	waitJob(t, job)
	if _, err := os.Stat(job.logPath); err != nil {
		t.Fatalf("log should exist while the job is tracked: %v", err)
	}

	h.jobs.now = func() time.Time { return time.Now().Add(shellJobFinishedTTL + time.Minute) }
	h.jobs.mu.Lock()
	h.jobs.pruneLocked()
	h.jobs.mu.Unlock()

	if _, err := os.Stat(job.logPath); !os.IsNotExist(err) {
		t.Errorf("log should be removed after expiry, stat err = %v", err)
	}
}
