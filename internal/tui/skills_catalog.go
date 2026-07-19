package tui

import "github.com/yottadynamics/yottacode/internal/skills"

// loadOfficialSkillsCatalog is a package seam for tests. Production uses the
// public yottacode-skills GitHub repository; TUI unit tests replace it with a
// local stub so catalog navigation never depends on the network.
var loadOfficialSkillsCatalog = func() ([]skills.Skill, error) {
	return skills.ListOfficialCatalog(skills.OfficialCatalogOptions{})
}
