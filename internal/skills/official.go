package skills

import (
	"fmt"
	"strings"
)

// OfficialSkillsOwner and OfficialSkillsRepo identify the public curated
// skills catalog maintained by YottaDynamics. Paid/private skill packs
// intentionally live in separate authenticated repositories so this public
// source stays simple and redistributable.
const (
	OfficialSkillsOwner = "yottadynamics"
	OfficialSkillsRepo  = "yottacode-skills"
	OfficialPrefix      = "official/"
)

// OfficialSource returns the GitHub shorthand for a skill in the official
// public catalog. The returned string is accepted by the existing GitHub
// Contents installer, so official installs reuse the normal fetch/update path.
func OfficialSource(slug string) (string, error) {
	slug = strings.TrimSpace(slug)
	if strings.HasPrefix(slug, OfficialPrefix) {
		slug = strings.TrimPrefix(slug, OfficialPrefix)
	}
	if !skillNamePattern.MatchString(slug) {
		return "", fmt.Errorf("invalid official skill %q", slug)
	}
	return OfficialSkillsOwner + "/" + OfficialSkillsRepo + "/skills/" + slug, nil
}

// NormalizeOfficialSource expands the public official shortcut into the
// canonical GitHub shorthand. Non-official sources are returned unchanged so
// callers can pass every install source through this helper unconditionally.
func NormalizeOfficialSource(src string) (string, bool, error) {
	trimmed := strings.TrimSpace(src)
	if !strings.HasPrefix(trimmed, OfficialPrefix) {
		return src, false, nil
	}
	expanded, err := OfficialSource(trimmed)
	if err != nil {
		return "", true, err
	}
	return expanded, true, nil
}
