// Package version exposes the build version. Kept small so any package can
// import it without dependency churn.
package version

import (
	"runtime/debug"
	"sync"
)

// Current is the human-readable release label. Bump on every meaningful
// shipped change. Set to a `-dev` suffix between releases.
const Current = "0.1.0"

// shortCommitLen is how many hex chars of the git SHA we surface — long
// enough to be unique within any reasonable repo, short enough to fit
// alongside the version in tight UI (startup card, prompt header).
const shortCommitLen = 7

type buildInfo struct {
	commit     string // short SHA, "" if unknown
	commitTime string // RFC3339, "" if unknown
	dirty      bool
	known      bool // whether ReadBuildInfo returned VCS settings at all
}

var (
	infoOnce sync.Once
	info     buildInfo
)

// readInfo populates the cached buildInfo from runtime/debug. Go's
// toolchain stamps these settings into binaries built with `go build`
// from a git checkout (since 1.18). They're absent for `go run`,
// tarball builds, and `-buildvcs=false` builds — all handled by
// returning empty strings rather than crashing.
func readInfo() buildInfo {
	infoOnce.Do(func() {
		bi, ok := debug.ReadBuildInfo()
		if !ok {
			return
		}
		var sawVCS bool
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs":
				sawVCS = true
			case "vcs.revision":
				if len(s.Value) >= shortCommitLen {
					info.commit = s.Value[:shortCommitLen]
				} else {
					info.commit = s.Value
				}
			case "vcs.time":
				info.commitTime = s.Value
			case "vcs.modified":
				info.dirty = s.Value == "true"
			}
		}
		info.known = sawVCS
	})
	return info
}

// Commit returns the 7-char short SHA the binary was built from, or ""
// if VCS info isn't available (e.g. `go run`, tarball build).
func Commit() string { return readInfo().commit }

// CommitTime returns the RFC3339 timestamp of the build commit, or "".
func CommitTime() string { return readInfo().commitTime }

// Dirty reports whether the working tree had uncommitted changes when
// the binary was built.
func Dirty() bool { return readInfo().dirty }

// Full renders a human-readable version string. Falls back to plain
// Current when no build info is available, so `go run` and tarball
// builds don't print misleading parens.
//
//	"0.1.0"                    — no VCS info available
//	"0.1.0 (e546e47)"          — clean build from a checkout
//	"0.1.0 (e546e47, dirty)"   — built with uncommitted changes
func Full() string {
	bi := readInfo()
	if bi.commit == "" {
		return Current
	}
	if bi.dirty {
		return Current + " (" + bi.commit + ", dirty)"
	}
	return Current + " (" + bi.commit + ")"
}
