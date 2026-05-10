// Package filerefs implements the @file reference system: when a user
// types something like "explain @main.go and @internal/foo.go", the
// agent detects the @-prefixed tokens, reads the matching files from
// the working directory, and injects their contents into the system
// prompt so the model sees the file as authoritative context for the
// turn — no extra read_file tool call required.
//
// The package is split into three small phases:
//
//   - Parse extracts @<path> tokens from a user message.
//   - Load reads each token's file relative to a cwd, with size caps
//     and a strict cwd-confinement check (no symlinks, no traversal).
//   - Inject rewrites a system-prompt string by stripping any prior
//     auto-injected section and appending a fresh one for the current
//     turn's refs.
//
// All file-load failures are recorded on the Ref itself (Error field)
// rather than returned globally — the caller renders them as warnings
// and still sends the turn. Hard-failing on a missing @file would be
// frustrating in practice (typos, deleted files, paths inside compound
// words like "@TODO").
package filerefs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Marker is the heading that identifies an auto-injected file-refs
// block in a system prompt. Inject strips any prior block matching
// this marker before appending a new one — which keeps the prompt
// from accumulating stale snapshots across turns.
const Marker = "## Referenced files (auto-injected from @-prefixed paths)"

// Size caps. Per-file is enforced first; total accumulates as files
// load and short-circuits the rest once breached. Both are deliberate
// (small) so the system prompt never balloons.
const (
	MaxFileSize  = 200 * 1024  // 200 KiB per file
	MaxTotalSize = 1024 * 1024 // 1 MiB total per turn
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
}

// refRegex matches an @-prefixed path token. The leading group enforces
// that the @ sits at the start of input or after a "boundary" character
// (whitespace, opening bracket, comma, semicolon) — that's what keeps
// "email@example.com" from being treated as a file reference. The
// captured path stops at any whitespace or closing bracket so trailing
// punctuation like "@foo.go." or "(@foo.go)" gets handled by Parse.
var refRegex = regexp.MustCompile(`(?:^|[\s(\[{,;])@([^\s@()\[\]{}]+)`)

// trailingPunct is stripped from the right edge of a captured path.
// Periods are deliberately excluded — `.` is part of normal extensions
// and stripping it would mangle "@foo.go" anywhere it sits next to a
// sentence boundary. We accept that "@foo.go." (sentence-final) keeps
// the trailing dot and may fail to load; the user gets a clear error.
const trailingPunct = ",;:!?)]}>"

// Parse scans input for @<path> tokens and returns one Ref per unique
// path, in first-appearance order. The Token field preserves the
// original "@path" form for echoing back to the user; Path is the
// cleaned version Load consumes. Empty input or no matches → nil.
func Parse(input string) []Ref {
	if !strings.Contains(input, "@") {
		return nil
	}
	matches := refRegex.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	out := make([]Ref, 0, len(matches))
	for _, m := range matches {
		raw := strings.TrimRight(m[1], trailingPunct)
		if raw == "" || raw == "." || raw == ".." {
			continue
		}
		if seen[raw] {
			continue
		}
		seen[raw] = true
		out = append(out, Ref{
			Token: "@" + raw,
			Path:  raw,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Load reads each ref's file relative to cwd and fills Content / Error
// in place. Refs are returned in the same order. The cwd is
// canonicalized once so symlink/traversal checks compare against a
// stable root; absolute paths and any path that resolves outside cwd
// are rejected with a clear Error rather than read.
//
// Size enforcement: a file larger than MaxFileSize is read up to the
// cap and marked with a "...truncated" note in Content. Once the
// running total exceeds MaxTotalSize, every remaining ref is rejected
// with a budget-exhausted Error so the system prompt stays bounded.
func Load(refs []Ref, cwd string) []Ref {
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
	rootEval, err := filepath.EvalSymlinks(rootAbs)
	if err == nil {
		rootAbs = rootEval
	}

	totalSize := 0
	for i := range refs {
		ref := &refs[i]
		if filepath.IsAbs(ref.Path) {
			ref.Error = "absolute paths are not supported; use a path relative to the working directory"
			continue
		}
		if strings.HasPrefix(ref.Path, "~") {
			ref.Error = "home-relative paths (~) are not supported; use a path relative to the working directory"
			continue
		}
		joined := filepath.Join(rootAbs, ref.Path)
		// Resolve any traversal in the joined path and check the
		// result still falls under cwd. We use Clean here rather than
		// EvalSymlinks because we want to reject symlinks outright
		// below — EvalSymlinks would happily follow a symlink to
		// /etc/passwd and we'd never know.
		cleaned := filepath.Clean(joined)
		if !pathContains(rootAbs, cleaned) {
			ref.Error = "path resolves outside the working directory"
			continue
		}
		info, err := os.Lstat(cleaned)
		if err != nil {
			if os.IsNotExist(err) {
				ref.Error = "no such file or directory"
			} else {
				ref.Error = err.Error()
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			ref.Error = "symlinks are not followed for safety"
			continue
		}
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
				ref.Loaded = false
				ref.Content = ""
				ref.Size = 0
				ref.Error = fmt.Sprintf("total reference budget (%d bytes) exhausted before this entry", MaxTotalSize)
				totalSize -= len(ref.Content)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			ref.Error = "not a regular file"
			continue
		}
		// Read up to MaxFileSize+1 so we can detect overflow without
		// loading multi-megabyte files into memory.
		data, truncated, err := readBounded(cleaned, MaxFileSize)
		if err != nil {
			ref.Error = err.Error()
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

// readBounded reads up to limit+1 bytes from path; if it actually got
// limit+1 bytes the file is over the cap and we report truncated=true
// alongside the limit-sized prefix.
func readBounded(path string, limit int) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	buf := make([]byte, limit+1)
	n, err := readFull(f, buf)
	if err != nil {
		return nil, false, err
	}
	if n > limit {
		return buf[:limit], true, nil
	}
	return buf[:n], false, nil
}

// readFull reads as much as possible into buf until EOF or error.
// Distinct from io.ReadFull which errors on short reads — we *want*
// short reads to return cleanly so we know the file fit in the cap.
func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			if err.Error() == "EOF" {
				return total, nil
			}
			return total, err
		}
		if n == 0 {
			return total, nil
		}
	}
	return total, nil
}

// pathContains reports whether child is the root or a descendant of
// root. Both are expected to be absolute and Clean'd. Equality counts
// as containment — referencing the cwd itself (`@.`) is allowed.
func pathContains(root, child string) bool {
	if root == child {
		return true
	}
	rootSep := root
	if !strings.HasSuffix(rootSep, string(filepath.Separator)) {
		rootSep += string(filepath.Separator)
	}
	return strings.HasPrefix(child, rootSep)
}

// renderDirListing formats a directory's entries as a short, sorted
// listing. Used when @<path> resolves to a directory; we don't recurse
// (that's what list_project_structure is for) — we just give the model
// enough to decide what to read next.
func renderDirListing(entries []os.DirEntry) string {
	var b strings.Builder
	b.WriteString("(directory listing)\n")
	for _, e := range entries {
		marker := "f"
		if e.IsDir() {
			marker = "d"
		} else if e.Type()&os.ModeSymlink != 0 {
			marker = "l"
		}
		fmt.Fprintf(&b, "%s %s\n", marker, e.Name())
	}
	return strings.TrimRight(b.String(), "\n")
}

// Rewrite returns input with the leading `@` stripped from every
// successfully-loaded ref's token. Refs that failed to load keep
// their `@` so the model can ask the user about the typo / missing
// path explicitly.
//
// Why this exists: without rewriting, the user's message still
// contains `@docs/foo.md` after we've injected the file's contents
// into the system prompt. Some models then call `read_file("@docs/foo.md")`
// — passing the literal `@` to the tool, which fails because the
// `@` becomes part of the path. Stripping it produces a plain path
// that pairs naturally with the section we already injected up in
// the system prompt and removes the failure mode entirely.
func Rewrite(input string, refs []Ref) string {
	if input == "" || len(refs) == 0 {
		return input
	}
	loaded := make(map[string]bool, len(refs))
	for _, r := range refs {
		if r.Loaded {
			loaded[r.Path] = true
		}
	}
	if len(loaded) == 0 {
		return input
	}
	return refRegex.ReplaceAllStringFunc(input, func(match string) string {
		// match = "<boundary>@<path-with-trailing-punct>?"
		// boundary is "" (start of input) or a single byte from the
		// boundary class. Find the @ and split there.
		idx := strings.Index(match, "@")
		if idx < 0 {
			return match
		}
		boundary := match[:idx]
		raw := match[idx+1:]
		path := strings.TrimRight(raw, trailingPunct)
		tail := raw[len(path):]
		if !loaded[path] {
			return match
		}
		return boundary + path + tail
	})
}

// BuildSection formats a slice of Refs into a markdown block ready for
// injection into the system prompt. Refs whose Loaded is false are
// emitted as inline error notices so the model sees the failure
// instead of silently missing a reference. Returns "" when refs is
// empty so callers can short-circuit "no refs → no injection."
func BuildSection(refs []Ref) string {
	if len(refs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(Marker)
	b.WriteString("\n\nThe user prefixed one or more paths with `@` in their last message. ")
	b.WriteString("Their full contents are inlined below — treat these as authoritative for this turn. ")
	b.WriteString("Do NOT call read_file (or any other read tool) for these paths; the body you'd get from a read is already here. ")
	b.WriteString("If a path appears in the message without a leading `@` after this header, it has already been attached above and you can answer without reading it again.\n\n")
	for _, r := range refs {
		fmt.Fprintf(&b, "### %s\n", r.Token)
		if !r.Loaded {
			fmt.Fprintf(&b, "_could not load: %s_\n\n", r.Error)
			continue
		}
		if r.IsDir {
			fmt.Fprintf(&b, "```\n%s\n```\n\n", r.Content)
			continue
		}
		lang := languageFromPath(r.Path)
		fmt.Fprintf(&b, "```%s\n%s\n```\n\n", lang, r.Content)
	}
	return strings.TrimRight(b.String(), "\n")
}

// StripSection removes a prior auto-injected file-refs block from a
// system prompt, returning the prompt with the section excised. Used
// by Inject so successive turns replace (not accumulate) the block.
// If no marker is present, prompt is returned unchanged.
func StripSection(prompt string) string {
	idx := strings.Index(prompt, Marker)
	if idx < 0 {
		return prompt
	}
	// Walk back over the blank-line separator we wrote in front of the
	// section so we don't leave dangling whitespace.
	cut := idx
	for cut > 0 && (prompt[cut-1] == '\n' || prompt[cut-1] == ' ' || prompt[cut-1] == '\t') {
		cut--
	}
	return strings.TrimRight(prompt[:cut], "\n")
}

// Inject rewrites prompt by stripping any prior auto-injected block and
// appending a fresh one for the supplied refs. When refs is empty the
// prompt is returned with the block stripped (so a turn that uses no
// refs cleans up after one that did).
func Inject(prompt string, refs []Ref) string {
	stripped := StripSection(prompt)
	section := BuildSection(refs)
	if section == "" {
		return stripped
	}
	if stripped == "" {
		return section
	}
	return stripped + "\n\n" + section
}

// languageFromPath maps a file extension to a markdown code-fence
// language tag. The set covers the common cases that show up in
// practice; anything unknown returns "" so the fence renders as plain
// text. Mirrors the (smaller) mapping used by the syntax-highlighting
// code in internal/tui/highlight.go but is intentionally separate —
// this list only needs to inform the LLM, not chroma.
func languageFromPath(p string) string {
	ext := strings.ToLower(filepath.Ext(p))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".jsx":
		return "jsx"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".swift":
		return "swift"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".rb":
		return "ruby"
	case ".php":
		return "php"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".fish":
		return "fish"
	case ".lua":
		return "lua"
	case ".sql":
		return "sql"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".scss", ".sass":
		return "scss"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".xml":
		return "xml"
	case ".md", ".markdown":
		return "markdown"
	case ".dockerfile":
		return "dockerfile"
	case ".tf":
		return "terraform"
	case ".proto":
		return "protobuf"
	case ".graphql", ".gql":
		return "graphql"
	case ".vue":
		return "vue"
	case ".svelte":
		return "svelte"
	case ".r":
		return "r"
	case ".pl", ".pm":
		return "perl"
	case ".ex", ".exs":
		return "elixir"
	case ".erl":
		return "erlang"
	case ".clj", ".cljs":
		return "clojure"
	case ".scala":
		return "scala"
	case ".dart":
		return "dart"
	case ".zig":
		return "zig"
	case ".nim":
		return "nim"
	case ".hs":
		return "haskell"
	case ".ml", ".mli":
		return "ocaml"
	case ".fs", ".fsx":
		return "fsharp"
	}
	// Files without an extension that have a known basename.
	switch strings.ToLower(filepath.Base(p)) {
	case "dockerfile":
		return "dockerfile"
	case "makefile":
		return "makefile"
	}
	return ""
}
