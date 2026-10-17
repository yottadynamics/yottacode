package codemap

import (
	"os"
	"time"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// racyWindow is how recently a file must have been modified for its parse result
// to be withheld from the cache. A file's size and modification time are the
// cache key, and a write that lands within the filesystem's timestamp granularity
// of the previous one, with the same length, would look unchanged. Timestamps are
// coarse: a few milliseconds on Linux, one or two seconds on HFS+ and FAT. A file
// older than this window cannot be mid-write in that way. Git guards its index the
// same way.
const racyWindow = 2 * time.Second

// ParseCache remembers each file's parse result between builds so a rebuild
// re-reads only files that changed. It is memoization, not a patched index: every
// build still walks the tree and recomputes edges and statistics from scratch, so
// the cache can only skip work, never leave stale structure behind. An entry is
// reused only when the file's size and modification time are identical.
//
// A cache is tied to one symbol source and must not be shared between builds that
// use different sources. It is safe for concurrent use.
type ParseCache struct {
	// racy overrides racyWindow; tests shrink it because they cannot backdate a
	// file's inode change time.
	racy time.Duration

	mu      syncutil.Mutex
	entries map[string]*cacheEntry
	gen     uint64
}

type cacheEntry struct {
	size  int64
	mtime int64
	ctime int64
	res   *fileResult
	gen   uint64 // last build that touched the entry
}

// begin starts a build generation.
func (c *ParseCache) begin() {
	c.mu.Lock()
	c.gen++
	c.mu.Unlock()
}

// end finishes a build. After a complete one, entries no build touched (deleted or
// newly excluded files) are dropped so the cache cannot grow without bound; a
// partial build keeps everything, because it never looked at some files.
func (c *ParseCache) end(complete bool) {
	if !complete {
		return
	}
	c.mu.Lock()
	for rel, e := range c.entries {
		if e.gen != c.gen {
			delete(c.entries, rel)
		}
	}
	c.mu.Unlock()
}

// get returns the cached result for rel if the file is unchanged.
func (c *ParseCache) get(rel string, fi os.FileInfo) *fileResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[rel]
	if e == nil || e.size != fi.Size() || e.mtime != fi.ModTime().UnixNano() || e.ctime != changeTime(fi) {
		return nil
	}
	e.gen = c.gen
	return e.res
}

// put records a result unless the file was modified too recently to trust.
func (c *ParseCache) put(rel string, fi os.FileInfo, res *fileResult) {
	window := c.racy
	if window <= 0 {
		window = racyWindow
	}
	if time.Since(fi.ModTime()) < window {
		return
	}
	if ct := changeTime(fi); ct != 0 && time.Since(time.Unix(0, ct)) < window {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*cacheEntry{}
	}
	c.entries[rel] = &cacheEntry{size: fi.Size(), mtime: fi.ModTime().UnixNano(), ctime: changeTime(fi), res: res, gen: c.gen}
}

// Len reports how many files are cached.
func (c *ParseCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
