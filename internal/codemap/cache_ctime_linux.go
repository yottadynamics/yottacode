//go:build linux

package codemap

import (
	"os"
	"syscall"
)

// changeTime returns the inode change time in nanoseconds, or 0 if unavailable.
// Unlike the modification time, it cannot be set by the user (touch -r, tar, rsync -t),
// so a file rewritten with its old mtime and the same size still invalidates the cache.
func changeTime(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ctim.Nano()
	}
	return 0
}
