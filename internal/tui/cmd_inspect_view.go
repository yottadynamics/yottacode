package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/yottadynamics/yottacode/internal/catalog"
	"github.com/yottadynamics/yottacode/internal/session"
)

// Layout and threshold constants for the /inspect list view. The widths are
// sized so a turn row fits the popup at its usual width (~90 columns) without
// wrapping.
const (
	inspectToolsColWidth = 30 // tools text before it is truncated with "…"
	inspectFlagColWidth  = 7  // "low-out", "trunc", "filter"
	inspectSparkMax      = 40 // sparkline columns; longer histories are bucketed
	inspectToolRowsMax   = 5  // tool table rows before the "N other tools" row

	// The error section lists at most inspectErrorGroupsMax distinct failures,
	// each with up to inspectErrorTurnsMax turn numbers and a message cut to
	// inspectErrPreviewChars (the detail view has the full text).
	inspectErrorGroupsMax  = 8
	inspectErrorTurnsMax   = 6
	inspectErrPreviewChars = 64

	// inspectDeltaJump is the per-turn input growth the Δin column highlights
	// (a big file read, a subagent result, ...). A fall of inspectCompactDropPct
	// or more from at least inspectCompactMinPrev tokens is treated as a likely
	// compaction. Both are heuristics: the saved compaction events carry no turn
	// index to match against.
	inspectDeltaJump      = 20_000
	inspectCompactDropPct = 30
	inspectCompactMinPrev = 10_000
)

var inspectSparkRunes = []rune("▁▂▃▄▅▆▇█")

// inspectViewState is everything the interactive /inspect panel needs beyond
// the rendered text: the parsed turns, the context window the input column is
// measured against, and the user's cursor/filter/detail selections. It lives
// behind a pointer on Model (like the pickers' state) and is re-rendered into
// Model.inspectPanel on every change. Not safe for concurrent use; only the
// TUI's update goroutine touches it.
type inspectViewState struct {
	turns       []inspectTurnView
	sessionID   string
	summary     string
	window      int
	compactions int

	flagged  bool   // f: only turns with errors/flags
	collapse bool   // c: fold runs of identical turns
	allTools bool   // t: show every tool, not just the top few
	query    string // /: substring over tools + messages

	// Tool table selection, mirroring the turn rows: tab moves focus between
	// the two tables, ↵ on a tool filters the turns to those that called it.
	focusTools bool
	toolCursor int
	toolFilter string
	toolLines  []int    // logical panel line of each rendered tool row
	toolNames  []string // tool name of each rendered tool row

	searching bool // typing into the query
	cursor    int  // index into rows()
	detail    int  // turn index shown in detail view, -1 for the list

	// rowLines[i] is the logical panel line holding rows()[i]; refreshed by
	// renderInspect so scrolling can keep the cursor on screen.
	rowLines []int
}

// inspectRow is one selectable line of the list: a single turn, or (with
// collapse on) a run of identical consecutive turns.
type inspectRow struct {
	idx []int
}

func newInspectView(s *session.Session, window int) *inspectViewState {
	turns := buildInspectTurns(s)
	return &inspectViewState{
		turns:       turns,
		sessionID:   s.ID,
		summary:     inspectSessionSummary(s, turns),
		window:      window,
		compactions: len(s.CompactionEvents),
		detail:      -1,
	}
}

// inspectWindowFor resolves the context window the input column's fill color
// and the peak percentage are measured against, from the first model that
// answered a turn (falling back to the session's own model). Zero means
// unknown, in which case those are shown without a percentage or color.
func (m Model) inspectWindowFor(s *session.Session, turns []inspectTurnView) int {
	if s == nil {
		return 0
	}
	model := s.Model
	for _, t := range turns {
		if t.model != "" {
			model = t.model
			break
		}
	}
	if model == "" {
		return 0
	}
	return catalog.ResolveWindowForProvider(
		m.fileCfg.ProviderKindForModel(model),
		model,
		m.fileCfg.ContextWindowOverride(model),
		m.fileCfg.Context.DefaultWindow,
	)
}

func (t inspectTurnView) errorCount() int { return t.errors }

func (t inspectTurnView) flagged() bool {
	return t.errorCount() > 0 || t.stopFlag != "" || t.lowSignal
}

// inspectSearchText is the lowercased haystack "/" matches against: the user
// and assistant text plus every tool name. Newlines keep a query from matching
// across the seams between fields.
func inspectSearchText(t inspectTurnView) string {
	var b strings.Builder
	b.WriteString(t.user)
	b.WriteByte('\n')
	b.WriteString(t.assistant)
	for _, c := range t.toolCalls {
		b.WriteByte('\n')
		b.WriteString(c.name)
	}
	return strings.ToLower(b.String())
}

// matches reports whether the turn contains q (case-insensitive). q is
// lowercased by the caller once per filter pass, not once per turn.
func (t inspectTurnView) matches(lowerQ string) bool {
	return lowerQ == "" || strings.Contains(t.search, lowerQ)
}

func (t inspectTurnView) callsTool(name string) bool {
	for _, c := range t.toolCalls {
		if c.name == name {
			return true
		}
	}
	return false
}

// inspectTurnFlag is the short, fixed-width-friendly flag for a turn's row:
// a cut-off or filtered response outranks the low-output heuristic. The detail
// view (inspectTurnNote) lists every flag in full.
func inspectTurnFlag(t inspectTurnView) string {
	switch {
	case t.stopFlag == "truncated":
		return "trunc"
	case t.stopFlag == "filtered":
		return "filter"
	case t.lowSignal:
		return "low-out"
	}
	return ""
}

func (t inspectTurnView) toolSignature() string { return t.toolSig }

// visible returns the turn indexes passing the flagged filter and search.
func (vs *inspectViewState) visible() []int {
	var out []int
	lowerQ := strings.ToLower(vs.query)
	for i, t := range vs.turns {
		if vs.flagged && !t.flagged() {
			continue
		}
		if !t.matches(lowerQ) {
			continue
		}
		if vs.toolFilter != "" && !t.callsTool(vs.toolFilter) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// rows groups visible turns into selectable rows. Only consecutive turns with
// the same non-empty tool signature and no errors or flags fold together, so
// collapsing can never hide a failure.
func (vs *inspectViewState) rows() []inspectRow {
	var rows []inspectRow
	for _, i := range vs.visible() {
		t := vs.turns[i]
		if vs.collapse && len(rows) > 0 && !t.flagged() && t.toolSignature() != "" {
			last := &rows[len(rows)-1]
			prev := vs.turns[last.idx[len(last.idx)-1]]
			if last.idx[len(last.idx)-1] == i-1 && !prev.flagged() && prev.toolSignature() == t.toolSignature() {
				last.idx = append(last.idx, i)
				continue
			}
		}
		rows = append(rows, inspectRow{idx: []int{i}})
	}
	return rows
}

// clampCursor keeps the cursor inside the current row list.
func (vs *inspectViewState) clampCursor(n int) {
	vs.cursor = min(max(vs.cursor, 0), max(n-1, 0))
}

// jumpToError moves the cursor to the next (dir>0) or previous (dir<0) row
// containing a tool error, wrapping around. It reports whether one was found.
func (vs *inspectViewState) jumpToError(rows []inspectRow, dir int) bool {
	n := len(rows)
	for step := 1; step <= n; step++ {
		i := ((vs.cursor+dir*step)%n + n) % n
		for _, ti := range rows[i].idx {
			if vs.turns[ti].errorCount() > 0 {
				vs.cursor = i
				return true
			}
		}
	}
	return false
}

// inspectSparkline renders vals as one block-character column each,
// scaled to the largest value. Long histories are bucketed (max per bucket)
// down to inspectSparkMax so the line never wraps.
func inspectSparkline(vals []int64) string {
	if len(vals) > inspectSparkMax {
		bucketed := make([]int64, inspectSparkMax)
		for i, v := range vals {
			j := i * inspectSparkMax / len(vals)
			bucketed[j] = max(bucketed[j], v)
		}
		vals = bucketed
	}
	var top int64
	for _, v := range vals {
		top = max(top, v)
	}
	if top == 0 {
		return ""
	}
	out := make([]rune, len(vals))
	for i, v := range vals {
		idx := int(v * int64(len(inspectSparkRunes)-1) / top)
		out[i] = inspectSparkRunes[idx]
	}
	return string(out)
}

// inspectInputStyled pads text to width and colors it by how full the context
// window is: green below 60%, yellow from 60%, red from 85%. With an unknown
// window it is returned unstyled. Padding happens before styling so ANSI
// escapes don't throw the column alignment off.
func inspectInputStyled(text string, v int64, window, width int) string {
	text = fmt.Sprintf("%*s", width, text)
	if window <= 0 || v <= 0 {
		return text
	}
	pct := float64(v) / float64(window)
	col := colorBrand
	switch {
	case pct >= 0.85:
		col = colorErr
	case pct >= 0.60:
		col = colorWarning
	}
	return lipgloss.NewStyle().Foreground(col).Render(text)
}

// inspectDeltaText formats the change in input tokens from the previous turn:
// "·" below 1K (noise), otherwise a signed figure like "+85K" / "-91K".
// Large jumps are yellow; a drop of inspectCompactDropPct or more (a likely
// compaction) is green.
func inspectDeltaText(prev, cur int64, width int) string {
	d := cur - prev
	var text string
	switch {
	case d > -1000 && d < 1000:
		text = "·"
	case d > 0:
		text = "+" + formatTokens(int(d))
	default:
		text = "-" + formatTokens(int(-d))
	}
	text = fmt.Sprintf("%*s", width, text)
	switch {
	case d >= inspectDeltaJump:
		return lipgloss.NewStyle().Foreground(colorWarning).Render(text)
	case d < 0 && prev >= inspectCompactMinPrev && cur*100 <= prev*(100-inspectCompactDropPct):
		return lipgloss.NewStyle().Foreground(colorBrand).Render(text)
	}
	return text
}

// inspectTurnNote lists every flag on turn ti in words, for the detail view
// (the list rows show only the single highest-priority inspectTurnFlag).
func inspectTurnNote(vs *inspectViewState, ti int) string {
	t := vs.turns[ti]
	var notes []string
	if t.stopFlag != "" {
		notes = append(notes, t.stopFlag)
	}
	if t.lowSignal {
		notes = append(notes, "low output")
	}
	if ti > 0 && t.usage != nil && vs.turns[ti-1].usage != nil {
		prev, cur := vs.turns[ti-1].usage.InputTokens, t.usage.InputTokens
		if prev >= inspectCompactMinPrev && cur*100 <= prev*(100-inspectCompactDropPct) {
			notes = append(notes, fmt.Sprintf("↓ context -%d%%", (prev-cur)*100/prev))
		}
	}
	return strings.Join(notes, ", ")
}

type inspectToolAgg struct {
	name           string
	calls, errs    int
	latSum, latMax int64
	latN           int
}

func inspectToolAggregates(turns []inspectTurnView) []inspectToolAgg {
	byName := map[string]*inspectToolAgg{}
	var order []string
	for _, t := range turns {
		for _, c := range t.toolCalls {
			a, ok := byName[c.name]
			if !ok {
				a = &inspectToolAgg{name: c.name}
				byName[c.name] = a
				order = append(order, c.name)
			}
			a.calls++
			if strings.HasPrefix(c.status, "error") {
				a.errs++
			}
			if c.latencyMS != nil {
				a.latSum += *c.latencyMS
				a.latMax = max(a.latMax, *c.latencyMS)
				a.latN++
			}
		}
	}
	out := make([]inspectToolAgg, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	// Failing tools first so a capped table can't hide them behind busier,
	// healthy ones; then by call count.
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].errs > 0) != (out[j].errs > 0) {
			return out[i].errs > 0
		}
		return out[i].calls > out[j].calls
	})
	return out
}

// inspectToolsToggle is the toolNames sentinel for the table's last row, the
// "N other tools" / "show top N only" row, which expands or collapses the
// table instead of filtering. NUL can't appear in a real tool name.
const inspectToolsToggle = "\x00toggle"

// inspectGutter is the two-column left gutter shared by the tool and turn
// tables: ▸ on the selected row, ● on an active tool filter, and a dim › on
// every other row to show it is selectable (↵). Keyboard-only: the TUI has no
// mouse support. dim is the pre-rendered "› " gutter: styling it per row is
// the hottest part of a redraw on a long session, so renderInspect builds it
// once per pass.
func inspectGutter(dim string, selected, filtered bool) string {
	switch {
	case selected:
		return "▸ "
	case filtered:
		return "● "
	}
	return dim
}

// inspectOtherToolsLine summarizes the tools cut from the capped table, so
// the omitted tail still contributes its call and error totals.
func inspectOtherToolsLine(rest []inspectToolAgg) string {
	calls, errs := 0, 0
	for _, a := range rest {
		calls += a.calls
		errs += a.errs
	}
	return fmt.Sprintf("%d other %s · %d %s · %d %s", len(rest), usagePluralize("tool", len(rest)), calls, usagePluralize("call", calls), errs, usagePluralize("error", errs))
}

// inspectMS formats a latency; sub-second values keep millisecond precision
// so fast tools don't all read as "0s".
func inspectMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return formatDuration(time.Duration(ms) * time.Millisecond)
}

// inspectErrorPreview condenses a tool failure to one line: the "error:" and
// repeated "<tool>:" prefixes carry no information next to the "✗ <tool>"
// label, so they're stripped before truncating.
func inspectErrorPreview(name, full string) string {
	s := strings.TrimSpace(full)
	for {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "error:"), name+":"))
		if trimmed == s {
			break
		}
		s = trimmed
	}
	return truncateForRender(s, inspectErrPreviewChars)
}

// renderInspectPanel renders a session with the default view (no filter,
// nothing collapsed). The interactive panel goes through renderInspect.
func renderInspectPanel(s *session.Session) string {
	if s == nil {
		return styleEmpty.Render("session not found")
	}
	return renderInspect(newInspectView(s, 0))
}

// renderInspect renders the list view (or the detail view when one is open)
// and records each row's line so the cursor can be scrolled into view.
func renderInspect(vs *inspectViewState) string {
	if vs.detail >= 0 && vs.detail < len(vs.turns) {
		return renderInspectDetail(vs, vs.detail)
	}
	label := vs.sessionID
	if len(label) > 8 {
		label = label[:8]
	}
	var b strings.Builder
	line := 0
	w := func(s string) {
		b.WriteString(s)
		b.WriteByte('\n')
		line += strings.Count(s, "\n") + 1
	}
	w(fmt.Sprintf("inspect  session %s", label))
	w(styleMeta.Render(vs.summary))
	vs.rowLines = vs.rowLines[:0]
	dimGutter := styleMeta.Render("›") + " "
	if len(vs.turns) == 0 {
		w("")
		w(styleEmpty.Render("no turns in this session yet"))
		return strings.TrimRight(b.String(), "\n")
	}

	inputs := make([]int64, len(vs.turns))
	cacheVals := make([]int64, len(vs.turns))
	costVals := make([]int64, len(vs.turns))
	var topIn int64
	var costTotal float64
	hasCache, hasCost, multiModel := false, false, false
	for i, t := range vs.turns {
		if t.usage != nil {
			inputs[i] = t.usage.InputTokens
			topIn = max(topIn, t.usage.InputTokens)
			if hit := cacheHitRate(*t.usage); hit >= 0 {
				cacheVals[i] = int64(hit * 1000)
				hasCache = true
			}
			if t.usage.CostAvailable {
				costVals[i] = int64(t.usage.CostUSD * 1e6)
				costTotal += t.usage.CostUSD
				hasCost = true
			}
		}
		if t.model != vs.turns[0].model {
			multiModel = true
		}
	}
	w("")
	if spark := inspectSparkline(inputs); spark != "" {
		extra := "peak " + formatTokens(int(topIn))
		if vs.window > 0 {
			extra += fmt.Sprintf("/%s (%d%%)", formatTokens(vs.window), int(topIn)*100/vs.window)
		}
		w(fmt.Sprintf("%-11s %s  %s", "input/turn", spark, extra))
	}
	if hasCache {
		if spark := inspectSparkline(cacheVals); spark != "" {
			w(fmt.Sprintf("%-11s %s", "cache hit", spark))
		}
	}
	if hasCost {
		if spark := inspectSparkline(costVals); spark != "" {
			w(fmt.Sprintf("%-11s %s  total %s est", "cost/turn", spark, formatUSD(costTotal)))
		}
	}

	if aggs := inspectToolAggregates(vs.turns); len(aggs) > 0 {
		w("")
		w(styleMeta.Render(fmt.Sprintf("  %-26s %5s %4s %7s %7s", "tool", "calls", "err", "avg", "max")))
		vs.toolLines, vs.toolNames = vs.toolLines[:0], vs.toolNames[:0]
		shown := len(aggs)
		if !vs.allTools {
			shown = min(shown, inspectToolRowsMax)
		}
		// A trailing selectable row expands (or collapses) the table when there
		// are more tools than the cap, so it never depends on knowing the t key.
		toggleRow := len(aggs) > inspectToolRowsMax
		rowCount := shown
		if toggleRow {
			rowCount++
		}
		vs.toolCursor = min(max(vs.toolCursor, 0), rowCount-1)
		for i, a := range aggs[:shown] {
			avg, mx := "-", "-"
			if a.latN > 0 {
				avg, mx = inspectMS(a.latSum/int64(a.latN)), inspectMS(a.latMax)
			}
			errs := fmt.Sprintf("%4d", a.errs)
			if a.errs > 0 {
				errs = styleError.Render(errs)
			}
			gutter := inspectGutter(dimGutter, vs.focusTools && i == vs.toolCursor, a.name == vs.toolFilter)
			vs.toolLines = append(vs.toolLines, line)
			vs.toolNames = append(vs.toolNames, a.name)
			w(fmt.Sprintf("%s%-26s %5d %s %7s %7s", gutter, ansi.Truncate(a.name, 26, "…"), a.calls, errs, avg, mx))
		}
		if toggleRow {
			text := inspectOtherToolsLine(aggs[shown:]) + " · ↵ show all"
			if vs.allTools {
				text = fmt.Sprintf("show top %d only", inspectToolRowsMax)
			}
			vs.toolLines = append(vs.toolLines, line)
			vs.toolNames = append(vs.toolNames, inspectToolsToggle)
			w(inspectGutter(dimGutter, vs.focusTools && vs.toolCursor == shown, false) + styleMeta.Render(text))
		}
	}

	rows := vs.rows()
	vs.clampCursor(len(rows))
	w("")
	status := fmt.Sprintf("showing %d of %d turns", len(vs.visible()), len(vs.turns))
	if vs.compactions > 0 {
		status += fmt.Sprintf(" · %d %s", vs.compactions, usagePluralize("compaction", vs.compactions))
	}
	if vs.toolFilter != "" {
		status += " · tool: " + vs.toolFilter
	}
	if vs.flagged {
		status += " · flagged only"
	}
	if vs.collapse {
		status += " · repeats collapsed"
	}
	if vs.searching {
		status += " · search: " + vs.query + "▏"
	} else if vs.query != "" {
		status += " · search: " + vs.query
	}
	w(styleMeta.Render(status))
	costHdr := ""
	if hasCost {
		costHdr = fmt.Sprintf("  %7s", "cost")
	}
	modelHdr := ""
	if multiModel {
		modelHdr = "  model"
	}
	// The turn column is as wide as its longest label, so a collapsed range
	// like "100-120 ×21" doesn't push the other columns out of line.
	turnW := 4
	rowLabel := func(r inspectRow) string {
		first, last := vs.turns[r.idx[0]], vs.turns[r.idx[len(r.idx)-1]]
		if len(r.idx) > 1 {
			return fmt.Sprintf("%d-%d ×%d", first.n, last.n, len(r.idx))
		}
		return fmt.Sprintf("%d", first.n)
	}
	for _, r := range rows {
		turnW = max(turnW, ansi.StringWidth(rowLabel(r)))
	}
	toolsHdr := "tools"
	if multiModel {
		toolsHdr = fmt.Sprintf("%-*s", inspectToolsColWidth, "tools")
	}
	w(styleMeta.Render(fmt.Sprintf("  %*s  %5s  %6s  %6s  %6s  %5s%s  %-*s  %s%s", turnW, "turn", "wall", "in", "Δin", "out", "cache", costHdr, inspectFlagColWidth, "flag", toolsHdr, modelHdr)))
	if len(rows) == 0 {
		w(styleEmpty.Render("  no turns match the current filter"))
	}
	for ri, r := range rows {
		first, last := vs.turns[r.idx[0]], vs.turns[r.idx[len(r.idx)-1]]
		var wallMS, outTok int64
		wallKnown := true
		var peakIn int64
		var costSum float64
		costKnown := false
		for _, ti := range r.idx {
			t := vs.turns[ti]
			if t.wallTimeMS == nil {
				wallKnown = false
			} else {
				wallMS += *t.wallTimeMS
			}
			if t.usage != nil {
				outTok += t.usage.OutputTokens
				peakIn = max(peakIn, t.usage.InputTokens)
				if t.usage.CostAvailable {
					costSum += t.usage.CostUSD
					costKnown = true
				}
			}
		}
		wall, in, out, cache, cost := "-", "-", "-", "-", "-"
		if wallKnown {
			wall = inspectMS(wallMS)
		}
		inCol := fmt.Sprintf("%6s", in)
		deltaCol := fmt.Sprintf("%6s", "-")
		if last.usage != nil {
			in = formatTokens(int(peakIn))
			out = formatTokens(int(outTok))
			inCol = inspectInputStyled(in, peakIn, vs.window, 6)
			if pi := r.idx[0] - 1; pi >= 0 && vs.turns[pi].usage != nil {
				deltaCol = inspectDeltaText(vs.turns[pi].usage.InputTokens, peakIn, 6)
			}
			if hit := cacheHitRate(*last.usage); hit >= 0 {
				cache = fmt.Sprintf("%.0f%%", hit*100)
			}
		}
		if costKnown {
			cost = formatUSD(costSum)
		}
		tools := first.toolSignature()
		if tools == "" {
			tools = "-"
		}
		tools = ansi.Truncate(tools, inspectToolsColWidth-4, "…")
		toolsPad := 0
		if multiModel {
			toolsPad = inspectToolsColWidth
		}
		errs := 0
		for _, ti := range r.idx {
			errs += vs.turns[ti].errorCount()
		}
		errMark := ""
		if errs > 0 {
			errMark = fmt.Sprintf("✗%d", errs)
			tools += " " + errMark
		}
		marker := inspectGutter(dimGutter, !vs.focusTools && ri == vs.cursor, false)
		costCol := ""
		if hasCost {
			costCol = fmt.Sprintf("  %7s", cost)
		}
		// Flag sits in its own short fixed column ahead of the (variable-width)
		// tools text, so it can't be pushed off the right edge and wrapped.
		flag := ""
		if len(r.idx) == 1 {
			flag = inspectTurnFlag(first)
		}
		flagCol := fmt.Sprintf("%-*s", inspectFlagColWidth, flag)
		if flag != "" {
			flagCol = styleHint.Render(flagCol)
		}
		if errMark != "" {
			tools = strings.Replace(tools, errMark, styleError.Render(errMark), 1)
		}
		row := fmt.Sprintf("%s%*s  %5s  %s  %s  %6s  %5s%s  %s  %s", marker, turnW, rowLabel(r), wall, inCol, deltaCol, out, cache, costCol, flagCol, tools)
		if multiModel {
			row += strings.Repeat(" ", max(0, toolsPad-ansi.StringWidth(tools))) + "  " + first.model
		}
		vs.rowLines = append(vs.rowLines, line)
		w(row)
	}
	for _, l := range inspectErrorSection(vs) {
		w(l)
	}
	// Footer is added by windowedInspectPanel so its row is included in the
	// popup's height calculation rather than being clipped after scrolling.
	return strings.TrimRight(b.String(), "\n")
}

// inspectErrorGroup is one distinct tool failure and every visible turn it
// happened in.
type inspectErrorGroup struct {
	tool    string
	preview string
	turns   []int
}

// inspectErrorGroups collapses the visible turns' tool failures by (tool,
// condensed message), in first-seen order, so a failure that repeats across
// turns is listed once with all its turn numbers.
func inspectErrorGroups(vs *inspectViewState) []inspectErrorGroup {
	var groups []inspectErrorGroup
	index := map[string]int{}
	for _, ti := range vs.visible() {
		t := vs.turns[ti]
		for _, c := range t.toolCalls {
			if c.errorText == "" {
				continue
			}
			preview := inspectErrorPreview(c.name, c.errorText)
			key := c.name + "\x00" + preview
			gi, ok := index[key]
			if !ok {
				gi = len(groups)
				index[key] = gi
				groups = append(groups, inspectErrorGroup{tool: c.name, preview: preview})
			}
			if g := &groups[gi]; len(g.turns) == 0 || g.turns[len(g.turns)-1] != t.n {
				g.turns = append(g.turns, t.n)
			}
		}
	}
	return groups
}

// inspectErrorSection renders the grouped failures under the turn table. The
// table rows only carry a ✗N marker, so the rhythm of one row per turn is
// never broken by wrapped error text; the full text is in the detail view.
func inspectErrorSection(vs *inspectViewState) []string {
	groups := inspectErrorGroups(vs)
	if len(groups) == 0 {
		return nil
	}
	total := 0
	for _, g := range groups {
		total += len(g.turns)
	}
	lines := []string{"", styleMeta.Render(fmt.Sprintf("errors · %d %s in %d %s", len(groups), usagePluralize("failure", len(groups)), total, usagePluralize("turn", total)))}
	for i, g := range groups {
		if i == inspectErrorGroupsMax {
			lines = append(lines, styleMeta.Render(fmt.Sprintf("+%d more · press e to step through error turns", len(groups)-inspectErrorGroupsMax)))
			break
		}
		turns := make([]string, 0, inspectErrorTurnsMax)
		for j, n := range g.turns {
			if j == inspectErrorTurnsMax {
				turns = append(turns, fmt.Sprintf("+%d", len(g.turns)-inspectErrorTurnsMax))
				break
			}
			turns = append(turns, fmt.Sprintf("%d", n))
		}
		head := fmt.Sprintf("%s  turn %s", g.tool, strings.Join(turns, ", "))
		lines = append(lines, "  "+styleError.Render("✗ "+head), "    "+styleMeta.Render(g.preview))
	}
	return lines
}

// renderInspectDetail shows one turn untruncated: the request, the reply,
// every tool call with full arguments, latency and failure text, and the
// token breakdown.
func renderInspectDetail(vs *inspectViewState, ti int) string {
	t := vs.turns[ti]
	var b strings.Builder
	fmt.Fprintf(&b, "inspect  turn %d of %d\n", t.n, len(vs.turns))
	var meta []string
	if t.wallTimeMS != nil {
		meta = append(meta, "wall "+inspectMS(*t.wallTimeMS))
	}
	if t.model != "" {
		meta = append(meta, t.model)
	}
	if t.usage != nil {
		meta = append(meta, fmt.Sprintf("%s in · %s out", formatTokens(int(t.usage.InputTokens)), formatTokens(int(t.usage.OutputTokens))))
		if hit := cacheHitRate(*t.usage); hit >= 0 {
			meta = append(meta, fmt.Sprintf("cache %.0f%% hit", hit*100))
		}
		if t.usage.CostAvailable {
			meta = append(meta, formatUSD(t.usage.CostUSD)+" est")
		}
	}
	if len(meta) > 0 {
		b.WriteString(styleMeta.Render(strings.Join(meta, " · ")))
		b.WriteByte('\n')
	}
	if extra := strings.TrimSpace(inspectDetailTokens(t.usage)); extra != "" {
		b.WriteString(styleMeta.Render(extra))
		b.WriteByte('\n')
	}
	if note := inspectTurnNote(vs, ti); note != "" {
		b.WriteString(styleHint.Render("flags: " + note))
		b.WriteByte('\n')
	}
	section := func(title, body string) {
		if strings.TrimSpace(body) == "" {
			return
		}
		b.WriteString("\n")
		b.WriteString(styleMeta.Render(title))
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(body))
		b.WriteString("\n")
	}
	section("user", t.user)
	section("assistant", t.assistant)
	if len(t.toolCalls) > 0 {
		b.WriteString("\n")
		b.WriteString(styleMeta.Render(fmt.Sprintf("tool calls (%d)", len(t.toolCalls))))
		b.WriteString("\n")
	}
	for i, c := range t.toolCalls {
		status := c.status
		if status == "" {
			status = "ok"
		}
		head := fmt.Sprintf("%d. %s · %s", i+1, c.name, status)
		if c.latencyMS != nil {
			head += " · " + inspectMS(*c.latencyMS)
		}
		if strings.HasPrefix(status, "error") {
			head = styleError.Render(head)
		}
		b.WriteString(head)
		b.WriteString("\n")
		if c.args != "" {
			b.WriteString("   args  " + c.args + "\n")
		}
		if c.errorText != "" {
			b.WriteString("   " + styleError.Render(c.errorText) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// rebuildInspectPanel re-renders the panel text from the view state and
// scrolls so the cursor row stays visible.
func (m *Model) rebuildInspectPanel() {
	if m.inspectView == nil {
		return
	}
	m.inspectPanel = renderInspect(m.inspectView)
	if m.inspectView.detail >= 0 {
		m.inspectScrollOffset = min(m.inspectScrollOffset, m.inspectMaxScrollOffset())
		return
	}
	m.inspectEnsureCursorVisible()
}

// inspectVisualLineOf maps a logical panel line to its first wrapped row.
func (m Model) inspectVisualLineOf(logical int) int {
	width := max(m.popupWidth()-2, 1)
	lines := strings.Split(m.inspectPanel, "\n")
	v := 0
	for i := 0; i < logical && i < len(lines); i++ {
		v += strings.Count(ansi.Wrap(lines[i], width, ""), "\n") + 1
	}
	return v
}

func (m *Model) inspectEnsureCursorVisible() {
	vs := m.inspectView
	if vs == nil {
		return
	}
	cursor, lines := vs.cursor, vs.rowLines
	if vs.focusTools {
		cursor, lines = vs.toolCursor, vs.toolLines
	}
	if cursor >= len(lines) {
		return
	}
	line := m.inspectVisualLineOf(lines[cursor])
	visible := m.inspectVisibleLines()
	if cursor == 0 && line < visible-1 {
		// First row with the header above it fits on screen: show both.
		m.inspectScrollOffset = 0
		return
	}
	if line < m.inspectScrollOffset {
		m.inspectScrollOffset = line
	} else if line >= m.inspectScrollOffset+visible-1 {
		m.inspectScrollOffset = line - visible + 2
	}
	m.inspectScrollOffset = min(max(m.inspectScrollOffset, 0), m.inspectMaxScrollOffset())
}

func (m Model) inspectFooter() string {
	vs := m.inspectView
	switch {
	case vs == nil:
		return "↑↓ scroll · PgUp/PgDn · Home/End · Esc close"
	case vs.searching:
		return "type to search · ↵ keep · esc clear"
	case vs.detail >= 0:
		return "←→ prev/next turn · ↑↓ scroll · esc back · q close"
	default:
		if vs.focusTools {
			return "↑↓ select · ↵ filter turns by tool (or expand the list) · tab/esc back to turns"
		}
		return "↑↓ select · ↵ detail · e/E error · f flagged · c collapse · tab tools · / search · esc close"
	}
}

// inspectHintKeys is the short key reminder shown on the scroll-position line
// while there is more to scroll, so the keys are discoverable without having
// to reach the full footer at the bottom.
func (m Model) inspectHintKeys() string {
	vs := m.inspectView
	switch {
	case vs == nil || vs.searching:
		return ""
	case vs.detail >= 0:
		return " · ←→ turn · esc back"
	default:
		if vs.focusTools {
			return " · ↵ filter by tool · tab turns"
		}
		return " · ↵ detail · e error · f flagged · c fold · tab tools · / find"
	}
}

func (m Model) closeInspect() Model {
	m.inspectOpen = false
	m.inspectSession = nil
	m.inspectView = nil
	m.inspectPanel = ""
	m.inspectScrollOffset = 0
	return m
}

// toolKey handles keys while the tool table has focus. It reports whether the
// key was consumed; anything else (e, f, c, /, q, ...) drops focus back to the
// turn table and is handled there as usual.
func (vs *inspectViewState) toolKey(msg tea.KeyPressMsg) bool {
	last := len(vs.toolNames) - 1
	switch {
	case msg.Code == tea.KeyUp:
		vs.toolCursor = max(vs.toolCursor-1, 0)
	case msg.Code == tea.KeyDown:
		vs.toolCursor = min(vs.toolCursor+1, last)
	case msg.Code == tea.KeyHome:
		vs.toolCursor = 0
	case msg.Code == tea.KeyEnd:
		vs.toolCursor = max(last, 0)
	case msg.Code == tea.KeyEnter:
		// Enter toggles: selecting the active filter's tool clears it.
		if vs.toolCursor <= last && vs.toolNames[vs.toolCursor] == inspectToolsToggle {
			// Expand/collapse in place and stay on the table; the render clamps
			// the cursor onto the toggle row again after a collapse.
			vs.allTools = !vs.allTools
			return true
		}
		if vs.toolCursor <= last {
			name := vs.toolNames[vs.toolCursor]
			if vs.toolFilter == name {
				name = ""
			}
			vs.toolFilter = name
		}
		vs.focusTools, vs.cursor = false, 0
	case msg.Code == tea.KeyTab || msg.Code == tea.KeyEsc:
		vs.focusTools = false
	default:
		vs.focusTools = false
		return false
	}
	return true
}

// updateInspectInteractive handles keys for the cursor-driven panel.
func (m Model) updateInspectInteractive(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	vs := m.inspectView
	if vs.searching {
		switch {
		case msg.Code == tea.KeyEsc:
			vs.query, vs.searching, vs.cursor = "", false, 0
		case msg.Code == tea.KeyEnter:
			vs.searching = false
		case msg.Code == tea.KeyBackspace:
			if r := []rune(vs.query); len(r) > 0 {
				vs.query = string(r[:len(r)-1])
				vs.cursor = 0
			}
		case msg.Text != "":
			vs.query += msg.Text
			vs.cursor = 0
		}
		m.rebuildInspectPanel()
		return m, nil
	}

	if vs.detail >= 0 {
		switch msg.Code {
		case tea.KeyEsc, tea.KeyEnter:
			vs.detail = -1
			m.inspectScrollOffset = 0
		case tea.KeyLeft, tea.KeyRight:
			step := 1
			if msg.Code == tea.KeyLeft {
				step = -1
			}
			vs.detail = min(max(vs.detail+step, 0), len(vs.turns)-1)
			m.inspectScrollOffset = 0
		case tea.KeyUp:
			m.inspectScrollOffset = max(0, m.inspectScrollOffset-1)
			return m, nil
		case tea.KeyDown:
			m.inspectScrollOffset = min(m.inspectMaxScrollOffset(), m.inspectScrollOffset+1)
			return m, nil
		case tea.KeyPgUp:
			m.inspectScrollOffset = max(0, m.inspectScrollOffset-m.inspectVisibleLines())
			return m, nil
		case tea.KeyPgDown:
			m.inspectScrollOffset = min(m.inspectMaxScrollOffset(), m.inspectScrollOffset+m.inspectVisibleLines())
			return m, nil
		default:
			if msg.Text == "q" {
				return m.closeInspect(), nil
			}
			return m, nil
		}
		m.rebuildInspectPanel()
		return m, nil
	}

	if vs.focusTools && vs.toolKey(msg) {
		m.rebuildInspectPanel()
		return m, nil
	}
	rows := vs.rows()
	switch {
	case msg.Code == tea.KeyEsc && vs.toolFilter != "":
		vs.toolFilter, vs.cursor = "", 0
	case msg.Code == tea.KeyEsc || msg.Text == "q":
		return m.closeInspect(), nil
	case msg.Code == tea.KeyTab:
		vs.focusTools = len(vs.toolNames) > 0
	case msg.Code == tea.KeyUp:
		vs.cursor--
	case msg.Code == tea.KeyDown:
		vs.cursor++
	case msg.Code == tea.KeyPgUp:
		vs.cursor -= m.inspectVisibleLines()
	case msg.Code == tea.KeyPgDown:
		vs.cursor += m.inspectVisibleLines()
	case msg.Code == tea.KeyHome:
		vs.cursor = 0
	case msg.Code == tea.KeyEnd:
		vs.cursor = len(rows) - 1
	case msg.Code == tea.KeyEnter:
		if len(rows) > 0 {
			vs.clampCursor(len(rows))
			vs.detail = rows[vs.cursor].idx[0]
			m.inspectScrollOffset = 0
		}
	case msg.Text == "e" && len(rows) > 0:
		vs.jumpToError(rows, 1)
	case msg.Text == "E" && len(rows) > 0:
		vs.jumpToError(rows, -1)
	case msg.Text == "f":
		vs.flagged = !vs.flagged
		vs.cursor = 0
	case msg.Text == "c":
		vs.collapse = !vs.collapse
		vs.cursor = 0
	case msg.Text == "t":
		// The tool table sits above the rows, so growing it moves every row
		// line; rebuildInspectPanel re-derives them and keeps the cursor visible.
		vs.allTools = !vs.allTools
	case msg.Text == "/":
		vs.searching = true
	default:
		return m, nil
	}
	vs.clampCursor(len(vs.rows()))
	m.rebuildInspectPanel()
	if msg.Code == tea.KeyHome || msg.Text == "t" {
		// Home means the top of the panel (header included), even when the
		// header is taller than the screen and the first row stays below it.
		// Toggling the tool table likewise brings it into view instead of
		// scrolling back down to the cursor row.
		m.inspectScrollOffset = 0
	}
	return m, nil
}
