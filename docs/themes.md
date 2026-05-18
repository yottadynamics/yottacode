# Themes

The yottacode TUI ships with eleven built-in color palettes. Switch between them with `/theme` — an interactive picker with a live preview pane — or pin one in `~/.yottacode/config.toml` so every new session boots into it.

## The picker

`/theme` opens a two-pane overlay. Theme names live on the left; the right pane shows a live showcase rendered in the **highlighted** palette — inline tokens, a chroma-highlighted code block, a diff fragment, and status / state markers.

| Key | Effect |
|---|---|
| `↑` / `↓` (also `j` / `k`) | Move the cursor; the highlighted theme is live-applied to the whole TUI so you see the new look across the entire surface, not just the preview pane. |
| `Home` / `End` | Jump to the first / last theme. |
| `Enter` | Commit the highlighted theme and persist it to `~/.yottacode/config.toml`. |
| `Esc` | Cancel — revert to the theme that was active when the picker opened. Navigation is non-destructive. |

The cursor opens on the currently-active theme. The green `❯` arrow stays green across every palette so the "you are here" cue doesn't shift color when you preview a theme.

## Scriptable shortcuts

For muscle memory or non-interactive callers (e.g. one-shot mode), two forms bypass the picker:

```
/theme set <name>      # explicit
/theme <name>          # positional shortcut, same effect
```

Both apply, persist, and report `[theme] switched to "<name>" (persisted)`.

## Built-in palettes

Order matches what the picker displays: `terminal` leads (the universal-safe and default pick), then alphabetical.

| Name | Description | Chroma style |
|---|---|---|
| `terminal` (default) | The main-branch look — adaptive colors that respect your terminal background. Foreground colors are spelled as `AdaptiveColor` light/dark pairs (dark text on a light terminal, light text on a dark terminal); the ✓ success dot stays on ANSI green (2/10) so it matches your terminal palette exactly. The "what yottacode looked like before themes" theme. | terminal default (`monokai`) |
| `catppuccin` | Catppuccin Mocha — pastel lavender, peach, and teal on warm dark. The most-requested community theme of the last few years. | `catppuccin-mocha` |
| `dimmed` | GitHub Dark Dimmed — soft graphite slate (`#22272e`) with muted blue/green accents on a `#adbac7` foreground. GitHub's own "easier on the eyes" alternative to full dark mode. The backdrop paints chrome surfaces (palette overlay, approval modal, watermark box) so the theme identity is visible; fenced code blocks intentionally **do not** pick up the backdrop, so chroma syntax highlighting stays clean. | `github-dark` |
| `gruvbox` | Warm retro — burnt orange, mustard yellow, and sage green on dark brown. The popular Vim colorscheme. | `gruvbox` |
| `high-contrast` | Maximum legibility — bright primaries on a transparent background, no mid-greys. Designed for low-vision and bright-room terminals. | `github-high-contrast` |
| `low-contrast` | The deliberate inverse of `high-contrast` — every role pulled toward mid-grey so the entire surface sits in a narrow tonal band and nothing pops. State colors carry a faint hue cast (cool / green / amber / rose) but stay desaturated and tonally close to body text. A "calm" reading surface for long ambient sessions; not the right pick if you need errors to be loud. | `github` |
| `no-color` | Monochrome — every role renders as default terminal foreground. Even fenced code blocks render in B&W. Useful for piping to tools that don't strip ANSI, or for users who prefer a flat surface. | `bw` |
| `nord` | Arctic Ice Studio's cool blue-grey — restrained northern tones, minimal saturation. | `nord` |
| `one-dark` | Atom's classic slate with cool blue, green, and a distinctive coral red. The "default dark" muscle memory for a large slice of devs. | `onedark` |
| `solarized-dark` | Ethan Schoonover's classic — deliberate L*a*b* tones, blue base, amber/violet accents. | `solarized-dark` |
| `tokyo-night` | Deep navy with vivid blue, purple, and cyan. More saturated than Nord while staying cool; very popular in the Neovim community. | `tokyonight-night` |

## Why there's no true "light" theme

yottacode is an **inline** TUI — it preserves your terminal scrollback rather than taking over an alternate screen. lipgloss styles only paint behind text they render, not behind the whole viewport, so a theme can't make a dark terminal look like a light one. The `dimmed` theme works around this for chrome surfaces (palette overlay, approval modal) only.

If you want the full light experience, run yottacode in a terminal whose background is light. For "softer than dark, regardless of terminal," use `low-contrast`.

## Persistence

The picker's Enter (or `/theme set <name>`) writes:

```toml
[theme]
name = "dimmed"
```

to `~/.yottacode/config.toml`. The default theme (`terminal`) is *not* persisted — omitting the section keeps the file minimal for users who never touched the command.

A typo in the persisted `name` is rejected at load time:

```
config: ~/.yottacode/config.toml: theme.name = "termnal" is not a registered theme (try one of terminal, catppuccin, dimmed, gruvbox, high-contrast, low-contrast, no-color, nord, one-dark, solarized-dark, tokyo-night)
```

## Adding a theme (for contributors)

Drop an `internal/tui/themes/<name>.go` file with a single `init()` that calls `register(Palette{ Name: …, Description: …, Highlight: …, Accent: …, …})`. The TUI's `styles.go` builds every UI style from those role colors; nothing else needs editing for the new theme to surface in the picker. Tests in `themes_test.go` assert the registered set — append the new name to `expectedThemes` in the right slot (head pins `terminal` first; the tail is alphabetical) so the registry-coverage test stays accurate.

Lock the theme contract:

- **Every role must be filled.** `styles.go` reads all ten role fields; an unset `lipgloss.AdaptiveColor{}` renders as default-foreground (effectively invisible against the terminal background) and looks like a bug.
- **`HasBackground = true` is opt-in.** Only set it when you want chrome surfaces (palette overlay, approval modal, watermark box) painted with your `Background` color. Code blocks are intentionally exempted to preserve chroma syntax highlighting; most themes leave `HasBackground` zero so the user's terminal background shows through.
- **`Highlight` is the chroma style name.** Pair it deliberately — a light UI palette with `monokai` reads as broken; a dark UI with `github` washes out fenced code.
- **Pin vs. adapt.** Use `pin(...)` when the theme's identity is "this exact look on every terminal" (Catppuccin, Gruvbox, Solarized). Use `lipgloss.AdaptiveColor{Light: …, Dark: …}` when the theme should respond to terminal background detection (`low-contrast`).
