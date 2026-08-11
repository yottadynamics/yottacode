package themes

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
)

// colorEquals compares a color.Color (as stored on an AdaptiveColor
// after the v2 migration moved Light/Dark from raw hex strings to
// color.Color values) against an expected hex/ANSI spec string by
// resolving both through RGBA. Needed because color.Color is an
// interface — string literals can no longer be compared directly.
func colorEquals(c color.Color, want string) bool {
	if c == nil {
		return want == ""
	}
	wr, wg, wb, wa := lipgloss.Color(want).RGBA()
	cr, cg, cb, ca := c.RGBA()
	return wr == cr && wg == cg && wb == cb && wa == ca
}

// sameColor reports whether two color.Color values resolve to the
// same RGBA — used to detect an AdaptiveColor pinned to one value
// (Light == Dark).
func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

// expectedThemes is the registered set this binary ships, in the
// canonical display order Names() returns: head (terminal and
// yottacode-dark) then the alphabetical tail. Lock it in a test so a
// future palette refactor that accidentally drops a theme or reshuffles
// the head surfaces loudly instead of silently shipping the wrong list.
// Add a new theme by inserting here in the correct slot AND in init() of
// the corresponding file.
var expectedThemes = []string{
	// Head: universal default pinned to the top.
	"terminal",
	// Head: yotta's polished dark theme stays one slot below the default.
	"yottacode-dark",
	// Tail: alphabetical after the curated head.
	"catppuccin",
	"dimmed",
	"gruvbox",
	"high-contrast",
	"low-contrast",
	"no-color",
	"nord",
	"one-dark",
	"solarized-dark",
	"studio-dark",
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
	if dark == nil || colorEquals(dark, "#000000") {
		t.Errorf("dimmed.Background is nil or pure black — should be a shade away from terminal black")
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
		light, dark color.Color
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
		if v.light == nil || v.dark == nil {
			t.Errorf("terminal.%s missing one side: light=%v dark=%v", role, v.light, v.dark)
		}
		if sameColor(v.light, v.dark) {
			t.Errorf("terminal.%s pinned (light == dark); defeats AdaptiveColor", role)
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
		name  string
		value color.Color
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
		if !sameColor(r.value, first) {
			t.Errorf("no-color.%s does not match Accent (every role must collapse)", r.name)
		}
	}
}

func TestStudioDark_PaintsRecordingBackdrop(t *testing.T) {
	p, ok := Get("studio-dark")
	if !ok {
		t.Fatalf("studio-dark theme not registered")
	}
	if !p.HasBackground {
		t.Errorf("studio-dark.HasBackground = false, want true — recording chrome needs the charcoal backdrop")
	}
	if !colorEquals(p.Background.Dark, "#0b0f0e") || !colorEquals(p.Background.Light, "#0b0f0e") {
		t.Errorf("studio-dark.Background != pinned #0b0f0e")
	}
	if !colorEquals(p.Accent.Dark, "#00ff66") {
		t.Errorf("studio-dark.Accent.Dark != punchy yottacode green #00ff66")
	}
	if !colorEquals(p.Content.Dark, "#f2fff7") {
		t.Errorf("studio-dark.Content.Dark != crisp off-white #f2fff7")
	}
	if !colorEquals(p.Rule.Dark, "#008f4a") {
		t.Errorf("studio-dark.Rule.Dark != balanced recording border green #008f4a")
	}
}

func TestYottaDark_PaintsYottaNightBackdrop(t *testing.T) {
	p, ok := Get("yottacode-dark")
	if !ok {
		t.Fatalf("yottacode-dark theme not registered")
	}
	if !p.HasBackground {
		t.Errorf("yottacode-dark.HasBackground = false, want true — the theme needs its charcoal backdrop")
	}
	if !colorEquals(p.Background.Dark, "#121212") || !colorEquals(p.Background.Light, "#121212") {
		t.Errorf("yottacode-dark.Background != pinned charcoal #121212")
	}
	if !colorEquals(p.Content.Dark, "#e8e8e8") {
		t.Errorf("yottacode-dark.Content.Dark != bright primary text #e8e8e8")
	}
	if !colorEquals(p.Dim.Dark, "#6a6a6a") {
		t.Errorf("yottacode-dark.Dim.Dark != neutral muted gray #6a6a6a")
	}
	if !colorEquals(p.Rule.Dark, "#1e1e1e") {
		t.Errorf("yottacode-dark.Rule.Dark != subtle elevation #1e1e1e")
	}
	if !colorEquals(p.Accent.Dark, "#bb9af7") {
		t.Errorf("yottacode-dark.Accent.Dark != Grok magenta #bb9af7")
	}
	if !colorEquals(p.Assistant.Dark, "#00e5ff") {
		t.Errorf("yottacode-dark.Assistant.Dark != Yotta signature cyan-teal #00e5ff")
	}
	if !colorEquals(p.Success.Dark, "#9ece6a") {
		t.Errorf("yottacode-dark.Success.Dark != Tokyo Night green #9ece6a")
	}
	if !colorEquals(p.Error.Dark, "#f7768e") {
		t.Errorf("yottacode-dark.Error.Dark != Tokyo Night red #f7768e")
	}
	if !colorEquals(p.Warm.Dark, "#ff9e64") {
		t.Errorf("yottacode-dark.Warm.Dark != Tokyo Night orange #ff9e64")
	}
}

// DefaultName must point at a registered theme — otherwise MustGet
// would recurse / panic / return a zero palette.
func TestDefaultName_IsRegistered(t *testing.T) {
	if !IsValid(DefaultName) {
		t.Errorf("DefaultName = %q is not registered", DefaultName)
	}
}
