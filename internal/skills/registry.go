package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadResult is what LoadAll returns: a deduplicated, name-sorted
// slice of skills plus a slice of human-readable warnings the caller
// can surface to the user. Warnings are non-fatal — a single
// malformed skill should not block startup.
type LoadResult struct {
	Skills   []Skill
	Warnings []string
}

// LoadAll resolves skills from all three sources (built-in, user,
// project) and merges them with project > user > builtin precedence.
// reservedNames is the set of slash-command names a skill must not
// shadow — when a skill's name is in the set, the skill is dropped
// with a warning so the existing slash command keeps its meaning.
// Pass nil to skip the reserved-name guard (used by tests).
func LoadAll(cwd string, reservedNames map[string]bool) (LoadResult, error) {
	var res LoadResult
	seen := map[string]int{}

	add := func(sk Skill) {
		if reservedNames != nil && reservedNames[sk.Name] {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"skill %q (%s) shadows a built-in slash command — dropping",
				sk.Name, sk.Source))
			return
		}
		if idx, ok := seen[sk.Name]; ok {
			res.Skills[idx] = sk
			return
		}
		seen[sk.Name] = len(res.Skills)
		res.Skills = append(res.Skills, sk)
	}

	// Lowest precedence first so later sources overwrite earlier ones.
	for _, sk := range LoadBuiltins() {
		add(sk)
	}

	userDir, err := UserSkillsDir()
	if err == nil {
		uskills, uwarn := loadDir(userDir, ScopeUser)
		res.Warnings = append(res.Warnings, uwarn...)
		for _, sk := range uskills {
			add(sk)
		}
	}

	projectDir := ProjectSkillsDir(cwd)
	pskills, pwarn := loadDir(projectDir, ScopeProject)
	res.Warnings = append(res.Warnings, pwarn...)
	for _, sk := range pskills {
		add(sk)
	}

	sort.Slice(res.Skills, func(i, j int) bool { return res.Skills[i].Name < res.Skills[j].Name })
	return res, nil
}

// loadDir scans dir for skill directories — each subdir is one skill
// rooted at <dir>/<slug>/SKILL.md. Missing dir is a non-error (a
// user without any custom skills shouldn't see noise on startup).
// Slug-validation and name==dir matching happen in ParseSkillFile.
func loadDir(dir string, scope Scope) ([]Skill, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("read %s skills dir %q: %v", scope, dir, err)}
	}
	var out []Skill
	var warnings []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		// Skip hidden dirs and the conventional staging dirs people use
		// when developing skills locally.
		if strings.HasPrefix(slug, ".") || strings.HasPrefix(slug, "_") {
			continue
		}
		skillDir := filepath.Join(dir, slug)
		skillFile := filepath.Join(skillDir, "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// A subdir without SKILL.md isn't a skill — silently
				// ignore. Some users keep README.md / .git / etc. at
				// the top level of their skills dir.
				continue
			}
			warnings = append(warnings, fmt.Sprintf("read %s skill %q: %v", scope, skillFile, err))
			continue
		}
		sk, err := ParseSkillFile(data, slug)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("parse %s skill %q: %v", scope, skillFile, err))
			continue
		}
		sk.Source = scope
		sk.SourcePath = skillFile
		sk.Dir = skillDir
		out = append(out, sk)
	}
	return out, warnings
}

// Find returns the named skill or nil. Case-sensitive: skill names
// are lowercase-only by the slug pattern, so casing is part of identity.
func Find(skills []Skill, name string) *Skill {
	for i := range skills {
		if skills[i].Name == name {
			return &skills[i]
		}
	}
	return nil
}
