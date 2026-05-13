package usercmd

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// defaultsFS holds the markdown bodies for commands shipped with the
// binary. Each file is parsed at startup the same way an on-disk
// command is, so the surface area (frontmatter keys, argument
// substitution, @file refs) stays identical regardless of whether
// the command came from disk or the binary.
//
//go:embed all:defaults
var defaultsFS embed.FS

// LoadDefaults parses the embedded built-in commands. The shape
// mirrors `loadScope` (file → Command) but reads from the embed.FS
// instead of the disk and never returns "duplicate name" errors
// (the repo's own file layout enforces uniqueness — duplicates can't
// exist at build time).
//
// All returned commands carry Scope=ScopeDefault. SourceFile is set
// to the FS-relative path (e.g. `defaults/git/commit-message.md`) so
// /help can render a "(default)" tag instead of a real on-disk path.
func LoadDefaults() (cmds []Command, errs []LoadError) {
	walkErr := fs.WalkDir(defaultsFS, "defaults", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, LoadError{Path: p, Err: err, Level: LevelError})
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}

		name, nameErr := commandNameFromEmbed("defaults", p)
		if nameErr != nil {
			// A misnamed file in the embed tree is a build-time bug,
			// not a user error — surface as LevelError so it shows
			// up loud in tests. (Production builds would also see
			// it; the bug should be fixed at the source, not at
			// runtime.)
			errs = append(errs, LoadError{Path: p, Err: nameErr, Level: LevelError})
			return nil
		}

		content, readErr := defaultsFS.ReadFile(p)
		if readErr != nil {
			errs = append(errs, LoadError{Path: p, Err: readErr, Level: LevelError})
			return nil
		}

		parsed, parseErr := parse(content)
		if parseErr != nil {
			errs = append(errs, LoadError{Path: p, Err: parseErr, Level: LevelError})
			return nil
		}

		cmds = append(cmds, Command{
			Name:        name,
			Description: strings.TrimSpace(parsed.Frontmatter.Description),
			ArgHint:     strings.TrimSpace(parsed.Frontmatter.ArgumentHint),
			Body:        parsed.Body,
			SourceFile:  p, // FS-relative; renderers special-case this prefix
			Scope:       ScopeDefault,
		})
		return nil
	})
	if walkErr != nil {
		errs = append(errs, LoadError{Path: "defaults", Err: walkErr, Level: LevelError})
	}
	sortByName(cmds)
	return cmds, errs
}

// commandNameFromEmbed derives the slash command name from an embed.FS
// path. Same logic as commandNameFrom but operates on forward-slash
// paths directly (embed.FS uses Unix separators on every OS, so we
// don't need filepath.Rel/ToSlash).
func commandNameFromEmbed(root, p string) (string, error) {
	rel := strings.TrimPrefix(p, root+"/")
	rel = strings.TrimSuffix(rel, ".md")
	if rel == "" {
		return "", fmt.Errorf("empty command name derived from embed path %q", p)
	}
	segments := strings.Split(rel, "/")
	for _, seg := range segments {
		if !nameSegmentPattern.MatchString(seg) {
			return "", fmt.Errorf(
				"invalid embed path segment %q in %q", seg, p)
		}
	}
	return strings.Join(segments, ":"), nil
}

// LoadAll merges the embedded defaults with the on-disk user/project
// commands returned by Load. Precedence, highest first:
//
//  1. Project scope (`<cwd>/.yottacode/commands/`)
//  2. User scope (`~/.yottacode/commands/`)
//  3. Default scope (embedded into the binary)
//
// When a user or project command shadows a default of the same name,
// the default is silently replaced — that's the *expected* behavior of
// "ship a starter kit the user can customize," not a misconfiguration
// worth a startup notice. The same-scope and built-in shadow warnings
// from Load() still flow through unchanged.
func LoadAll(cwd string) (cmds []Command, errs []LoadError) {
	defaults, derrs := LoadDefaults()
	errs = append(errs, derrs...)

	custom, cerrs := Load(cwd)
	errs = append(errs, cerrs...)

	merged := map[string]Command{}
	for _, d := range defaults {
		merged[d.Name] = d
	}
	for _, c := range custom {
		// User/project silently shadows default. No warning because
		// editing a default by dropping a same-named file in
		// ~/.yottacode/commands/ is the documented override path,
		// not a mistake.
		merged[c.Name] = c
	}

	cmds = make([]Command, 0, len(merged))
	for _, c := range merged {
		cmds = append(cmds, c)
	}
	sortByName(cmds)
	return cmds, errs
}

// DisplayPath returns the path string to show in /help for a command.
// For on-disk commands it's the SourceFile as-is; for defaults it
// renders a "(default)" tag instead of the FS-internal path which
// would be useless to the user (they can't `vim` the embed.FS).
func DisplayPath(cmd Command) string {
	if cmd.Scope == ScopeDefault {
		return "(default)"
	}
	return cmd.SourceFile
}

