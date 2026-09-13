package agent

import "strings"

// EditFailureKind is the shared classification used by edit tools and UI
// surfaces to keep recovery guidance consistent.
type EditFailureKind string

const (
	EditFailureUnknown       EditFailureKind = "unknown"
	EditFailureStale         EditFailureKind = "stale"
	EditFailureMissingAnchor EditFailureKind = "missing_anchor"
	EditFailureAmbiguous     EditFailureKind = "ambiguous"
	EditFailureInvalidRange  EditFailureKind = "invalid_range"
	EditFailureInvalidText   EditFailureKind = "invalid_text"
	EditFailureOverlap       EditFailureKind = "overlap"
	EditFailureInvalidHash   EditFailureKind = "invalid_hash"
	EditFailureMalformed     EditFailureKind = "malformed"
	EditFailureRepeated      EditFailureKind = "repeated"
)

// ClassifyEditFailure maps textual tool results to the stable recovery
// vocabulary shared by the model-facing tools and TUI. Tool output remains
// plain text on the wire, so classification deliberately accepts both typed
// error names and their human-readable forms.
func ClassifyEditFailure(toolName, output string) EditFailureKind {
	s := strings.ToLower(output)
	switch toolName {
	case "edit_file":
		if strings.Contains(s, "byte-for-byte identical") || strings.Contains(s, "no-op") {
			return EditFailureRepeated
		}
		if strings.Contains(s, "old_string not found") || strings.Contains(s, "appears ") {
			return EditFailureStale
		}
	case "edit_anchored":
		if strings.Contains(s, "anchor is required") {
			return EditFailureMissingAnchor
		}
		if strings.Contains(s, "stale anchor") {
			return EditFailureStale
		}
		if strings.Contains(s, "ambiguous") {
			return EditFailureAmbiguous
		}
		if strings.Contains(s, "overlap") || strings.Contains(s, "same insertion point") {
			return EditFailureOverlap
		}
		if strings.Contains(s, "start_anchor must") || strings.Contains(s, "unsupported op") || strings.Contains(s, "requires non-empty") || strings.Contains(s, "does not accept") || strings.Contains(s, "no-op") {
			return EditFailureRepeated
		}
	case "apply_hashline":
		if strings.Contains(s, "concurrent_write") || strings.Contains(s, "file changed while") {
			return EditFailureStale
		}
		if strings.Contains(s, "hash_mismatch") || strings.Contains(s, "old bytes do not match anchor hash") {
			return EditFailureInvalidHash
		}
		if strings.Contains(s, "stale_anchor") || strings.Contains(s, "stale anchor") {
			return EditFailureStale
		}
		if strings.Contains(s, "ambiguous_anchor") || strings.Contains(s, "ambiguous anchor") {
			return EditFailureAmbiguous
		}
		if strings.Contains(s, "invalid_range") || strings.Contains(s, "outside source bounds") {
			return EditFailureInvalidRange
		}
		if strings.Contains(s, "invalid_text") || strings.Contains(s, "non-text") || strings.Contains(s, "valid utf-8") {
			return EditFailureInvalidText
		}
		if strings.Contains(s, "overlapping_hunks") || strings.Contains(s, "overlap") {
			return EditFailureOverlap
		}
		if strings.Contains(s, "invalid_hash") || strings.Contains(s, "hash must") || strings.Contains(s, "hash is required") {
			return EditFailureInvalidHash
		}
	case "apply_diff":
		switch ClassifyPatchFailure(output) {
		case PatchFailureMalformed:
			return EditFailureMalformed
		case PatchFailureStale:
			return EditFailureStale
		}
	}
	return EditFailureUnknown
}
