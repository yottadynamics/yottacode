package version

import (
	"strings"
	"testing"
)

// TestFull_FallsBackToPlainWhenNoCommit asserts the human-readable
// Full() string degrades cleanly when VCS info is missing — which is
// the runtime state under `go test` (the test binary is built from a
// temp dir, not the source tree, so debug.ReadBuildInfo carries no
// vcs.revision). The function must not invent a fake SHA or render
// stray parens; it should just be Current.
func TestFull_FallsBackToPlainWhenNoCommit(t *testing.T) {
	got := Full()
	if Commit() == "" && got != Current {
		t.Fatalf("Full() with no commit = %q, want %q", got, Current)
	}
}

// TestFull_ContainsCurrent — whatever VCS state the build is in,
// Full() must always carry the semver prefix. Catches a regression
// where someone replaces Current with the SHA only.
func TestFull_ContainsCurrent(t *testing.T) {
	if !strings.HasPrefix(Full(), Current) {
		t.Fatalf("Full() = %q, want prefix %q", Full(), Current)
	}
}

// TestCommit_LengthBound — when a commit *is* present, it must be
// trimmed to the short form. Long SHAs in the startup card make the
// box overflow.
func TestCommit_LengthBound(t *testing.T) {
	c := Commit()
	if c == "" {
		t.Skip("no VCS info in this build")
	}
	if len(c) > shortCommitLen {
		t.Fatalf("Commit() = %q (len=%d), want ≤ %d chars", c, len(c), shortCommitLen)
	}
}
