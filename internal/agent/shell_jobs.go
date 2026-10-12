package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/subagents"
	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// ShellTaskType is the task-registry / dock label of a background shell job.
const ShellTaskType = "shell"

const (
	// shellJobBufferBytes caps the rolling output kept per job. Output past the
	// cap drops from the front, so poll/log always see the most recent bytes.
	shellJobBufferBytes = 256 * 1024
	// shellJobFinishedTTL is how long a finished job stays pollable.
	shellJobFinishedTTL = 30 * time.Minute
	// shellDoneTailChars is the output tail carried by the completion notice.
	shellDoneTailChars = 2000
	// shellPollMaxBytes bounds the output a single poll/wait/log returns.
	shellPollMaxBytes = 32 * 1024
	// shellKillWait bounds how long Kill and Cleanup wait for a signaled job to
	// be reaped: the sandbox kill budget (5s) plus its WaitDelay slack.
	shellKillWait = 7 * time.Second
	// shellIDMinPrefix is the shortest unambiguous id prefix a lookup accepts.
	shellIDMinPrefix = 4
)

// shellJobMaxTracked bounds running + finished jobs held at once. At the cap the
// oldest finished job is evicted; if every job is still running a new start is
// refused. A var so tests can exercise the cap without 64 processes.
var shellJobMaxTracked = 64

// ErrShellJobsUnavailable means this session cannot host detached shell jobs
// (oneshot has no TUI or task registry to deliver completions through).
var ErrShellJobsUnavailable = errors.New("background shell jobs are not available in this session")

// ShellJobs owns the session's background shell jobs. A job is a normal
// sandboxed command whose lifetime is the session, not the turn: Esc ends the
// turn but must not kill it. Each job is also registered as a non-agent task in
// the subagent registry, which gives it the dock row, /subagents stop and the
// completion banner/wake for free.
//
// Concurrency: mu guards the jobs map and closed; each job's own mu guards its
// terminal state and bookkeeping; the output buffer has its own lock. The lock
// order is ShellJobs.mu -> shellJob.mu, never the reverse.
type ShellJobs struct {
	agent *AgentTool

	mu     syncutil.Mutex
	jobs   map[string]*shellJob
	closed bool
	now    func() time.Time
}

// NewShellJobs builds the manager on top of an AgentTool, borrowing its task
// registry, background gate and completion callback.
func NewShellJobs(a *AgentTool) *ShellJobs {
	return &ShellJobs{agent: a, jobs: map[string]*shellJob{}, now: time.Now}
}

// Available reports whether this session can host detached jobs at all.
func (m *ShellJobs) Available() bool {
	return m != nil && m.agent != nil && m.agent.AllowBackground && m.agent.Tasks != nil
}

type shellJob struct {
	id      string
	command string
	pid     int
	started time.Time
	notify  bool
	// notifyDropped records that a completion notice was requested but this
	// session has no way to deliver one.
	notifyDropped bool
	cancel        context.CancelFunc
	buf           *jobBuffer
	done          chan struct{}

	mu       syncutil.Mutex
	finished bool
	exit     int
	status   subagents.TaskStatus
	ended    time.Time
	killed   bool // stopped by the process tool
	consumed bool // the model has already seen the outcome (poll/wait/kill)
	waiting  int  // callers currently blocked in wait
	cursor   int64
}

// Start launches command as a background job. sb is the sandbox run_bash is
// configured with, so a job is isolated exactly like a foreground command. The
// job runs on a session-scoped context, never the caller's.
func (m *ShellJobs) Start(sb Sandbox, command, cwd string, notify bool) (*shellJob, error) {
	if !m.Available() {
		return nil, ErrShellJobsUnavailable
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("background shell jobs are shutting down")
	}
	m.pruneLocked()
	if len(m.jobs) >= shellJobMaxTracked {
		m.mu.Unlock()
		return nil, fmt.Errorf("too many background shell jobs (%d); kill one with process(action=\"kill\") first", shellJobMaxTracked)
	}

	// A completion notice needs a delivery path. Where there is none (ACP) the
	// request is downgraded rather than silently never arriving, and the start
	// result says so, so the model knows to poll.
	deliverable := m.agent.canDeliverBackgroundDone()
	id := subagents.NewTaskID()
	job := &shellJob{
		id:            id,
		command:       command,
		started:       m.now(),
		notify:        notify && deliverable,
		notifyDropped: notify && !deliverable,
		buf:           newJobBuffer(shellJobBufferBytes),
		done:          make(chan struct{}),
	}
	task := &subagents.Task{
		ID:           id,
		AgentType:    ShellTaskType,
		Prompt:       command,
		Started:      job.started,
		Status:       subagents.TaskRunning,
		Background:   true,
		NotifyOnDone: job.notify,
		NonAgent:     true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	job.cancel = cancel

	cmd := sb.Command(ctx, command, cwd)
	// One writer for both streams: exec gives an identical Stdout/Stderr a
	// single pipe, so the buffer keeps the real interleaving.
	cmd.Stdout = job.buf
	cmd.Stderr = job.buf
	if err := cmd.Start(); err != nil {
		cancel()
		m.mu.Unlock()
		return nil, fmt.Errorf("failed to start: %w", err)
	}
	if cmd.Process != nil {
		job.pid = cmd.Process.Pid
	}
	m.jobs[id] = job
	m.mu.Unlock()

	m.agent.Tasks.Add(task)
	m.agent.Tasks.AttachCancel(id, cancel)

	go m.watch(job, cmd, ctx)
	return job, nil
}

// watch reaps the job and publishes its terminal state exactly once.
func (m *ShellJobs) watch(job *shellJob, cmd *exec.Cmd, ctx context.Context) {
	defer job.cancel()
	var waitErr error
	// A bare detached goroutine has no parent frame: a panic would take down the
	// session, so degrade it to an errored job instead.
	defer func() {
		if r := recover(); r != nil {
			m.finish(job, -1, subagents.TaskErrored, "internal error: "+panicToError("shell job", r).Error())
		}
	}()
	waitErr = cmd.Wait()

	exit := -1
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	job.mu.Lock()
	killed := job.killed
	job.mu.Unlock()

	var status subagents.TaskStatus
	switch {
	case killed || ctx.Err() != nil:
		status = subagents.TaskCanceled
	case exit == 0:
		status = subagents.TaskCompleted
	default:
		status = subagents.TaskErrored
	}
	note := ""
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) && !errors.Is(waitErr, exec.ErrWaitDelay) {
		note = "wait error: " + waitErr.Error()
	}
	m.finish(job, exit, status, note)
}

// finish records the terminal state once and announces it. The completion
// notice wakes the model only when it asked for one and has not already
// collected the outcome itself.
func (m *ShellJobs) finish(job *shellJob, exit int, status subagents.TaskStatus, note string) {
	job.mu.Lock()
	if job.finished {
		job.mu.Unlock()
		return
	}
	job.finished = true
	job.exit = exit
	job.status = status
	job.ended = m.now()
	wake := job.notify && !job.consumed && job.waiting == 0
	if job.waiting > 0 {
		job.consumed = true
	}
	job.mu.Unlock()

	result := formatShellDone(job, exit, status, note)
	close(job.done)
	errored := status == subagents.TaskErrored
	m.agent.Tasks.MarkDone(job.id, status, result, errored, 0)
	// Only wake-relevant completions enter the model-facing callback. Silent
	// jobs remain durably available through process(poll|wait|log), so they
	// cannot consume notification capacity or crowd out requested wakes.
	if job.notify {
		m.agent.fireBackgroundDone(SubagentBackgroundDone{
			TaskID:       job.id,
			AgentType:    ShellTaskType,
			Result:       result,
			Errored:      errored,
			Duration:     job.ended.Sub(job.started),
			NotifyOnDone: wake,
		})
	}
}

func formatShellDone(job *shellJob, exit int, status subagents.TaskStatus, note string) string {
	var b strings.Builder
	switch status {
	case subagents.TaskCanceled:
		b.WriteString("stopped")
	default:
		fmt.Fprintf(&b, "exit=%d", exit)
	}
	fmt.Fprintf(&b, " · $ %s", oneLine(job.command))
	if note != "" {
		fmt.Fprintf(&b, "\n%s", note)
	}
	if tail, cut := job.buf.tailString(shellDoneTailChars); tail != "" {
		b.WriteString("\n--- output")
		if cut {
			b.WriteString(" (tail)")
		}
		b.WriteString(" ---\n")
		b.WriteString(tail)
	}
	return b.String()
}

// lookup resolves an id or unambiguous prefix to a tracked job.
func (m *ShellJobs) lookup(id string) (*shellJob, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("session_id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	if j, ok := m.jobs[id]; ok {
		return j, nil
	}
	if len(id) >= shellIDMinPrefix {
		var match *shellJob
		for k, j := range m.jobs {
			if strings.HasPrefix(k, id) {
				if match != nil {
					return nil, fmt.Errorf("session_id %q is ambiguous; use more characters", id)
				}
				match = j
			}
		}
		if match != nil {
			return match, nil
		}
	}
	return nil, fmt.Errorf("unknown session_id %q (jobs end with the session and finished ones expire after %s)", id, shellJobFinishedTTL)
}

// pruneLocked drops finished jobs past their TTL, then evicts the oldest
// finished jobs while the table is full. Caller holds m.mu.
func (m *ShellJobs) pruneLocked() {
	now := m.now()
	type fin struct {
		id string
		at time.Time
	}
	var finished []fin
	for id, j := range m.jobs {
		j.mu.Lock()
		done, at := j.finished, j.ended
		j.mu.Unlock()
		if !done {
			continue
		}
		if now.Sub(at) > shellJobFinishedTTL {
			delete(m.jobs, id)
			continue
		}
		finished = append(finished, fin{id, at})
	}
	if len(m.jobs) < shellJobMaxTracked {
		return
	}
	sort.Slice(finished, func(i, k int) bool { return finished[i].at.Before(finished[k].at) })
	for _, f := range finished {
		if len(m.jobs) < shellJobMaxTracked {
			return
		}
		delete(m.jobs, f.id)
	}
}

// snapshot is a consistent view of a job for formatting.
type shellSnapshot struct {
	id       string
	command  string
	pid      int
	runtime  time.Duration
	finished bool
	exit     int
	status   subagents.TaskStatus
	notify   bool
}

func (m *ShellJobs) snapshot(j *shellJob) shellSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return m.snapshotLocked(j)
}

// snapshotLocked is snapshot for a caller already holding j.mu.
func (m *ShellJobs) snapshotLocked(j *shellJob) shellSnapshot {
	end := m.now()
	if j.finished {
		end = j.ended
	}
	return shellSnapshot{
		id: j.id, command: j.command, pid: j.pid, runtime: end.Sub(j.started),
		finished: j.finished, exit: j.exit, status: j.status, notify: j.notify,
	}
}

func (s shellSnapshot) statusLine() string {
	switch {
	case !s.finished:
		return "running"
	case s.status == subagents.TaskCanceled:
		return "stopped"
	default:
		return fmt.Sprintf("exited (exit=%d)", s.exit)
	}
}

// Poll reports a job's state plus any output produced since the last poll or
// wait. It never blocks and marks a finished job's outcome as seen.
func (m *ShellJobs) Poll(id string) (string, error) {
	j, err := m.lookup(id)
	if err != nil {
		return "", err
	}
	return m.report(j), nil
}

// Wait blocks up to d for the job to finish (or ctx to end), then reports it
// like Poll. A caller blocked here receives the completion itself, so the
// background notice does not fire a second time.
func (m *ShellJobs) Wait(ctx context.Context, id string, d time.Duration) (string, error) {
	j, err := m.lookup(id)
	if err != nil {
		return "", err
	}
	j.mu.Lock()
	registered := !j.finished
	if registered {
		j.waiting++
	}
	j.mu.Unlock()
	if registered {
		timer := time.NewTimer(d)
		select {
		case <-j.done:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
		j.mu.Lock()
		j.waiting--
		j.mu.Unlock()
	}
	return m.report(j), nil
}

// Log returns the last n lines of a job's retained output without moving the
// poll cursor.
func (m *ShellJobs) Log(id string, lines int) (string, error) {
	j, err := m.lookup(id)
	if err != nil {
		return "", err
	}
	snap := m.snapshot(j)
	text, cut := j.buf.tailLines(lines, shellPollMaxBytes)
	var b strings.Builder
	writeShellHeader(&b, snap)
	b.WriteString("--- last output")
	if cut {
		b.WriteString(" (earlier output omitted)")
	}
	b.WriteString(" ---\n")
	b.WriteString(text)
	return b.String(), nil
}

// Kill stops a running job and reports its final state. The sandbox cancel path
// does the signalling (process-group SIGKILL on the host, marker kill inside a
// container); this waits for the reap.
func (m *ShellJobs) Kill(ctx context.Context, id string) (string, error) {
	j, err := m.lookup(id)
	if err != nil {
		return "", err
	}
	j.mu.Lock()
	if !j.finished {
		j.killed = true
		j.consumed = true // the caller is the one stopping it; no wake needed
	}
	j.mu.Unlock()
	m.agent.Tasks.Cancel(j.id)
	j.cancel()

	timer := time.NewTimer(shellKillWait)
	defer timer.Stop()
	select {
	case <-j.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	snap := m.snapshot(j)
	var b strings.Builder
	writeShellHeader(&b, snap)
	if !snap.finished {
		b.WriteString("note: stop signal sent; the process has not exited yet, poll again shortly\n")
	}
	return b.String(), nil
}

// Cleanup stops every running job and waits (bounded) for them to be reaped, so
// quitting yottacode cannot orphan a dev server. It also refuses new starts.
// Implements the shutdown half of CleanupTool for ProcessTool.
func (m *ShellJobs) Cleanup(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.closed = true
	jobs := make([]*shellJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		jobs = append(jobs, j)
	}
	m.mu.Unlock()

	var running []*shellJob
	for _, j := range jobs {
		j.mu.Lock()
		if !j.finished {
			j.killed = true
			j.consumed = true
			running = append(running, j)
		}
		j.mu.Unlock()
	}
	for _, j := range running {
		j.cancel()
	}
	deadline := time.NewTimer(shellKillWait)
	defer deadline.Stop()
	for _, j := range running {
		select {
		case <-j.done:
		case <-deadline.C:
			return fmt.Errorf("shell jobs: %d still exiting at shutdown", len(running))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func writeShellHeader(b *strings.Builder, s shellSnapshot) {
	fmt.Fprintf(b, "session_id: %s\n", s.id)
	fmt.Fprintf(b, "status: %s\n", s.statusLine())
	if s.pid > 0 {
		fmt.Fprintf(b, "pid: %d\n", s.pid)
	}
	fmt.Fprintf(b, "runtime: %s\n", s.runtime.Round(time.Second))
	fmt.Fprintf(b, "command: %s\n", oneLine(s.command))
}

// report formats status plus new output since the cursor, advancing the cursor
// and (for a finished job) marking the outcome as seen.
//
// Status, the output read, the cursor advance and the "seen" mark all happen in
// one critical section on j.mu. finish also takes j.mu, so a job cannot flip to
// finished between the read and the mark (which would claim the outcome was
// delivered without its last bytes), and two concurrent polls cannot both read
// from the same cursor and return the same output twice. The buffer's own lock
// nests inside j.mu, never the other way round.
func (m *ShellJobs) report(j *shellJob) string {
	j.mu.Lock()
	snap := m.snapshotLocked(j)
	data, next, lost := j.buf.readFrom(j.cursor, shellPollMaxBytes)
	j.cursor = next
	if j.finished {
		j.consumed = true
	}
	j.mu.Unlock()

	var b strings.Builder
	writeShellHeader(&b, snap)
	b.WriteString("--- new output")
	if lost > 0 {
		fmt.Fprintf(&b, " (%d earlier bytes were dropped)", lost)
	}
	b.WriteString(" ---\n")
	b.WriteString(data)
	if data == "" {
		b.WriteString("(none)\n")
	}
	if !snap.finished {
		b.WriteString("\nstill running: process(action=\"wait\") to block until it exits, \"kill\" to stop it.\n")
	}
	return b.String()
}
