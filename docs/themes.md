# Themes

The yottacode TUI ships with twelve built-in color palettes. Switch between them with `/theme` — an interactive picker with a live preview pane — or pin one in `~/.yottacode/config.toml` so every new session boots into it.

## The picker

`/theme` opens a two-pane overlay. Theme names live on the left; the right pane shows a live showcase rendered in the **highlighted** palette — inline tokens, a chroma-highlighted code block, a diff fragment, and status / state markers.

| Key | Effect |
|---|---|
| `↑` / `↓` (also `j` / `k`) | Move the cursor; the highlighted theme is live-applied to the whole TUI — including your terminal's real background color, for themes that set one (see below) — so you see the new look across the entire surface, not just the preview pane. |
| `Home` / `End` | Jump to the first / last theme. |
| `Enter` | Commit the highlighted theme and persist it to `~/.yottacode/config.toml`. |
| `Esc` | Cancel — revert to the theme that was active when the picker opened, real terminal background included. Navigation is non-destructive. |

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

A theme marked **real bg** repaints your terminal emulator's actual background color (not just in-app chrome) the moment you preview or apply it — see [Real terminal background](#real-terminal-background) below.

| Name | Description | Chroma style |
|---|---|---|
| `terminal` (default) | The main-branch look — adaptive colors that respect your terminal background. Foreground colors are spelled as `AdaptiveColor` light/dark pairs (dark text on a light terminal, light text on a dark terminal); the ✓ success dot stays on ANSI green (2/10) so it matches your terminal palette exactly. The "what yottacode looked like before themes" theme. Never touches your real terminal background — that's the point of this one. | terminal default (`monokai`) |
| `catppuccin` **(real bg)** | Catppuccin Mocha — pastel lavender, peach, and teal on warm dark (`#1e1e2e`). The most-requested community theme of the last few years. | `catppuccin-mocha` |
| `dimmed` **(real bg)** | GitHub Dark Dimmed — soft graphite slate (`#22272e`) with muted blue/green accents on a `#adbac7` foreground. GitHub's own "easier on the eyes" alternative to full dark mode. | `github-dark` |
| `gruvbox` **(real bg)** | Warm retro — burnt orange, mustard yellow, and sage green on dark brown (`#282828`). The popular Vim colorscheme. | `gruvbox` |
| `high-contrast` | Maximum legibility — bright primaries on a transparent background, no mid-greys. Designed for low-vision and bright-room terminals; deliberately leaves your real terminal background alone. | `github-high-contrast` |
| `low-contrast` | The deliberate inverse of `high-contrast` — every role pulled toward mid-grey so the entire surface sits in a narrow tonal band and nothing pops. State colors carry a faint hue cast (cool / green / amber / rose) but stay desaturated and tonally close to body text. A "calm" reading surface for long ambient sessions; not the right pick if you need errors to be loud. Adapts to your real terminal background rather than replacing it. | `github` |
| `no-color` | Monochrome — every role renders as default terminal foreground. Even fenced code blocks render in B&W. Useful for piping to tools that don't strip ANSI, or for users who prefer a flat surface. Auto-selected when the [`NO_COLOR`](https://no-color.org/) environment variable is set (non-empty) and no `[theme]` is configured — an explicit config theme always wins, per the convention. Never touches your real terminal background. | `bw` |
| `nord` **(real bg)** | Arctic Ice Studio's cool blue-grey — restrained northern tones, minimal saturation (`#2e3440`). | `nord` |
| `one-dark` **(real bg)** | Atom's classic slate with cool blue, green, and a distinctive coral red (`#282c34`). The "default dark" muscle memory for a large slice of devs. | `onedark` |
| `solarized-dark` **(real bg)** | Ethan Schoonover's classic — deliberate L*a*b* tones, blue base, amber/violet accents (`#002b36`). | `solarized-dark` |
| `studio-dark` **(real bg)** | Recording-first studio palette — near-black charcoal backdrop, crisp off-white text, punchy yottacode green accents, brighter metadata, and non-neon warning/error colors tuned to stay readable after screenshare and YouTube/X compression. Best paired with a dark terminal profile when recording demos. | `github-dark` |
| `tokyo-night` **(real bg)** | Deep navy with vivid blue, purple, and cyan (`#1a1b26`). More saturated than Nord while staying cool; very popular in the Neovim community. | `tokyonight-night` |

## Real terminal background

Themes marked **real bg** above repaint your terminal emulator's actual background color via the [OSC 11](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html) control sequence — not just yottacode's own chrome surfaces, the whole terminal canvas, exactly as if you'd changed your terminal profile's background color yourself. This only became possible once the TUI moved to full-screen (alt-screen) rendering — see "Why there's no true light theme" below for the constraint it lifted.

What to expect:

- **Live preview, live revert.** Browsing the picker repaints your real terminal background on every cursor move, same as every other role; `Esc` puts it back exactly as it was.
- **Restored on exit, always.** yottacode captures your terminal's original background at startup and writes it back before handing the terminal back to your shell — on a normal quit, on Ctrl+C, and on a crash (anywhere the process gets a chance to run its shutdown path; `kill -9` is the one exception nothing can defer through, same as any other cleanup in the app).
- **Silently skipped on unsupported terminals.** yottacode queries your terminal for OSC 11 support once at startup. If nothing answers — some SSH hops without escape-sequence passthrough, tmux without `set -g allow-passthrough on`, a handful of older or headless terminals — the whole feature is a no-op for that session: themes still switch every other color, they just don't touch the real background. Nothing is retried or reported as an error.
- **Themes without "real bg"** (`terminal`, `high-contrast`, `low-contrast`, `no-color`) intentionally leave your terminal's background exactly as you have it configured — that's the contract those themes are built around, not an oversight.

## Why there's no true "light" theme

Foreground colors alone can't fake a light theme on a dark terminal — `AdaptiveColor` picks between a light-mode and dark-mode variant, but every role still has to render *something*, and text tuned for a dark backdrop reads poorly no matter which variant you pick if the canvas underneath stays dark. Real terminal-background theming (above) closes part of this gap for the themes that opt in, but none of the twelve palettes here currently ship a light-mode background — the closest is `low-contrast`, which stays close to body text without replacing your terminal's own background.

If you want the full light experience, run yottacode in a terminal whose background is light, or pin a themed background to a light hex color if you maintain your own theme (see "Adding a theme" below).

## Persistence

The picker's Enter (or `/theme set <name>`) writes:

```toml
[theme]
name = "dimmed"
```

to `~/.yottacode/config.toml`. The default theme (`terminal`) is *not* persisted — omitting the section keeps the file minimal for users who never touched the command.

A typo in the persisted `name` is rejected at load time:

```
config: ~/.yottacode/config.toml: theme.name = "termnal" is not a registered theme (try one of terminal, catppuccin, dimmed, gruvbox, high-contrast, low-contrast, no-color, nord, one-dark, solarized-dark, studio-dark, tokyo-night)
```

## Adding a theme (for contributors)

Drop an `internal/tui/themes/<name>.go` file with a single `init()` that calls `register(Palette{ Name: …, Description: …, Highlight: …, Accent: …, …})`. The TUI's `styles.go` builds every UI style from those role colors; nothing else needs editing for the new theme to surface in the picker. Tests in `themes_test.go` assert the registered set — append the new name to `expectedThemes` in the right slot (head pins `terminal` first; the tail is alphabetical) so the registry-coverage test stays accurate.

Lock the theme contract:

- **Every role must be filled.** `styles.go` reads all ten role fields; an unset `lipgloss.AdaptiveColor{}` renders as default-foreground (effectively invisible against the terminal background) and looks like a bug.
- **`HasBackground = true` is opt-in, and does two things.** In-app chrome surfaces (palette overlay, approval modal, watermark box) get painted with your `Background` color — code blocks are intentionally exempted to preserve chroma syntax highlighting. And, when the terminal supports it (see [Real terminal background](#real-terminal-background)), the theme repaints the user's *actual* terminal background to the same color via OSC 11 — automatic, no separate flag. Only opt in for themes with a real, canonical background swatch (a named color scheme like Nord or Solarized); themes meant to adapt to whatever the user's terminal already looks like should leave `HasBackground` zero.
- **`Highlight` is the chroma style name.** Pair it deliberately — a light UI palette with `monokai` reads as broken; a dark UI with `github` washes out fenced code.
- **Pin vs. adapt.** Use `pin(...)` when the theme's identity is "this exact look on every terminal" (Catppuccin, Gruvbox, Solarized). Use `lipgloss.AdaptiveColor{Light: …, Dark: …}` when the theme should respond to terminal background detection (`low-contrast`).
