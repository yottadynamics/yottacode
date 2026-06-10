package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/yottadynamics/yottacode/internal/ychome"
)

// PlanModeState is the per-session, runtime-mutable plan-mode flag the
// loop reads on every tool dispatch and prompt assembly. The TUI flips
// `Active` from the main goroutine via /plan or Shift+Tab while the
// agent goroutine reads it inside executeToolCall and streamIteration —
// atomic.Bool keeps that benign race detector-clean.
//
// The pointer is shared between LoopConfig and the TUI Model, so a flip
// in cmd_plan.go takes effect on the very next iteration with no
// reconstruction or message rewriting. PlanFile is set when entering
// plan mode (from `/plan <topic>` arg or the first user message) and
// kept stable for the lifetime of the active plan. Until it's set,
// writes to the plan file are blocked — the gate compares against
// PlanFile and an empty string never matches a real path.
type PlanModeState struct {
	Active   atomic.Bool
	PlanFile string
}

// IsActive is a nil-safe check used by the loop.
func (p *PlanModeState) IsActive() bool {
	return p != nil && p.Active.Load()
}

// SlugFromPrompt converts a free-form prompt into a stable, filesystem-
// safe slug suitable for a plan filename. Format:
//
//	<kebab>-<16-hex-of-sha256(salt|prompt)>
//
// The kebab portion caps at 60 characters so the suffixed filename
// stays well under common filesystem limits. Empty / all-punctuation
// input falls back to "untitled".
//
// The hash suffix gives every plan a unique filename even when two
// prompts share the same opening words (e.g. "fix bug in foo" vs
// "fix bug in bar" both truncated to "fix-bug-in") and avoids the
// slug-collision class entirely. Salt is typically the session ID, so
// the same prompt typed in a different session lands on a different
// file.
func SlugFromPrompt(prompt, salt string) string {
	kebab := kebabFromPrompt(prompt)
	if kebab == "" {
		kebab = "untitled"
	}
	if len(kebab) > 60 {
		kebab = strings.TrimRight(kebab[:60], "-")
	}
	sum := sha256.Sum256([]byte(salt + "|" + prompt))
	return kebab + "-" + hex.EncodeToString(sum[:8])
}

var nonSlugRune = regexp.MustCompile(`[^a-z0-9]+`)

func kebabFromPrompt(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsSpace(r), r == '-', r == '_', r == '/', r == '\\':
			b.WriteRune('-')
		}
	}
	out := nonSlugRune.ReplaceAllString(b.String(), "-")
	return strings.Trim(out, "-")
}

// PlansDir returns the directory plan files live under:
// $YOTTACODE_HOME/plans (when set) or ~/.yottacode/plans otherwise,
// via the shared ychome.Dir resolution. Does not create the directory
// — write_file's MkdirAll handles that lazily on first write.
func PlansDir() (string, error) {
	return ychome.Dir("plans")
}

// PlanEntry describes one plan file on disk for the picker + CLI
// resume flow. Slug is the basename without the `.md` suffix (matches
// the format SlugFromPrompt produces).
type PlanEntry struct {
	Slug     string
	Path     string
	Modified time.Time
	Size     int64
}

// ListPlans enumerates plan files under PlansDir() and returns them
// sorted by modified-time descending (newest first). Missing
// directory returns (nil, nil) — a fresh install has no plans yet
// and that's not an error.
func ListPlans() ([]PlanEntry, error) {
	dir, err := PlansDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]PlanEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, PlanEntry{
			Slug:     strings.TrimSuffix(e.Name(), ".md"),
			Path:     filepath.Join(dir, e.Name()),
			Modified: info.ModTime(),
			Size:     info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// MatchPlan returns the first plan whose slug contains the query
// (case-insensitive substring match). Plans must already be sorted
// newest-first — typical usage is ListPlans → MatchPlan, so the most
// recent match wins on ties. Returns nil when nothing matches; the
// caller renders the list to help the user pick a real slug.
func MatchPlan(plans []PlanEntry, query string) *PlanEntry {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	for i := range plans {
		if strings.Contains(strings.ToLower(plans[i].Slug), q) {
			return &plans[i]
		}
	}
	return nil
}

// planAddendumMaxBytes caps how much of the plan file we inline into
// the system prompt. Plans are usually small (markdown notes a few
// hundred lines tops); the cap is a safety net for a model that
// accidentally writes a megabyte of output to the plan path. Larger
// files are truncated with a clear marker so the model knows the
// view is partial.
const planAddendumMaxBytes = 50 * 1024

// readPlanFileForAddendum reads the plan file's contents for inclusion
// in the per-iteration system prompt. Always returns a non-empty
// string so the addendum's `%s` slot is never blank — distinguishes
// missing / empty / present-and-readable so the model knows whether
// to create the file with write_file or revise it with edit_file.
//
// Errors are squashed into a human-readable marker rather than
// propagated; the model never benefits from an "errno=2" leaking
// through, and a transient read failure shouldn't fail the whole turn.
func readPlanFileForAddendum(path string) string {
	if path == "" {
		return "(no plan file resolved yet — your next user message will name one; until then, use read tools to investigate)"
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "(file does not exist yet — your first write_file call will create it)"
		}
		return fmt.Sprintf("(could not stat plan file: %v)", err)
	}
	if info.Size() == 0 {
		return "(file is empty — write the initial plan now)"
	}
	if info.Size() > planAddendumMaxBytes {
		// Read just the prefix so the model still gets context. The
		// trailing marker tells it we truncated; it can read the full
		// file via read_file if it needs the rest.
		f, err := os.Open(path)
		if err != nil {
			return fmt.Sprintf("(could not open plan file: %v)", err)
		}
		defer f.Close()
		buf := make([]byte, planAddendumMaxBytes)
		n, _ := f.Read(buf)
		return string(buf[:n]) +
			fmt.Sprintf("\n\n…(truncated at %d bytes of %d total — call read_file for the rest)…", planAddendumMaxBytes, info.Size())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(could not read plan file: %v)", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return "(file is empty — write the initial plan now)"
	}
	return string(body)
}

// PlanFilePath resolves a slug to its absolute plan-file path. The
// returned path is what the gate compares writes against and what the
// system-prompt addendum tells the model to write to.
func PlanFilePath(slug string) (string, error) {
	dir, err := PlansDir()
	if err != nil {
		return "", err
	}
	if slug == "" {
		return "", fmt.Errorf("plan slug is empty")
	}
	return filepath.Join(dir, slug+".md"), nil
}

// IsPlanFileWrite reports whether this tool call is one of the
// mutating tools (write_file / edit_file / apply_diff) targeting the
// resolved plan file. The loop uses this to auto-approve those writes
// in plan mode — they're the model's only legitimate mutation surface
// while planning, so prompting on every edit is friction with no value
// (the gate already established the target is the plan file). False
// when planFile is empty, when the tool isn't a write tool, or when
// the target path differs from planFile.
func IsPlanFileWrite(name, argsJSON, planFile string) bool {
	if planFile == "" {
		return false
	}
	switch name {
	case "write_file", "edit_file", "apply_diff":
		target := pathFromWriteToolArgs(name, argsJSON)
		return target != "" && samePath(target, planFile)
	}
	return false
}

// isPlanBoundaryTool reports whether this tool transitions plan mode
// itself (enter_plan_mode / exit_plan_mode). Boundary tools NEVER
// auto-approve — not under yolo, not under auto mode, not under
// --yolo's BypassPermissions, not via a permissions Allow rule —
// because the approval round-trip IS the handshake in which the TUI
// flips the shared mode state before forwarding the decision. Any
// auto-approval path would skip that flip: the tool would report a
// mode change the session never made, and the model would plan
// against gates that aren't there (or implement against gates that
// still are).
func isPlanBoundaryTool(name string) bool {
	return name == "enter_plan_mode" || name == "exit_plan_mode"
}

// planModeSchemaFilter returns the adapter-tools filter streamIteration
// applies per iteration: exit_plan_mode is advertised only while plan
// mode is active; enter_plan_mode only while it is NOT. Everything
// else always passes. A flip mid-turn (Shift+Tab, or the enter/exit
// handshake itself) takes effect on the next iteration's schema; a
// stale-schema call that slips through in between is still caught by
// PlanModeGate (enter_plan_mode is not in its allowlist) or is a
// harmless no-op the TUI handles idempotently.
func planModeSchemaFilter(planActive bool) func(string) bool {
	return func(name string) bool {
		switch name {
		case "exit_plan_mode":
			return planActive
		case "enter_plan_mode":
			return !planActive
		}
		return true
	}
}

// PlanModeGate is the read/write classifier the loop consults before
// every tool call when plan mode is active. Returns ("", false) when
// the call may proceed, or (errorString, true) when the call must be
// refused. The errorString is what the model sees as the tool result,
// so it's phrased as actionable guidance — the model can switch to a
// read-only or plan-file alternative on the next iteration.
//
// Allowlist:
//   - exit_plan_mode: the only way out of plan mode.
//   - todo_write: progress tracking, no side effects.
//   - write_file/edit_file/apply_diff: only when the target path equals
//     planFile. Any other write target is blocked.
//   - any tool whose RequiresApproval returns false: the implicit
//     "read-only" classification (read_file, grep, glob, list_*,
//     git_log_file, fetch_url, …). New read-only tools auto-classify.
//
// The gate runs BEFORE Permissions.Evaluate so explicit deny rules
// still win in plan mode (Deny > plan-mode-allow > permissions >
// tool-policy).
func PlanModeGate(tool Tool, argsJSON, planFile string) (string, bool) {
	name := tool.Name()
	switch name {
	case "exit_plan_mode", "todo_write":
		return "", false
	case "write_file", "edit_file", "apply_diff":
		if planFile != "" {
			target := pathFromWriteToolArgs(name, argsJSON)
			if target != "" && samePath(target, planFile) {
				return "", false
			}
		}
		return planModeBlockMessage(name, planFile), true
	default:
		if !tool.RequiresApproval(argsJSON) {
			return "", false
		}
	}
	return planModeBlockMessage(name, planFile), true
}

func planModeBlockMessage(name, planFile string) string {
	if planFile == "" {
		return fmt.Sprintf(
			"error: tool %q is unavailable in plan mode; ask the user a clarifying question or call exit_plan_mode when your plan is ready (no plan file is set yet — your next user message will name one)",
			name,
		)
	}
	return fmt.Sprintf(
		"error: tool %q is unavailable in plan mode; you may only read, update %s, and call exit_plan_mode when your plan is ready",
		name, planFile,
	)
}

// pathFromWriteToolArgs extracts the target path field from the JSON
// args of a write/edit tool. write_file and edit_file use {"path": ...};
// apply_diff uses {"file_path": ...}. Returns "" on parse failure or
// missing field — the gate then treats the call as a non-plan-file
// write and blocks it.
func pathFromWriteToolArgs(name, argsJSON string) string {
	var probe struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &probe); err != nil {
		return ""
	}
	switch name {
	case "apply_diff":
		return probe.FilePath
	default:
		return probe.Path
	}
}

// samePath compares two filesystem paths after resolving each to its
// absolute, cleaned form. Avoids comparing an unresolved relative
// "./plans/foo.md" against the plan-file's absolute form.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ax, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	bx, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return filepath.Clean(ax) == filepath.Clean(bx)
}
