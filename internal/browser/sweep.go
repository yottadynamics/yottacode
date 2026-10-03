package browser

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Each browser session gets a fresh profile directory under the temp dir
// (cookies, site data, cache) that Close removes. If yottacode is killed with
// SIGKILL, or the machine loses power, Close never runs and the directory —
// with the cookies of every site the agent visited — is left behind. Nothing
// else ever cleans it up, so the first browser launch of the next session does.

// staleProfileAge is how old a profile directory must be before it is even
// considered. It exists so a session that is still starting up (directory made,
// Chrome not yet holding its lock) is never mistaken for a dead one.
const staleProfileAge = time.Hour

// maxSweepRemovals bounds the work done at one launch: each profile can hold
// tens of MB of cache, and a temp dir with hundreds of them shouldn't add
// seconds to starting a browser. The rest go on later launches.
const maxSweepRemovals = 20

// profileDirRE is deliberately strict — the exact shape os.MkdirTemp produces
// for our two prefixes — so a directory a person named "yottacode-browser-notes"
// is never touched.
var profileDirRE = regexp.MustCompile(`^yottacode-browser-(download-)?[0-9]+$`)

// sweepStaleProfiles removes profile directories under root that a dead session
// left behind, and returns what it removed. A directory is removed only if ALL
// of these hold:
//
//   - its name is exactly a yottacode profile/download dir (profileDirRE);
//   - it is a real directory (Lstat — a symlink is never followed) owned by uid,
//     so nobody else's files are ever touched;
//   - it hasn't been modified for at least minAge;
//   - it is not keep (the caller's own live profile);
//   - no live browser holds it. Chrome records its owner in a SingletonLock
//     symlink ("<hostname>-<pid>"): a lock from another host, one we can't parse,
//     or one whose pid still exists means "in use", and the directory stays. A
//     missing lock means no Chrome ever started there (or it exited cleanly).
//
// Everything else — including anything it is unsure about — is left alone.
// Errors are ignored: this is housekeeping and must never fail a launch.
func sweepStaleProfiles(root string, now time.Time, minAge time.Duration, uid int, host string, alive func(pid int) bool, keep string) (removed []string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if len(removed) >= maxSweepRemovals {
			break
		}
		if !profileDirRE.MatchString(e.Name()) || !e.IsDir() {
			continue // wrong name, or not a real directory (DirEntry doesn't follow symlinks)
		}
		path := filepath.Join(root, e.Name())
		if path == keep {
			continue
		}
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() {
			continue
		}
		if owner, ok := ownerOf(fi); !ok || owner != uid {
			continue
		}
		if now.Sub(fi.ModTime()) < minAge {
			continue
		}
		if profileInUse(path, host, alive) {
			continue
		}
		if err := os.RemoveAll(path); err == nil {
			removed = append(removed, path)
		}
	}
	return removed
}

// profileInUse reports whether a live browser may own dir, judged from Chrome's
// SingletonLock. When in doubt it says yes: a leaked directory is cheap, a
// deleted live profile is a broken session.
func profileInUse(dir, host string, alive func(pid int) bool) bool {
	target, err := os.Readlink(filepath.Join(dir, "SingletonLock"))
	if err != nil {
		// No lock at all: nothing is running there. Any other error (it is not a
		// symlink, permissions) is something we don't understand.
		return !errors.Is(err, fs.ErrNotExist)
	}
	i := strings.LastIndex(target, "-")
	if i <= 0 {
		return true
	}
	if lockHost := target[:i]; lockHost != host {
		return true // made on another machine (a shared temp dir): can't check its pid
	}
	pid, err := strconv.Atoi(target[i+1:])
	if err != nil {
		return true
	}
	return alive(pid)
}

// processAlive reports whether pid exists, via a zero-signal probe. EPERM means
// it exists but belongs to someone else, which still counts as alive. A pid we
// can't sensibly probe (<= 1) counts as alive rather than risk a wrong answer.
func processAlive(pid int) bool {
	if pid <= 1 {
		return true
	}
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// sweepOnceLocked runs the stale-profile sweep the first time a session is about
// to launch a browser, and never again for this Manager. Callers must hold m.mu.
// Managers built without a sweeper (every test literal) do nothing.
func (m *Manager) sweepOnceLocked() {
	if m.swept || m.sweepStale == nil {
		return
	}
	m.swept = true
	m.sweepStale(m.profileDir)
}

// defaultSweep sweeps the real temp dir for the real user.
func defaultSweep(keep string) {
	host, err := os.Hostname()
	if err != nil {
		return // can't tell which locks are ours; do nothing
	}
	sweepStaleProfiles(shortTempRoot(), time.Now(), staleProfileAge, os.Getuid(), host, processAlive, keep)
}
