//go:build !linux && !darwin

package codemap

import "os"

// changeTime is unavailable on this platform; the cache keys on size and
// modification time only.
func changeTime(os.FileInfo) int64 { return 0 }
