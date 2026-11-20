package filerefs

import (
	"bytes"
	"errors"
	"image"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yottadynamics/yottacode/internal/agent"
)

// DefaultLoadTimeout bounds one whole Prepare/LoadTimeout call.
const DefaultLoadTimeout = 5 * time.Second

// LoadOptions tunes Load.
type LoadOptions struct {
	// AllowOutside permits absolute, "~/" and "../" paths that resolve
	// outside the working directory. Interactive callers (the TUI, where
	// the user typed the path) enable it; scripted entry points
	// (`yottacode run`, ACP) leave it off so an untrusted prompt cannot
	// pull arbitrary local files into the model context.
	AllowOutside bool
	// OutsideAllowed, when set together with AllowOutside, is asked per
	// reference whether THAT path may leave the working directory. The TUI
	// uses it to keep references that only appear inside pasted text (which
	// the user did not type) confined to the project.
	OutsideAllowed func(path string) bool
}

// expandHome resolves a leading "~" or "~/" to the user's home
// directory. Other "~user" forms are left untouched (and will simply
// fail to resolve).
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// Blocked reports why path may not be attached (credential locations,
// symlinks into them), or nil. It is the same deny-list read_file uses.
func Blocked(path, cwd string) error {
	return agent.ValidateReadPath(path, agent.DefaultDenyReadPaths(cwd))
}

// canonicalPath resolves existing paths so containment and deny-list checks
// compare macOS symlinked paths in the same filesystem frame.
func canonicalPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// Load reads each ref confined to cwd: relative paths only, no escaping
// the working directory. See LoadWith for the interactive variant.
func Load(refs []Ref, cwd string) []Ref {
	return LoadWith(refs, cwd, LoadOptions{})
}

// LoadTimeout is Load with a wall-clock bound. A hung network mount or
// FIFO-like stall must not freeze the caller (usually the UI goroutine),
// so the work runs on a copy; on timeout every ref is reported as timed
// out and the straggler goroutine's result is discarded.
func LoadTimeout(refs []Ref, cwd string, opts LoadOptions, d time.Duration) []Ref {
	if len(refs) == 0 {
		return refs
	}
	if d <= 0 {
		d = DefaultLoadTimeout
	}
	cp := append([]Ref(nil), refs...)
	done := make(chan []Ref, 1)
	go func() { done <- LoadWith(cp, cwd, opts) }()
	select {
	case out := <-done:
		return out
	case <-time.After(d):
		out := append([]Ref(nil), refs...)
		for i := range out {
			out[i].Loaded = false
			out[i].Error = fmt.Sprintf("timed out after %s reading files (slow or unresponsive filesystem?)", d)
		}
		return out
	}
}

// LoadWith reads each ref's file and fills Content / Error in place.
// Refs are returned in the same order.
//
// Relative paths resolve against cwd. With opts.AllowOutside, absolute
// and "~/" paths are accepted too (the user typed them explicitly, so a
// screenshot in ~/Pictures can be attached) and Ref.Outside records it.
// Every path is checked against the agent's credential deny-list
// (symlinks resolved); inside cwd symlinks are never followed.
//
// Image files (png/jpg/gif/webp) are loaded into Data rather than
// Content so the caller can attach them as real image blocks, subject
// to per-image, per-turn-count and per-turn-total caps. Other binary
// files are rejected instead of being dumped into the prompt.
//
// Size enforcement: a text file larger than MaxFileSize is read up to
// the cap and marked with a "...truncated" note in Content. Once the
// running total exceeds MaxTotalSize, every remaining ref is rejected
// with a budget-exhausted Error so the prompt stays bounded.
func LoadWith(refs []Ref, cwd string, opts LoadOptions) []Ref {
	if len(refs) == 0 {
		return refs
	}
	rootAbs, err := filepath.Abs(cwd)
	if err != nil {
		for i := range refs {
			refs[i].Error = "cwd resolution failed: " + err.Error()
		}
		return refs
	}
	if rootEval, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = rootEval
	}

	totalSize, imgCount, imgTotal := 0, 0, 0
	for i := range refs {
		ref := &refs[i]
		if IsSensitivePath(ref.Path) {
			ref.Error = sensitiveMsg
			continue
		}
		resolve := func(raw string) string {
			p := expandHome(raw)
			if !filepath.IsAbs(p) {
				p = filepath.Join(rootAbs, p)
			}
			// Clean (not EvalSymlinks) first so we can see a symlink at
			// the final component with Lstat below.
			return filepath.Clean(p)
		}
		cleaned := resolve(ref.Path)
		info, err := os.Lstat(cleaned)
		// "explain @main.go." — sentence punctuation glued to the path.
		// Only when the dotted name does not exist, retry without the
		// trailing dots (never changes what an existing path means).
		if err != nil && os.IsNotExist(err) && strings.HasSuffix(ref.Path, ".") {
			if trimmed := strings.TrimRight(ref.Path, "."); trimmed != "" && !IsSensitivePath(trimmed) {
				alt := resolve(trimmed)
				if altInfo, altErr := os.Lstat(alt); altErr == nil {
					cleaned, info, err = alt, altInfo, nil
				}
			}
		}
		// Resolve the candidate and workspace root into the same filesystem
		// frame. macOS commonly exposes TempDir paths through /var while
		// EvalSymlinks returns /private/var; lexical comparison would reject
		// an absolute path that is actually inside cwd.
		rootFrame := canonicalPath(rootAbs)
		candidateFrame := canonicalPath(cleaned)
		inside := pathContains(rootFrame, candidateFrame)
		if !inside {
			switch {
			case !opts.AllowOutside:
				ref.Error = "path is outside the working directory (references outside the project are only available in the interactive TUI)"
			case opts.OutsideAllowed != nil && !opts.OutsideAllowed(ref.Path):
				ref.Error = "path is outside the working directory (a reference inside pasted text is limited to the project; type it yourself to attach files outside)"
			}
			if ref.Error != "" {
				continue
			}
		}
		if err != nil {
			if os.IsNotExist(err) {
				ref.Error = "no such file or directory"
			} else {
				ref.Error = err.Error()
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Check the symlink path before following it as well as the
			// resolved target below. The read-path validator resolves
			// symlinks, and this first check keeps credential targets blocked
			// even when macOS presents HOME through a /var -> /private/var
			// alias.
			if blockErr := Blocked(cleaned, rootAbs); blockErr != nil {
				ref.Error = "blocked: " + blockErr.Error()
				continue
			}
			if inside {
				ref.Error = "symlinks are not followed for safety"
				continue
			}
			resolved, err := filepath.EvalSymlinks(cleaned)
			if err != nil {
				ref.Error = "could not resolve symlink: " + err.Error()
				continue
			}
			if IsSensitivePath(resolved) {
				ref.Error = sensitiveMsg
				continue
			}
			cleaned = resolved
			if info, err = os.Lstat(cleaned); err != nil {
				ref.Error = err.Error()
				continue
			}
		}
		if err := Blocked(cleaned, rootAbs); err != nil {
			ref.Error = "blocked: " + err.Error()
			continue
		}
		ref.Outside = !pathContains(rootAbs, cleaned)

		if info.IsDir() {
			ref.IsDir = true
			entries, err := os.ReadDir(cleaned)
			if err != nil {
				ref.Error = "could not list directory: " + err.Error()
				continue
			}
			ref.Content = renderDirListing(entries)
			ref.Size = len(ref.Content)
			ref.Loaded = true
			totalSize += ref.Size
			if totalSize > MaxTotalSize {
				loadedSize := ref.Size
				ref.Loaded = false
				ref.Content = ""
				ref.Size = 0
				ref.Error = fmt.Sprintf("total reference budget (%d bytes) exhausted before this entry", MaxTotalSize)
				totalSize -= loadedSize
			}
			continue
		}
		if !info.Mode().IsRegular() {
			ref.Error = "not a regular file"
			continue
		}

		if mt, ok := imageMediaTypes[strings.ToLower(filepath.Ext(cleaned))]; ok {
			switch {
			case info.Size() > MaxImageSourceSize:
				ref.Error = fmt.Sprintf("image is %s; the limit is %d MiB (resize or compress it)", humanBytes(int(info.Size())), MaxImageSourceSize>>20)
			case imgCount >= MaxImagesPerTurn:
				ref.Error = fmt.Sprintf("at most %d images can be attached per message", MaxImagesPerTurn)
			}
			if ref.Error != "" {
				continue
			}
			data, _, err := readBounded(cleaned, MaxImageSourceSize)
			if err != nil {
				ref.Error = err.Error()
				continue
			}
			// Validate the payload before attaching it. Extension-only
			// classification would let a corrupt or mislabeled image reach
			// the provider and fail the entire request.
			if _, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil {
				ref.Error = fmt.Sprintf("could not decode image: %v", err)
				continue
			}
			// Oversized or over-dimension images are shrunk to fit rather
			// than refused: a provider-rejected image would fail the whole
			// request.
			if shrink, _ := needsShrink(data); shrink {
				small, smallType, note, err := shrinkImage(data)
				if err != nil {
					ref.Error = err.Error()
					continue
				}
				data, mt, ref.Note = small, smallType, note
			}
			if imgTotal+len(data) > MaxTotalImageSize {
				ref.Error = fmt.Sprintf("images exceed the %d MiB per-message total", MaxTotalImageSize>>20)
				ref.Note = ""
				continue
			}
			ref.IsImage = true
			ref.MediaType = mt
			ref.Data = data
			ref.Size = len(data)
			ref.Loaded = true
			imgCount++
			imgTotal += len(data)
			continue
		}

		// Read up to MaxFileSize+1 so we can detect overflow without
		// loading multi-megabyte files into memory.
		data, truncated, err := readBounded(cleaned, MaxFileSize)
		if err != nil {
			ref.Error = err.Error()
			continue
		}
		sniff := data
		if len(sniff) > 8192 {
			sniff = sniff[:8192]
		}
		if bytes.IndexByte(sniff, 0) >= 0 {
			ref.Error = "binary file; only text files and images (png, jpg, gif, webp) can be attached"
			continue
		}
		if totalSize+len(data) > MaxTotalSize {
			ref.Error = fmt.Sprintf("total reference budget (%d bytes) would be exceeded by this entry (%d bytes)", MaxTotalSize, len(data))
			continue
		}
		ref.Content = string(data)
		if truncated {
			ref.Content += fmt.Sprintf("\n\n...[truncated; file exceeds %d bytes]", MaxFileSize)
		}
		ref.Size = len(ref.Content)
		ref.Loaded = true
		totalSize += ref.Size
	}
	return refs
}

const sensitiveMsg = "sensitive files are not attached automatically; copy the relevant content into a safe project file"

// readBounded reads up to limit+1 bytes from path; if it actually got
// limit+1 bytes the file is over the cap and we report truncated=true
// alongside the limit-sized prefix. The open refuses a symlink at the
// final component (closing the window between the caller's Lstat and
// this open) and never blocks on a FIFO.
func readBounded(path string, limit int) ([]byte, bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, false, errors.New("symlinks are not followed for safety")
		}
		return nil, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !st.Mode().IsRegular() {
		return nil, false, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}

// Images returns the decoded image attachments among loaded refs, in
// order. Callers attach them to the user message.
func Images(refs []Ref) []Image {
	var out []Image
	for _, r := range refs {
		if r.Loaded && r.IsImage {
			out = append(out, Image{Data: r.Data, MediaType: r.MediaType, Name: filepath.Base(r.Path)})
		}
	}
	return out
}

// RejectImages marks loaded image refs as failed with reason and drops
// their bytes. Used by entry points whose model or transport cannot
// carry images, so the user sees an honest error instead of a silent
// no-op.
func RejectImages(refs []Ref, reason string) []Ref {
	for i := range refs {
		if refs[i].IsImage && refs[i].Loaded {
			refs[i].Loaded = false
			refs[i].Error = reason
			refs[i].Data = nil
			refs[i].Size = 0
		}
	}
	return refs
}
