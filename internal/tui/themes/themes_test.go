package themes

import (
	"strings"
	"testing"
)

// expectedThemes is the registered set this binary ships, in the
// canonical display order Names() returns: head (terminal) then the
// alphabetical tail. Lock it in a test so a future palette refactor
// that accidentally drops a theme or reshuffles the head surfaces
// loudly instead of silently shipping the wrong list. Add a new
// theme by inserting here in the correct slot AND in init() of the
// corresponding file.
var expectedThemes = []string{
	// Head: universal default pinned to the top.
	"terminal",
	// Tail: alphabetical.
	"catppuccin",
	"dimmed",
	"gruvbox",
	"high-contrast",
	"low-contrast",
	"no-color",
	"nord",
	"one-dark",
	"solarized-dark",
	"tokyo-night",
}

func TestNames_ReturnsAllRegisteredInDisplayOrder(t *testing.T) {
	got := Names()
	if len(got) != len(expectedThemes) {
		t.Fatalf("Names() returned %d themes, want %d (%v)", len(got), len(expectedThemes), got)
	}
	for i, want := range expectedThemes {
		if got[i] != want {
			t.Errorf("Names()[%d] = %q, want %q (full list: %v)", i, got[i], want, got)
		}
	}
}

// terminal must lead the picker — it's the universal "match my
// terminal" theme, the safe pick regardless of bg, and the user
// asked for it to sit on top. Pin the contract so a future
// alphabetical-only sort doesn't silently demote it.
func TestNames_TerminalLeadsTheList(t *testing.T) {
	got := Names()
	if len(got) == 0 || got[0] != "terminal" {
		t.Errorf("Names()[0] = %q, want \"terminal\" (full list: %v)", got, got)
	}
}

func TestGet_AllRegisteredThemesResolve(t *testing.T) {
	for _, name := range expectedThemes {
		t.Run(name, func(t *testing.T) {
			p, ok := Get(name)
			if !ok {
				t.Fatalf("Get(%q) returned ok=false", name)
			}
			if p.Name != name {
				t.Errorf("Get(%q).Name = %q, want %q", name, p.Name, name)
			}
			if p.Description == "" {
				t.Errorf("Get(%q) has empty description — every theme must explain itself", name)
			}
		})
	}
}

func TestGet_UnknownNameReturnsFalse(t *testing.T) {
	if _, ok := Get("definitely-not-a-theme"); ok {
		t.Errorf("Get of unknown theme should return ok=false")
	}
}

func TestMustGet_FallsBackToDefaultOnUnknown(t *testing.T) {
	p := MustGet("definitely-not-a-theme")
	if p.Name != DefaultName {
		t.Errorf("MustGet(unknown).Name = %q, want fallback to %q", p.Name, DefaultName)
	}
}

func TestIsValid_TrueForRegistered(t *testing.T) {
	for _, name := range expectedThemes {
		if !IsValid(name) {
			t.Errorf("IsValid(%q) = false, want true", name)
		}
	}
}

func TestIsValid_FalseForUnknown(t *testing.T) {
	if IsValid("not-a-theme") {
		t.Errorf("IsValid(\"not-a-theme\") = true, want false")
	}
}

// Dimmed is the one theme with a painted backdrop — assert it stays
// that way so a future palette tweak that drops HasBackground
// silently doesn't slip through review.
func TestDimmed_PaintsBackground(t *testing.T) {
	p, ok := Get("dimmed")
	if !ok {
		t.Fatalf("dimmed theme not registered")
	}
	if !p.HasBackground {
		t.Errorf("dimmed.HasBackground = false, want true — the dimmed theme's identity is its slate backdrop")
	}
	// Background should NOT be terminal-black; the whole point is to
	// be "a few shades off" so the user can see the surface.
	dark := p.Background.Dark
	if dark == "" || strings.HasPrefix(dark, "#00") && len(dark) == 7 && dark[3:] == "0000" {
		t.Errorf("dimmed.Background = %q — should be a shade away from terminal black, not pure black", dark)
	}
}

// Terminal theme is the main-branch look — every role spells out a
// light/dark AdaptiveColor pair so foreground flips with the
// terminal's reported background. Pin that contract: any role
// pinned to a single value (Light == Dark) would defeat the
// "respect your terminal" promise this theme exists to deliver.
//
// Exception: Success is allowed to pin to ANSI indices ("2"/"10")
// because those resolve through the user's terminal palette anyway
// — different mechanism, same outcome.
func TestTerminal_RolesAreAdaptive(t *testing.T) {
	p, ok := Get("terminal")
	if !ok {
		t.Fatalf("terminal theme not registered")
	}
	roles := map[string]struct {
		light, dark string
	}{
		"Accent":    {p.Accent.Light, p.Accent.Dark},
		"Warning":   {p.Warning.Light, p.Warning.Dark},
		"Error":     {p.Error.Light, p.Error.Dark},
		"Content":   {p.Content.Light, p.Content.Dark},
		"Dim":       {p.Dim.Light, p.Dim.Dark},
		"Rule":      {p.Rule.Light, p.Rule.Dark},
		"Assistant": {p.Assistant.Light, p.Assistant.Dark},
		"Code":      {p.Code.Light, p.Code.Dark},
		"Warm":      {p.Warm.Light, p.Warm.Dark},
	}
	for role, v := range roles {
		if v.light == "" || v.dark == "" {
			t.Errorf("terminal.%s missing one side: light=%q dark=%q", role, v.light, v.dark)
		}
		if v.light == v.dark {
			t.Errorf("terminal.%s pinned (light == dark == %q); defeats AdaptiveColor", role, v.light)
		}
	}
}

// NoColor must collapse every role to the same value — that's what
// makes it "no color." Splits across roles would defeat the
// monochrome promise.
func TestNoColor_AllRolesSameValue(t *testing.T) {
	p, ok := Get("no-color")
	if !ok {
		t.Fatalf("no-color theme not registered")
	}
	first := p.Accent.Dark
	roles := []struct {
		name, value string
	}{
		{"Success", p.Success.Dark},
		{"Warning", p.Warning.Dark},
		{"Error", p.Error.Dark},
		{"Content", p.Content.Dark},
		{"Dim", p.Dim.Dark},
		{"Rule", p.Rule.Dark},
		{"Assistant", p.Assistant.Dark},
		{"Code", p.Code.Dark},
		{"Warm", p.Warm.Dark},
	}
	for _, r := range roles {
		if r.value != first {
			t.Errorf("no-color.%s = %q, want %q (every role must collapse)", r.name, r.value, first)
		}
	}
}

// DefaultName must point at a registered theme — otherwise MustGet
// would recurse / panic / return a zero palette.
func TestDefaultName_IsRegistered(t *testing.T) {
	if !IsValid(DefaultName) {
		t.Errorf("DefaultName = %q is not registered", DefaultName)
	}
}
