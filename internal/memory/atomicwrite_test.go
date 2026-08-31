package memory

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAtomicWrite_RoundtripAndPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	if err := AtomicWrite(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600", info.Mode().Perm())
	}
}

// TestAtomicWrite_ConcurrentNoCorruptionOrTempLeak hammers the same
// destination from many goroutines. The fixed-".tmp" predecessor would
// interleave bytes or have one writer's cleanup delete another's staging
// file; unique temps make this last-writer-wins on a valid file with no
// leftover temp files.
func TestAtomicWrite_ConcurrentNoCorruptionOrTempLeak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.md")
	payloads := []string{strings.Repeat("A", 5000), strings.Repeat("B", 5000), strings.Repeat("C", 5000)}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	// Keep enough concurrent writers to exercise the unique-temp race without
	// exhausting thread quotas in sandboxed test runs.
	for range 6 {
		for i := range payloads {
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				if e := AtomicWrite(path, []byte(p), 0o600); e != nil {
					errs <- e
				}
			}(payloads[i])
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent AtomicWrite error: %v", e)
	}

	// Final content must be exactly one of the payloads — never a
	// byte-interleaved mixture or a truncated write.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	ok := false
	for _, p := range payloads {
		if string(got) == p {
			ok = true
			break
		}
	}
	if !ok {
		t.Errorf("final file is not a clean single write (len=%d) — corruption", len(got))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
