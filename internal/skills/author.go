package skills

// Authoring helpers — scaffold a starter SKILL.md and validate an
// existing one. The intent is to lower the floor for contributing
// community skills: a clean scaffold means new authors don't have
// to copy/paste frontmatter from another skill, and a one-shot
// validate command lets them confirm their work parses before they
// publish it.
//
// Both helpers reuse the existing parser/validator so the rules
// can't drift between authoring and runtime loading.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NewSkillOptions configures a scaffold call.
type NewSkillOptions struct {
	// Slug is the skill's canonical name. Must match the parent
	// directory name (the parser enforces this), so we use the same
	// string for both.
	Slug string
	// DestRoot is the parent dir the new skill folder is created
	// under. Defaults to UserSkillsDir() when empty. Tests inject a
	// tempdir.
	DestRoot string
	// Force overwrites an existing directory of the same slug.
	// Mirrors the install --force semantics.
	Force bool
}

// NewSkillResult describes a completed scaffold. Returned so the
// CLI can render "wrote ~/.yottacode/skills/<slug>/SKILL.md" without
// recomputing the path.
type NewSkillResult struct {
	Dir       string
	SkillFile string
}

// NewSkill scaffolds a starter SKILL.md at <DestRoot>/<Slug>/. The
// body is a small template hinting at where to fill in the
// description, body, and resource dirs. Refuses to overwrite an
// existing dir unless Force is set.
func NewSkill(opts NewSkillOptions) (NewSkillResult, error) {
	slug := strings.TrimSpace(opts.Slug)
	if slug == "" {
		return NewSkillResult{}, errors.New("slug is required")
	}
	if !skillNamePattern.MatchString(slug) {
		return NewSkillResult{}, fmt.Errorf(
			"invalid slug %q (allowed: lowercase letters, digits, hyphens; no leading/trailing/consecutive hyphens)", slug)
	}
	destRoot := opts.DestRoot
	if destRoot == "" {
		d, err := UserSkillsDir()
		if err != nil {
			return NewSkillResult{}, err
		}
		destRoot = d
	}
	if err := os.MkdirAll(destRoot, 0o700); err != nil {
		return NewSkillResult{}, err
	}
	dir := filepath.Join(destRoot, slug)
	if _, err := os.Stat(dir); err == nil {
		if !opts.Force {
			return NewSkillResult{}, fmt.Errorf("%s already exists — pass --force to overwrite", dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return NewSkillResult{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return NewSkillResult{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return NewSkillResult{}, err
	}
	skillFile := filepath.Join(dir, "SKILL.md")
	body := starterSkillBody(slug)
	if err := os.WriteFile(skillFile, []byte(body), 0o600); err != nil {
		return NewSkillResult{}, err
	}
	return NewSkillResult{Dir: dir, SkillFile: skillFile}, nil
}

// starterSkillBody is the scaffold's content. Intentionally tiny —
// just enough frontmatter to parse, with TODO markers in the
// fields a new author will want to fill in. The body suggests the
// progressive-disclosure pattern (overview + sub-sections) without
// prescribing it.
func starterSkillBody(slug string) string {
	return "---\n" +
		"name: " + slug + "\n" +
		"description: TODO — one short paragraph describing what this skill does and when to invoke it. The model sees this verbatim during selection.\n" +
		"license: TODO\n" +
		"metadata:\n" +
		"  author: TODO\n" +
		"  source-url: TODO\n" +
		"---\n" +
		"# " + slug + "\n\n" +
		"## When to use\n\n" +
		"TODO — describe the situations this skill should be applied to.\n\n" +
		"## Steps\n\n" +
		"1. TODO\n" +
		"2. TODO\n" +
		"3. TODO\n\n" +
		"## Notes\n\n" +
		"TODO — gotchas, anti-patterns, or links to references/\n"
}

// ValidateResult describes the outcome of a Validate call. Skill
// is populated on success (so callers can show the parsed name +
// description); Err carries the parser's error verbatim on failure
// so the user can see exactly which rule was violated.
type ValidateResult struct {
	Path  string
	Skill Skill
	Err   error
}

// Validate parses the SKILL.md at path against the same rules the
// runtime loader uses, returning the parsed Skill or the parser's
// error. path may be a directory (resolves to <path>/SKILL.md) or
// a SKILL.md file directly. The directory-name match is enforced
// only when path is a directory — pointing at a bare SKILL.md is
// for "is this file syntactically valid" lints in CI.
func Validate(path string) ValidateResult {
	res := ValidateResult{Path: path}
	abs, err := filepath.Abs(path)
	if err != nil {
		res.Err = err
		return res
	}
	info, err := os.Stat(abs)
	if err != nil {
		res.Err = err
		return res
	}
	var data []byte
	var expectedName string
	if info.IsDir() {
		expectedName = filepath.Base(abs)
		data, err = os.ReadFile(filepath.Join(abs, "SKILL.md"))
		if err != nil {
			res.Err = err
			return res
		}
	} else {
		if filepath.Base(abs) != "SKILL.md" {
			res.Err = fmt.Errorf("file source must be a SKILL.md, got %s", filepath.Base(abs))
			return res
		}
		data, err = os.ReadFile(abs)
		if err != nil {
			res.Err = err
			return res
		}
		// File mode: don't enforce dir-name match; user just wants to
		// know "does this file parse." Pass "" to skip the check.
	}
	sk, err := ParseSkillFile(data, expectedName)
	if err != nil {
		res.Err = err
		return res
	}
	res.Skill = sk
	return res
}
