// Package filerefs implements the @file reference system: when a user
// types something like "explain @main.go and @internal/foo.go", the
// agent detects the @-prefixed tokens, reads the matching files, and
// attaches their contents to that user message so the model sees them
// as context for the turn — no extra read_file tool call required.
//
// The package is split into small phases:
//
//   - Parse extracts @<path> tokens from a user message (plain, quoted,
//     backslash-escaped, or file:// forms).
//   - Load reads each token's file (relative to a cwd; absolute, "~/"
//     and "../" only when the caller opts in), with size caps, the
//     credential deny-list, no symlink following inside the cwd, and
//     image/binary detection.
//   - Prepare ties these together for an entry point: it loads, rewrites
//     the message text, and appends the attachment block.
//
// Attachments live in the user message (never the system prompt): file
// contents are user-supplied data and must not gain system authority,
// and editing the system prompt per turn would defeat provider prompt
// caching.
//
// All file-load failures are recorded on the Ref itself (Error field)
// rather than returned globally — the caller renders them as warnings
// and still sends the turn. Hard-failing on a missing @file would be
// frustrating in practice (typos, deleted files, paths inside compound
// words like "@TODO").
package filerefs

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Marker is the heading that identifies the attachment block appended
// to a user message (and, in sessions saved by older versions, the
// block that used to be injected into the system prompt).
const Marker = "## Referenced files (auto-injected from @-prefixed paths)"

// Size caps. Per-file is enforced first; total accumulates as files
// load and short-circuits the rest once breached. Both are deliberate
// (small) so a message never balloons.
const (
	MaxFileSize       = 200 * 1024  // 200 KiB per file
	MaxTotalSize      = 1024 * 1024 // 1 MiB total per turn
	MaxDirEntries     = 2000        // bounded directory listings
	MaxDirListingSize = 64 * 1024   // bounded rendered directory listings
)

// Ref describes a single @-prefixed reference detected in user input.
// After Load, exactly one of Loaded or Error is meaningful: Loaded=true
// + Content populated, or Loaded=false + Error explaining why.
type Ref struct {
	Token   string // the original "@path" as it appeared in input
	Path    string // the parsed path (without the leading @)
	Loaded  bool
	Content string
	Size    int    // bytes read (post-truncation)
	Error   string // populated when Loaded is false
	IsDir   bool   // true when Path resolves to a directory
	Outside bool   // true when the resolved path is outside the working directory
	Note    string // optional extra status, e.g. "resized from 8.2 MB … to 1.1 MB"

	// IsImage marks a loaded image file. Its bytes live in Data (not
	// Content) and ride the user message as an image block; BuildSection
	// only emits a one-line note for it.
	IsImage   bool
	MediaType string
	Data      []byte
}

// Image is one decoded image attachment produced by Images.
type Image struct {
	Data      []byte
	MediaType string
	Name      string
}

var imageMediaTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// sensitiveNames are exact (lowercased) basenames that are credential
// or secret stores.
var sensitiveNames = map[string]bool{
	".env": true, "credentials": true, "credentials.json": true,
	"authorized_keys": true, "id_rsa": true, "id_ed25519": true,
	"id_ecdsa": true, "id_dsa": true,
	".npmrc": true, ".pypirc": true, ".git-credentials": true,
	".pgpass": true, ".netrc": true,
}

var sensitiveSuffixes = []string{
	".pem", ".key", ".p12", ".pfx", ".kdbx", ".tfstate", ".tfstate.backup",
}

// IsSensitivePath identifies common credential and secret-file patterns
// that must not be sent to a model through @ references (and are hidden
// from the file picker). It looks at the basename only; directory-level
// protection is Blocked's job.
func IsSensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(filepath.ToSlash(path)))
	if sensitiveNames[base] || strings.HasPrefix(base, ".env.") {
		return true
	}
	for _, suffix := range sensitiveSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
