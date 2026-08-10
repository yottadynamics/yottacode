package tui

import "strings"

// hitKind distinguishes what a recorded screen region resolves to.
type hitKind int

const (
	hitItem   hitKind = iota // whole-row: any column on Row selects Index
	hitTab                   // column-bounded: only [ColStart,ColEnd) selects Index
	hitHotkey                // column-bounded (or whole-row when ColEnd==0): selects Key
)

// hitRegion is one recorded row/column -> item/tab/hotkey mapping.
type hitRegion struct {
	Row              int
	ColStart, ColEnd int // meaningful for hitTab/hitHotkey only; ignored for hitItem
	Kind             hitKind
	Index            int    // meaningful for hitItem/hitTab
	Key              string // meaningful for hitHotkey
}

// pickerHits accumulates row/column -> item/tab/hotkey mappings for ONE
// render of ONE picker/panel body. Built fresh by each render function
// that's given a non-nil accumulator; nil-safe throughout (every method
// is a no-op on a nil *pickerHits) so the ~30 existing call sites —
// tests, snapshot renders — that don't pass one need zero changes and
// pay zero cost.
//
// Rebuilt by re-invoking the render function at click time rather than
// cached from the last frame — matches this codebase's established
// "recompute, don't cache" philosophy (resizeTranscriptViewport,
// popupWidth) and sidesteps stale-geometry bugs (e.g. a spinner tick
// changing a "loading…" row between render and click).
type pickerHits struct {
	regions []hitRegion
}

// row registers a whole-row item hit at body-relative row `row` — any
// column on that row resolves to `index`.
func (h *pickerHits) row(row, index int) {
	if h == nil {
		return
	}
	h.regions = append(h.regions, hitRegion{Row: row, Kind: hitItem, Index: index})
}

// span registers a column-bounded tab hit — multiple tabs can share one
// row (renderProviderTabStrip, renderSkillsTabs).
func (h *pickerHits) span(row, colStart, colEnd, index int) {
	if h == nil {
		return
	}
	h.regions = append(h.regions, hitRegion{Row: row, ColStart: colStart, ColEnd: colEnd, Kind: hitTab, Index: index})
}

// hotkey registers a whole-row hotkey hit (e.g. one row of an approval
// modal's "[Y] yes" grid) that resolves to the same key string the
// existing keyboard switch already matches on.
func (h *pickerHits) hotkey(row int, key string) {
	if h == nil {
		return
	}
	h.regions = append(h.regions, hitRegion{Row: row, Kind: hitHotkey, Key: key})
}

// hotkeySpan registers a column-bounded hotkey hit — for rows that pack
// more than one hotkey ("[Y] yes      [N] no"), so a click resolves to
// whichever bracket it's actually over, not whichever one was recorded
// last for that row.
func (h *pickerHits) hotkeySpan(row, colStart, colEnd int, key string) {
	if h == nil {
		return
	}
	h.regions = append(h.regions, hitRegion{Row: row, ColStart: colStart, ColEnd: colEnd, Kind: hitHotkey, Key: key})
}

// resolve finds which registered region (if any) contains (row, col).
// For hitItem any column on the matching row hits; for hitTab and a
// column-bounded hitHotkey (ColEnd != 0) the column must additionally
// fall inside [ColStart,ColEnd) — a whole-row hitHotkey (ColEnd == 0)
// matches any column, same as hitItem.
func (h *pickerHits) resolve(row, col int) (kind hitKind, index int, key string, ok bool) {
	if h == nil {
		return 0, 0, "", false
	}
	for _, r := range h.regions {
		if r.Row != row {
			continue
		}
		bounded := r.Kind == hitTab || (r.Kind == hitHotkey && r.ColEnd != 0)
		if bounded && (col < r.ColStart || col >= r.ColEnd) {
			continue
		}
		return r.Kind, r.Index, r.Key, true
	}
	return 0, 0, "", false
}

// registerBracketHotkeys scans a plain (ANSI-stripped) rendered line for
// "[X]" bracket-hotkey tokens and registers a column-bounded hitHotkey
// region for each — from the token's own start to the next token's
// start on that line, or end of line for the last one. Shared by every
// hotkey-grid card (approval modal, path-trust modal, plan-approval
// cards) so multiple hotkeys packed onto one row ("[Y] yes    [N] no")
// each get their own click target instead of the whole row resolving to
// just one of them.
func registerBracketHotkeys(h *pickerHits, row int, plainLine string) {
	if h == nil {
		return
	}
	var starts []int
	var keys []string
	for i := 0; i+2 < len(plainLine); i++ {
		if plainLine[i] == '[' && plainLine[i+2] == ']' {
			c := plainLine[i+1]
			if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
				starts = append(starts, i)
				keys = append(keys, strings.ToLower(string(c)))
			}
		}
	}
	for i, start := range starts {
		end := len(plainLine)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		h.hotkeySpan(row, start, end, keys[i])
	}
}
