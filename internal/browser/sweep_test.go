package browser

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const (
	sweepHost    = "testhost"
	sweepLivePID = 4242 // the pid the fake liveness probe reports as running
	sweepDeadPID = 9999
)

func fakeAlive(pid int) bool { return pid == sweepLivePID }

// makeProfile creates root/name with some content, a SingletonLock symlink
// (lock == "" means none), and the given age. The mtime is set LAST: writing
// files into the directory would otherwise reset it.
func makeProfile(t *testing.T, root, name, lock string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "Default", "Cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Default", "Cookies"), []byte("cookie-jar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if lock != "" {
		if err := os.Symlink(lock, filepath.Join(dir, "SingletonLock")); err != nil {
			t.Fatal(err)
		}
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatal(err)
	}
	return dir
}

func lockFor(pid int) string { return fmt.Sprintf("%s-%d", sweepHost, pid) }

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func sweep(root string, uid int, keep string) []string {
	return sweepStaleProfiles(root, time.Now(), time.Hour, uid, sweepHost, fakeAlive, keep)
}

// What a killed session leaves behind is removed, whole.
func TestSweep_RemovesProfileWhoseBrowserIsDead(t *testing.T) {
	root := t.TempDir()
	dead := makeProfile(t, root, "yottacode-browser-111", lockFor(sweepDeadPID), 3*time.Hour)
	noLock := makeProfile(t, root, "yottacode-browser-222", "", 3*time.Hour)
	dl := makeProfile(t, root, "yottacode-browser-download-333", "", 3*time.Hour)

	removed := sweep(root, os.Getuid(), "")
	if len(removed) != 3 {
		t.Errorf("removed %v, want all three stale directories", removed)
	}
	for _, d := range []string{dead, noLock, dl} {
		if exists(d) {
			t.Errorf("%s should have been removed (a dead session's leftovers)", d)
		}
	}
}

// The failure that matters: deleting a session that is still running. Every
// way a profile can look "maybe in use" must keep it.
func TestSweep_KeepsAnythingThatMightBeInUse(t *testing.T) {
	root := t.TempDir()
	cases := map[string]string{
		"live browser":           makeProfile(t, root, "yottacode-browser-1", lockFor(sweepLivePID), 48*time.Hour),
		"lock from another host": makeProfile(t, root, "yottacode-browser-2", fmt.Sprintf("otherhost-%d", sweepDeadPID), 48*time.Hour),
		"unparseable lock":       makeProfile(t, root, "yottacode-browser-3", "garbage", 48*time.Hour),
		"lock with no pid":       makeProfile(t, root, "yottacode-browser-4", sweepHost+"-", 48*time.Hour),
		"lock with a bad pid":    makeProfile(t, root, "yottacode-browser-5", sweepHost+"-abc", 48*time.Hour),
	}
	if removed := sweep(root, os.Getuid(), ""); len(removed) != 0 {
		t.Errorf("removed %v; nothing here is provably dead", removed)
	}
	for why, dir := range cases {
		if !exists(dir) {
			t.Errorf("%s: %s was removed", why, dir)
		}
	}
}

// A session that is still starting up has a directory but maybe no lock yet.
func TestSweep_KeepsRecentProfiles(t *testing.T) {
	root := t.TempDir()
	fresh := makeProfile(t, root, "yottacode-browser-10", lockFor(sweepDeadPID), 5*time.Minute)
	freshNoLock := makeProfile(t, root, "yottacode-browser-11", "", 5*time.Minute)
	if removed := sweep(root, os.Getuid(), ""); len(removed) != 0 {
		t.Errorf("removed %v; both are younger than the minimum age", removed)
	}
	if !exists(fresh) || !exists(freshNoLock) {
		t.Error("a recent profile was removed")
	}
}

// Somebody else's directory (or one we can't prove is ours) is never touched.
func TestSweep_KeepsDirectoriesOwnedByOthers(t *testing.T) {
	root := t.TempDir()
	dir := makeProfile(t, root, "yottacode-browser-20", lockFor(sweepDeadPID), 3*time.Hour)
	// The directory belongs to the test's user; ask the sweep to act as
	// someone else.
	if removed := sweep(root, os.Getuid()+1, ""); len(removed) != 0 {
		t.Errorf("removed %v, but the directory is not owned by the sweeping user", removed)
	}
	if !exists(dir) {
		t.Error("another user's directory was removed")
	}
}

// Only the exact shape os.MkdirTemp makes for our prefixes.
func TestSweep_IgnoresNamesThatAreNotOurs(t *testing.T) {
	root := t.TempDir()
	var kept []string
	for _, name := range []string{
		"yottacode-browser-notes", "yottacode-browser-", "yottacode-browser", "yottacode-browser-12x",
		"yottacode-browser-download-", "not-yottacode-browser-12", "yottacode-browser-12-old", "other-123",
	} {
		kept = append(kept, makeProfile(t, root, name, "", 48*time.Hour))
	}
	if removed := sweep(root, os.Getuid(), ""); len(removed) != 0 {
		t.Errorf("removed %v; none of these are yottacode profile directories", removed)
	}
	for _, d := range kept {
		if !exists(d) {
			t.Errorf("%s was removed", d)
		}
	}
}

// A symlink with a matching name must neither be removed nor followed: what it
// points at is not ours to delete.
func TestSweep_NeverFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	victim := makeProfile(t, t.TempDir(), "precious", "", 48*time.Hour)
	link := filepath.Join(root, "yottacode-browser-30")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	// A regular file with a matching name is not a profile either.
	file := filepath.Join(root, "yottacode-browser-31")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if removed := sweep(root, os.Getuid(), ""); len(removed) != 0 {
		t.Errorf("removed %v; a symlink and a file are not profile directories", removed)
	}
	if !exists(filepath.Join(victim, "Default", "Cookies")) {
		t.Error("the sweep followed a symlink and deleted its target")
	}
	if !exists(link) || !exists(file) {
		t.Error("the symlink or the file was removed")
	}
}

// The caller's own live profile is excluded even if it looks stale.
func TestSweep_KeepsTheCallersOwnProfile(t *testing.T) {
	root := t.TempDir()
	mine := makeProfile(t, root, "yottacode-browser-40", lockFor(sweepDeadPID), 48*time.Hour)
	other := makeProfile(t, root, "yottacode-browser-41", lockFor(sweepDeadPID), 48*time.Hour)
	removed := sweep(root, os.Getuid(), mine)
	if !exists(mine) {
		t.Error("the caller's own profile was removed")
	}
	if exists(other) || len(removed) != 1 {
		t.Errorf("the other stale profile should have been removed; removed=%v", removed)
	}
}

// Launch latency stays bounded; the rest go on later launches.
func TestSweep_IsBoundedPerCall(t *testing.T) {
	root := t.TempDir()
	total := maxSweepRemovals + 10
	for i := 0; i < total; i++ {
		makeProfile(t, root, fmt.Sprintf("yottacode-browser-%d", 1000+i), "", 48*time.Hour)
	}
	if got := len(sweep(root, os.Getuid(), "")); got != maxSweepRemovals {
		t.Errorf("first sweep removed %d, want the cap of %d", got, maxSweepRemovals)
	}
	if got := len(sweep(root, os.Getuid(), "")); got != 10 {
		t.Errorf("second sweep removed %d, want the remaining 10", got)
	}
}

func TestSweep_MissingRootIsHarmless(t *testing.T) {
	if got := sweep(filepath.Join(t.TempDir(), "does-not-exist"), os.Getuid(), ""); len(got) != 0 {
		t.Errorf("removed %v from a directory that doesn't exist", got)
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("this process should be reported alive")
	}
	// A pid we can't sensibly probe counts as alive rather than risking a
	// wrong "dead" (pid 0 would signal a whole process group).
	for _, pid := range []int{0, 1, -1} {
		if !processAlive(pid) {
			t.Errorf("processAlive(%d) = false; unprobeable pids must count as alive", pid)
		}
	}
	// A process that has exited (and been reaped) is dead.
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child process: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if processAlive(pid) {
		t.Errorf("processAlive(%d) = true for a process that has exited", pid)
	}
}

// The Manager sweeps exactly once — before the first launch — and not at all
// when nothing is launched.
func TestManager_SweepsOnceBeforeFirstLaunch(t *testing.T) {
	fake := &fakeSession{}
	m := newTestManager(fake, t.TempDir())
	var calls int
	m.sweepStale = func(string) { calls++ }

	if _, err := m.Navigate(t.Context(), "https://example.com", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if _, err := m.Navigate(t.Context(), "https://example.com/two", ""); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if calls != 1 {
		t.Errorf("swept %d times, want exactly once", calls)
	}
}

func TestManager_NoSweepWithoutALaunch(t *testing.T) {
	m := newTestManager(&fakeSession{}, t.TempDir())
	m.sweepStale = func(string) { t.Error("swept although no browser was launched") }
	// A refused URL launches nothing; neither does status.
	if _, err := m.Navigate(t.Context(), "file:///etc/passwd", ""); err == nil {
		t.Fatal("expected the URL to be refused")
	}
	_ = m.Status()
}

func TestManager_HandoffAsFirstActionSweepsOnce(t *testing.T) {
	h := newHandoffHarness(t, &fakeSession{}, &fakeSession{})
	var calls int
	h.m.sweepStale = func(string) { calls++ }
	if _, err := h.m.Handoff(t.Context()); err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if _, err := h.m.Handoff(t.Context()); err != nil { // already visible: a no-op
		t.Fatalf("second Handoff: %v", err)
	}
	if calls != 1 {
		t.Errorf("swept %d times, want exactly once", calls)
	}
}
