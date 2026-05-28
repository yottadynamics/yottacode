package skills

// Content-hash helper for installed-skill drift detection. Used by
// Phase 2's `skills check` / `skills update` to tell whether the user
// edited a skill in place — in which case the next `update` skips
// the entry unless --force is set, so we never silently clobber
// hand-edits.
//
// The hash is computed deterministically over a sorted manifest of
// `relpath\tsha256(file)\n` lines, then sha256'd one more time. File
// modes and timestamps are ignored — only the *content* and the *set
// of paths* matter. The manifest-of-hashes shape means a file move
// also flips the top hash (the path changes), which is what we want
// for "did the user edit this skill" detection.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// HashInstalledDir produces a stable "sha256:HEXLOWER" identifier
// for the on-disk skill at dir. The lockfile is excluded from the
// manifest (the lockfile sits one level up from per-skill dirs, but
// we belt-and-suspenders this so a future colocation never breaks
// the hash) and so are any dot-prefixed staging dirs that Install
// may have crashed mid-flight and left behind.
func HashInstalledDir(dir string) (string, error) {
	type fileHash struct {
		Rel  string
		Hash string
	}
	var files []fileHash
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip hidden dirs at any depth — covers stray staging
			// dirs (.staging-*) that may have been orphaned by an
			// earlier crashed install.
			if p != dir && strings.HasPrefix(filepath.Base(p), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(p)
		if base == LockfileName || strings.HasPrefix(base, ".") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		// Normalize to forward slashes so the hash is stable across
		// platforms. Linux + macOS use `/` already, but the
		// portability-beats-single-OS-depth memory means we never
		// produce diverging hashes for the same content on different
		// hosts.
		rel = filepath.ToSlash(rel)
		h, err := hashFile(p)
		if err != nil {
			return err
		}
		files = append(files, fileHash{Rel: rel, Hash: h})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	top := sha256.New()
	for _, f := range files {
		fmt.Fprintf(top, "%s\t%s\n", f.Rel, f.Hash)
	}
	return "sha256:" + hex.EncodeToString(top.Sum(nil)), nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
