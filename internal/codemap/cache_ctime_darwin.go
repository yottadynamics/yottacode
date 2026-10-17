//go:build darwin

package codemap

import (
	"os"
	"syscall"
)

// changeTime returns the inode change time in nanoseconds, or 0 if unavailable.
// See cache_ctime_linux.go.
func changeTime(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ctimespec.Nano()
	}
	return 0
}
