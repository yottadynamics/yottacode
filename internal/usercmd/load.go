package usercmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Scope tags where a command came from. Surfaces in /help so users can
// see whether a command they're looking at lives on disk (user or
// project) or is shipped with the binary (default).
type Scope string

const (
	// ScopeDefault is for commands embedded into the binary as the
	// built-in starter kit (`/git:commit-message`, `/check:review`,
	// etc.). Lowest precedence — a user or project file with the
	// same name shadows the default. The body is editable by copying
	// the default to ~/.yottacode/commands/<same-path>.md.
	ScopeDefault Scope = "default"
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

// Command is one loaded custom command. Body is the post-frontmatter
// markdown, unsubstituted (the TUI calls Substitute() with the user's
// args at invocation time).
type Command struct {
	Name        string
	Description string
	ArgHint     string
	Body        string
	SourceFile  string
	Scope       Scope
}

// Level grades a LoadError as either a hard error (the file did not
// produce a usable command) or a warning (the file is fine but
// shadowed / dropped due to a collision). The TUI renders the two
// levels with different styles.
type Level int

const (
	LevelError Level = iota
	LevelWarning
)

// LoadError describes one problem encountered during Load. The file
// path is always populated so the user can locate the offending file.
type LoadError struct {
	Path  string
	Err   error
	Level Level
}

func (e LoadError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s: %v", e.Path, e.Err)
	}
	return e.Err.Error()
}

// Reserved is the set of built-in slash command names a custom file
// is forbidden from shadowing. Kept here (not derived from the TUI
// package's allSlash) so this package stays standalone-testable; the
// TUI's init() is the source of truth and a one-line drift test in
// the TUI package guards against new built-ins being forgotten.
var Reserved = map[string]bool{
	"help":           true,
	"quit":           true,
	"clear":          true,
	"permissions":    true,
	"system":         true,
	"sessions":       true,
	"model":          true,
	"provider":       true,
	"doctor":         true,
	"redo":           true,
	"recall":         true,
	"summarize":      true,
	"checkpoints":    true,
	"memory":         true,
	"max-iterations": true,
	"setup":          true,
	"init":           true,
	"plan":           true,
	"subagents":      true,
	"skills":         true,
	"git-commit":     true,
	"git-create-pr":  true,
	"git-review-pr":  true,
	"git-push":       true,
	"git-update-pr":  true,
}

// Load walks both scope directories (user first, then project) and
// returns the merged command list plus per-file load errors.
//
// Conflict resolution:
//   - Same name within the SAME scope: both copies dropped, error
//     emitted. We can't pick a winner safely (the user clearly
//     intended different prompts) and silently keeping one would
//     surprise them.
//   - Same name across scopes: project wins. The user-scope copy is
//     dropped with an info-level notice so the user understands why
//     /foo doesn't behave like their global file.
//   - Same name as a built-in: custom dropped with a warning.
//     Built-ins can't be shadowed because the dispatcher walks
//     built-ins first.
//
// Errors are returned alongside the commands rather than as a single
// error — one broken file should not block a directory of working
// commands. The TUI iterates errs at startup and emits one styled
// notice per entry.
func Load(cwd string) (cmds []Command, errs []LoadError) {
	userDir, err := UserCommandsDir()
	if err != nil {
		errs = append(errs, LoadError{Err: err, Level: LevelError})
	}

	// scope-keyed maps so duplicate detection is per-scope; we merge
	// across scopes at the end with project-wins semantics.
	userByName := map[string]Command{}
	userDup := map[string]bool{}
	if userDir != "" {
		var scopeErrs []LoadError
		userByName, userDup, scopeErrs = loadScope(userDir, ScopeUser)
		errs = append(errs, scopeErrs...)
	}

	projectByName := map[string]Command{}
	projectDup := map[string]bool{}
	if projectDir := ProjectCommandsDir(cwd); projectDir != "" {
		var scopeErrs []LoadError
		projectByName, projectDup, scopeErrs = loadScope(projectDir, ScopeProject)
		errs = append(errs, scopeErrs...)
	}

	// Drop reserved names from each scope (collected into a separate
	// list so the warning carries the file path).
	for name := range userByName {
		if Reserved[name] {
			errs = append(errs, LoadError{
				Path:  userByName[name].SourceFile,
				Err:   fmt.Errorf("name %q shadows a built-in command and was dropped", name),
				Level: LevelWarning,
			})
			delete(userByName, name)
		}
	}
	for name := range projectByName {
		if Reserved[name] {
			errs = append(errs, LoadError{
				Path:  projectByName[name].SourceFile,
				Err:   fmt.Errorf("name %q shadows a built-in command and was dropped", name),
				Level: LevelWarning,
			})
			delete(projectByName, name)
		}
	}

	// Cross-scope merge: project wins.
	merged := map[string]Command{}
	for name, cmd := range userByName {
		if userDup[name] {
			continue
		}
		merged[name] = cmd
	}
	for name, cmd := range projectByName {
		if projectDup[name] {
			continue
		}
		if userCmd, shadowed := merged[name]; shadowed {
			errs = append(errs, LoadError{
				Path: userCmd.SourceFile,
				Err: fmt.Errorf(
					"shadowed by project command at %s", cmd.SourceFile),
				Level: LevelWarning,
			})
		}
		merged[name] = cmd
	}

	cmds = make([]Command, 0, len(merged))
	for _, cmd := range merged {
		cmds = append(cmds, cmd)
	}
	// Stable ordering so /help and the palette render deterministically
	// across runs. Sorting by name is cheap and matches how users
	// scan an alphabetized list.
	sortByName(cmds)
	return cmds, errs
}

// loadScope walks one directory, parsing every .md file into a Command.
// Returns:
//   - byName: name → Command for files that loaded cleanly and have
//     no same-scope sibling with the same derived name.
//   - dup: set of names that appeared more than once in this scope
//     (both copies will be dropped by the caller).
//   - errs: per-file load errors (bad name, bad frontmatter, symlink,
//     etc.) plus one entry per duplicate-name pair.
func loadScope(dir string, scope Scope) (byName map[string]Command, dup map[string]bool, errs []LoadError) {
	byName = map[string]Command{}
	dup = map[string]bool{}

	info, statErr := os.Lstat(dir)
	if errors.Is(statErr, fs.ErrNotExist) {
		return byName, dup, nil
	}
	if statErr != nil {
		errs = append(errs, LoadError{Path: dir, Err: statErr, Level: LevelError})
		return byName, dup, errs
	}
	if !info.IsDir() {
		// Symlink-to-dir or a stray file at the dir path. Refuse
		// rather than follow — same conservative stance memory takes.
		if info.Mode()&os.ModeSymlink != 0 {
			errs = append(errs, LoadError{
				Path: dir,
				Err:  errors.New("commands path is a symlink; refusing to follow"),
				Level: LevelError,
			})
		}
		return byName, dup, errs
	}

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, LoadError{Path: path, Err: err, Level: LevelError})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}

		// Refuse symlinks. A symlinked .md inside commands/ could
		// reach into arbitrary paths and surprise the user; the cost
		// of supporting it is not worth the rope.
		lst, lerr := os.Lstat(path)
		if lerr == nil && lst.Mode()&os.ModeSymlink != 0 {
			errs = append(errs, LoadError{
				Path:  path,
				Err:   errors.New("symlinked command files are not supported"),
				Level: LevelError,
			})
			return nil
		}

		name, nameErr := commandNameFrom(dir, path)
		if nameErr != nil {
			errs = append(errs, LoadError{Path: path, Err: nameErr, Level: LevelError})
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			errs = append(errs, LoadError{Path: path, Err: readErr, Level: LevelError})
			return nil
		}

		p, parseErr := parse(content)
		if parseErr != nil {
			errs = append(errs, LoadError{Path: path, Err: parseErr, Level: LevelError})
			return nil
		}

		cmd := Command{
			Name:        name,
			Description: strings.TrimSpace(p.Frontmatter.Description),
			ArgHint:     strings.TrimSpace(p.Frontmatter.ArgumentHint),
			Body:        p.Body,
			SourceFile:  path,
			Scope:       scope,
		}

		if prior, exists := byName[name]; exists {
			errs = append(errs, LoadError{
				Path: path,
				Err: fmt.Errorf(
					"duplicate name %q (also at %s); both copies dropped",
					name, prior.SourceFile),
				Level: LevelError,
			})
			dup[name] = true
			return nil
		}
		byName[name] = cmd
		return nil
	})
	if walkErr != nil {
		errs = append(errs, LoadError{Path: dir, Err: walkErr, Level: LevelError})
	}
	return byName, dup, errs
}

// commandNameFrom derives the slash command name from a file path
// relative to its scope root. "commands/foo.md" → "foo";
// "commands/frontend/component.md" → "frontend:component". Each path
// segment is validated against nameSegmentPattern; any failure
// rejects the whole file.
func commandNameFrom(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("relative path: %w", err)
	}
	rel = strings.TrimSuffix(rel, ".md")
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." {
		return "", errors.New("empty command name derived from path")
	}
	segments := strings.Split(rel, "/")
	for _, seg := range segments {
		if !nameSegmentPattern.MatchString(seg) {
			return "", fmt.Errorf(
				"invalid path segment %q (lowercase letters, digits, hyphens; must start with a letter or digit)",
				seg)
		}
	}
	return strings.Join(segments, ":"), nil
}

// sortByName sorts commands lexicographically in place. Stable
// ordering keeps /help and the palette deterministic across runs.
func sortByName(cmds []Command) {
	// Tiny n; the simple insertion sort avoids pulling in sort.Slice
	// for a list that's typically < 30 items.
	for i := 1; i < len(cmds); i++ {
		for j := i; j > 0 && cmds[j-1].Name > cmds[j].Name; j-- {
			cmds[j-1], cmds[j] = cmds[j], cmds[j-1]
		}
	}
}
