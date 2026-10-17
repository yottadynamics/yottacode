package agent

import (
	"strings"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

// jobBuffer is the rolling output store behind a background shell job. It
// keeps the most recent `limit` bytes; older bytes drop from the front. Offsets
// are absolute (bytes ever written), so a reader's cursor stays meaningful
// after the front has been trimmed and it can tell how much it missed.
//
// The backing slice is allowed to grow to twice `limit` before being trimmed,
// so a steady stream costs an amortized O(1) copy per byte instead of a full
// copy on every write.
type jobBuffer struct {
	mu      syncutil.Mutex
	limit   int
	data    []byte
	dropped int64 // absolute offset of data[0]
}

func newJobBuffer(limit int) *jobBuffer {
	return &jobBuffer{limit: limit}
}

// Write implements io.Writer. It never fails, so a chatty command can never
// block or error out on a full buffer.
func (b *jobBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 2*b.limit {
		cut := len(b.data) - b.limit
		b.data = append(b.data[:0], b.data[cut:]...)
		b.dropped += int64(cut)
	}
	return len(p), nil
}

// total is the absolute number of bytes written so far. Caller holds b.mu.
func (b *jobBuffer) totalLocked() int64 {
	return b.dropped + int64(len(b.data))
}

// retained is the window actually held (at most limit bytes). Caller holds b.mu.
func (b *jobBuffer) retainedLocked() []byte {
	if len(b.data) > b.limit {
		return b.data[len(b.data)-b.limit:]
	}
	return b.data
}

// readFrom returns the output after absolute offset `from`, at most max bytes
// (the newest ones when more is available), the next cursor, and how many bytes
// between `from` and the returned text were unavailable (dropped from the
// buffer or skipped to honor max).
func (b *jobBuffer) readFrom(from int64, max int) (text string, next, lost int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	total := b.totalLocked()
	window := b.retainedLocked()
	start := total - int64(len(window)) // absolute offset of window[0]
	if from < 0 {
		from = 0
	}
	if from < start {
		lost = start - from
		from = start
	}
	if from > total {
		from = total
	}
	avail := total - from
	if max > 0 && avail > int64(max) {
		skip := avail - int64(max)
		lost += skip
		from += skip
	}
	out := window[from-start:]
	return strings.ToValidUTF8(string(out), ""), total, lost
}

// tailString returns roughly the last n bytes of retained output, trimmed of a
// trailing newline, and whether anything earlier was left out.
func (b *jobBuffer) tailString(n int) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	window := b.retainedLocked()
	cut := b.dropped > 0 || len(b.data) > b.limit
	if n > 0 && len(window) > n {
		window = window[len(window)-n:]
		cut = true
	}
	return strings.TrimRight(strings.ToValidUTF8(string(window), ""), "\n"), cut
}

// tailLines returns the last `lines` lines of retained output, capped at
// maxBytes, and whether earlier output was omitted.
func (b *jobBuffer) tailLines(lines, maxBytes int) (string, bool) {
	text, cut := b.tailString(maxBytes)
	if lines <= 0 {
		return text + "\n", cut
	}
	all := strings.Split(text, "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
		cut = true
	}
	out := strings.Join(all, "\n")
	if out == "" {
		return "(none)\n", cut
	}
	return out + "\n", cut
}
