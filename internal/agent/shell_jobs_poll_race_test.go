//go:build unix

package agent

import (
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/yottadynamics/yottacode/internal/syncutil"
)

var polledLine = regexp.MustCompile(`(?m)^L(\d+)$`)

// Several callers polling one job at the same time must between them see every
// line of output exactly once: no duplicates (two polls reading from the same
// cursor) and no gaps (a poll marking the outcome seen without its last bytes).
func TestShellJobs_ConcurrentPollersSeeEachLineExactlyOnce(t *testing.T) {
	const lines = 600
	h := newJobsHarness(t)
	// "L$i" is spelled so the marker never appears in the command echoed back
	// in the status header, only in the output.
	job := h.start(t, "i=0; while [ $i -lt 600 ]; do echo L$i; i=$((i+1)); done", false)

	var (
		mu   syncutil.Mutex
		seen = map[int]int{}
		wg   sync.WaitGroup
	)
	collect := func(out string) {
		mu.Lock()
		defer mu.Unlock()
		for _, m := range polledLine.FindAllStringSubmatch(out, -1) {
			n, _ := strconv.Atoi(m[1])
			seen[n]++
		}
	}
	for range 4 {
		wg.Go(func() {
			for {
				out, err := h.jobs.Poll(job.id)
				if err != nil {
					t.Errorf("Poll: %v", err)
					return
				}
				collect(out)
				select {
				case <-job.done:
					return
				default:
				}
			}
		})
	}
	wg.Wait()
	waitJob(t, job)
	// Drain anything produced after the last concurrent poll.
	out, _ := h.jobs.Poll(job.id)
	collect(out)

	for i := range lines {
		if seen[i] != 1 {
			t.Fatalf("line L%d was returned %d times across all polls, want exactly 1", i, seen[i])
		}
	}
	if len(seen) != lines {
		t.Fatalf("saw %d distinct lines, want %d", len(seen), lines)
	}
}
