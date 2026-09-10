package skills

import (
	"slices"

	"github.com/yottadynamics/yottacode/internal/config"
)

// EnableDefaultOn adds name to the persisted `[skills] default_on` list in
// config.toml, so it starts enabled in every future session, without
// disturbing any other entries already there. No-op if name is already
// present.
//
// This gives CLI installs (`yottacode skills install`, which has no live
// session to call SkillTool.Enable on) the same "usable immediately" outcome
// the in-TUI `/skills install` slash command gets by calling Enable() right
// after install — otherwise a CLI-installed skill sits loaded but disabled
// until the user separately opens /skills and enables it by hand.
func EnableDefaultOn(name string) error {
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}
	if slices.Contains(cfg.Skills.DefaultOn, name) {
		return nil
	}
	cfg.Skills.DefaultOn = append(cfg.Skills.DefaultOn, name)
	slices.Sort(cfg.Skills.DefaultOn)
	return config.Save(cfg, "")
}
