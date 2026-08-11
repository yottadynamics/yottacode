// Package themes is the palette registry the TUI's styles.go reads
// from when (re)building style vars. Each palette spells the seven
// canonical color roles (Accent, Success, Warning, Error, Content,
// Dim, Rule) plus a few legacy slots (Assistant, Code, Warm) that
// styles.go still references during the Phase 4 cleanup.
//
// Palettes use lipgloss.AdaptiveColor pairs so dark/light variants
// of the SAME theme adjust to the terminal's reported background;
// themes that want to pin one side (e.g. "gruvbox" forces the same
// retro look on light terminals too) set both pair fields to the
// same value via the pin() helper.
//
// Adding a theme is three lines: define a `func Foo() Palette`,
// register it in init(), and document it in docs/themes.md.
package themes

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

// Palette is the full color surface a theme needs to fill. Names
// mirror the canonical roles in internal/tui/styles.go; see that
// file's header for what each role drives visually.
//
// Highlight selects the chroma syntax-highlighting style (e.g.
// "monokai", "github", "bw"). Empty falls back to the styles.go
// default ("monokai"). Carries through so a low-saturation theme
// can pair its UI palette with a matching chroma style (e.g.
// "low-contrast" + "github", "gruvbox" + "gruvbox").
//
// Background is an optional surface tint applied to a few "framed"
// areas — code blocks, the input row, the palette overlay box. Most
// themes leave it zero (HasBackground=false) so the terminal's own
// background shows through; the "dimmed" theme uses it to paint a
// slate GitHub-Dark-Dimmed backdrop on chrome surfaces.
//
// Description is a one-line human-readable summary surfaced by
// /themes list and the preview header.
type Palette struct {
	Name        string
	Description string
	Highlight   string

	Accent    compat.AdaptiveColor
	Success   compat.AdaptiveColor
	Warning   compat.AdaptiveColor
	Error     compat.AdaptiveColor
	Content   compat.AdaptiveColor
	Dim       compat.AdaptiveColor
	Rule      compat.AdaptiveColor
	Assistant compat.AdaptiveColor
	Code      compat.AdaptiveColor
	Warm      compat.AdaptiveColor

	HasBackground bool
	Background    compat.AdaptiveColor

	// Monochrome marks a theme that wants every surface to render as
	// plain default-foreground text — currently only "no-color". Read
	// by markdown.go so glamour's rendering of finalized assistant
	// messages honors the same "no color at all" contract the theme
	// already gets for chroma code-block highlighting (via Highlight:
	// "bw") and every other role (via the flat ANSI-7 AdaptiveColors
	// above). Zero value (false) is correct for every other theme.
	Monochrome bool
}

// registry holds every built-in palette, keyed by name. Population
// happens in each palette's init() so adding a new theme file
// requires no edit here.
var registry = map[string]func() Palette{}

// DefaultName is the palette used when config.toml's [theme] section
// is empty or the named theme is missing. "terminal" is the universal
// safe pick — its AdaptiveColor pairs respect whatever background
// the user's terminal reports, so it boots cleanly on dark and light
// terminals alike.
const DefaultName = "terminal"

// pin returns an AdaptiveColor whose light and dark values are the
// same — used by themes that intentionally don't react to the
// terminal's background detection. Every fixed-palette theme
// (gruvbox, nord, catppuccin, …) builds its roles through this.
func pin(c string) compat.AdaptiveColor {
	return compat.AdaptiveColor{Light: lipgloss.Color(c), Dark: lipgloss.Color(c)}
}

// register installs a palette factory under its declared Name. Panics
// on duplicate registration so a refactor that accidentally double-
// registers a theme is caught at first load instead of silently
// shadowing.
func register(p Palette) {
	if _, dup := registry[p.Name]; dup {
		panic("themes: duplicate registration: " + p.Name)
	}
	registry[p.Name] = func() Palette { return p }
}

// Get returns the palette for a name, or (zero, false) when the name
// is unknown. Callers should handle the false case explicitly — the
// TUI's ApplyTheme falls back to DefaultName so an unknown config
// value doesn't fail the launch.
func Get(name string) (Palette, bool) {
	f, ok := registry[name]
	if !ok {
		return Palette{}, false
	}
	return f(), true
}

// MustGet returns the named palette, falling back to the default
// when the name is unknown. Used by ApplyTheme at startup so a
// stale config.toml entry can't block the user from launching.
func MustGet(name string) Palette {
	if p, ok := Get(name); ok {
		return p
	}
	return registry[DefaultName]()
}

// Names returns every registered theme in display order: a small
// hand-curated head followed by alphabetical for the rest. The head
// pins themes the user most often wants to reach first:
//
//  1. terminal — the universal "match my terminal" choice, works
//     on every background; the safest pick when you don't know
//     what to choose, and the default.
	//  2. yottacode-dark — the polished house dark theme users should see
	//     before the larger ecosystem-inspired tail.
//
// Anything else falls into the alphabetical tail so the order stays
// predictable across adding new palettes. Registry map iteration in
// Go is intentionally nondeterministic, so this function is also
// where we lock the order — never rely on map traversal directly.
func Names() []string {
	head := []string{"terminal", "yottacode-dark"}
	headSet := make(map[string]struct{}, len(head))
	for _, n := range head {
		headSet[n] = struct{}{}
	}

	tail := make([]string, 0, len(registry))
	for name := range registry {
		if _, pinned := headSet[name]; pinned {
			continue
		}
		tail = append(tail, name)
	}
	// Hand-rolled insertion sort — tiny N, avoids pulling sort just
	// for the tail.
	for i := 1; i < len(tail); i++ {
		for j := i; j > 0 && tail[j-1] > tail[j]; j-- {
			tail[j-1], tail[j] = tail[j], tail[j-1]
		}
	}

	// Filter head to entries that are actually registered — protects
	// against typos in the hand-curated list silently producing
	// duplicate entries in the tail.
	out := make([]string, 0, len(registry))
	for _, n := range head {
		if _, ok := registry[n]; ok {
			out = append(out, n)
		}
	}
	return append(out, tail...)
}

// IsValid reports whether name is a registered theme. Used by
// config.Validate so a `[theme] name = "typo"` fails at load time
// with a precise error instead of silently degrading to the default.
func IsValid(name string) bool {
	_, ok := registry[name]
	return ok
}
