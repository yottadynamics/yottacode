package browser

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/ysmood/leakless"
)

// rod's "leakless" guard is a small helper binary that kills the browser if
// yottacode dies without cleaning up. The dependency extracts it to a
// PREDICTABLE path — /tmp/leakless-<arch>-<version>/leakless, both parts
// public — and, if a file already exists there, runs it without checking who
// owns it or what it contains. On a shared machine that is a local
// privilege-escalation: another user pre-creates that path with their own
// program, and the next browser launch executes it as the victim.
//
// leaklessUsable closes that. It only lets the guard run if the helper and its
// directory belong to the current user and can't be modified by anyone else;
// otherwise the browser is launched without the guard. The cost of that
// fallback is that a hard-killed yottacode can leave an orphaned browser
// behind, which is far cheaper than running someone else's binary.
func leaklessUsable() bool {
	return leaklessUsableWith(leakless.Support, leakless.GetLeaklessBin, os.Getuid())
}

func leaklessUsableWith(supported func() bool, helperPath func() string, uid int) (ok bool) {
	if !supported() {
		return false // rod would skip the guard on this platform anyway
	}
	// GetLeaklessBin panics on an I/O error (read-only tmp, full disk); rod's
	// own launch would panic the same way, so treat it as "not usable".
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return verifyLeaklessHelper(helperPath(), uid) == nil
}

// verifyLeaklessHelper reports whether bin can be executed without trusting
// anyone but uid. The directory is checked and tightened first, then the
// file, so that once the file has been vetted nobody else can replace it.
func verifyLeaklessHelper(bin string, uid int) error {
	dir := filepath.Dir(bin)
	di, err := os.Lstat(dir) // Lstat: a symlinked directory must not pass
	if err != nil {
		return err
	}
	if !di.IsDir() {
		return fmt.Errorf("%s is not a real directory", dir)
	}
	if owner, ok := ownerOf(di); !ok || owner != uid {
		return fmt.Errorf("%s is not owned by the current user", dir)
	}
	if di.Mode().Perm()&0o077 != 0 {
		// Ours, but open to group/other (rod creates it 0775). Lock it down
		// before looking at the file inside, so nobody can swap it later.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("cannot make %s private: %w", dir, err)
		}
	}
	fi, err := os.Lstat(bin)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return errors.New(bin + " is not a regular file")
	}
	if owner, ok := ownerOf(fi); !ok || owner != uid {
		return fmt.Errorf("%s is not owned by the current user", bin)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by other users", bin)
	}
	return nil
}

// ownerOf is fileOwner behind a variable so tests can simulate a file owned by
// a different user (a test can't chown to one without being root).
var ownerOf = fileOwner

func fileOwner(fi os.FileInfo) (uid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
