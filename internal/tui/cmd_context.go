package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/contextwindow"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/skills"
)

// contextBarMin / contextBarMax bound the segmented progress bar so it
// reads cleanly across terminal sizes. The floor avoids unusable
// granularity in a narrow split; the ceiling keeps the bar from
// dominating a wide terminal. The status-bar `ctx` segment uses a
// fixed 6 cells (watermark.go's ctxBarWidth) — this view is a zoomed-
// in inspection, so we deliberately go wider.
const (
	contextBarMin = 32
	contextBarMax = 80
)

// contextBucket is one row of the breakdown. tokens drives both the
// bar segment width and the legend value; color paints both the
// matching bar segment and the row's leading marker so the eye can
// trace each segment back to its label. used=false marks free space
// (different glyph in the legend, no bar segment).
type contextBucket struct {
	label  string
	tokens int
	color  lipgloss.AdaptiveColor
	used   bool
}

// memoryFileEntry is one row of the Memory files section. label is
// the display path (already abbreviated for home/cwd); tokens is the
// estimated byte cost of the file contents.
type memoryFileEntry struct {
	label  string
	tokens int
}

// skillEntry is one row of the Skills section. group is which sub-
// header it lives under ("Built-in", "User", "Project", "Custom").
type skillEntry struct {
	group  string
	name   string
	tokens int
}

// cmdContext renders the /context breakdown onto the inline-overlay
// surface (below the cmdline + status bar) rather than into chat
// history — the report is transient inspection, so keeping it out of
// scrollback / the transcript / resume replay avoids cluttering the
// conversation record. The body is snapshotted here (it reads memory
// files from disk and walks the skill set) and held in
// m.contextReportBody until any key dismisses the overlay; see View()
// and the KeyMsg handler in Update. Read-only inspection; registered
// with PreservesTurn=true so it's safe to invoke while a turn is
// streaming.
func cmdContext(m Model, _ []string) (Model, tea.Cmd) {
	// Trailing blank line lifts the dismiss hint off the last section,
	// then a muted "press any key to exit" cues that the overlay owns
	// input until dismissed (any KeyMsg closes it; see Update).
	report := renderContextReport(&m) +
		"\n\n" + stylePaletteEmpty.Render("press any key to exit")
	// Indent the whole block 2 spaces so every line — headers included,
	// not just the already-indented legend/detail rows — clears the
	// overlay's left edge uniformly.
	m.contextReportBody = indentContextReport(report, "  ")
	m.contextReportOpen = true
	return m, nil
}

// indentContextReport prefixes each non-blank line with pad. Blank
// lines are left untouched so the block doesn't carry trailing
// whitespace. ANSI-safe — the pad lands before any style codes at the
// start of the line.
func indentContextReport(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if ln != "" {
			lines[i] = pad + ln
		}
	}
	return strings.Join(lines, "\n")
}

// renderContextReport assembles the full /context view: header,
// segmented bar, totals line, category legend, and the three
// breakdown sections (MCP / Memory / Skills).
func renderContextReport(m *Model) string {
	window := m.contextWindow()
	sysTok, convoTok := contextwindow.SplitMessages(m.sess.Messages)

	sysToolTokens, mcpToolTokens := contextToolTokens(m)

	memFiles := loadContextMemoryFiles(m.cwd)
	memTokens := 0
	for _, f := range memFiles {
		memTokens += f.tokens
	}

	// Skills are deliberately NOT a window bucket. Their in-window cost —
	// the name+description metadata tier — is already charged elsewhere:
	// to System prompt (appendSkillsSection bakes the list into the
	// system message) and to System tools (the Skill tool's schema
	// description carries the same list). Skill *bodies* load on demand
	// via the Skill tool, so they're not in-window until invoked.
	// Summing bodies here previously phantom-charged the window (~22K on
	// the built-in set) and skewed the total / free-space / percentage.
	// The Skills section below remains as an on-disk inventory; it does
	// not feed `used`.
	skillRows := loadContextSkillEntries(m)

	buckets := []contextBucket{
		{"System prompt", sysTok, colorDim, true},
		{"System tools", sysToolTokens, colorAccent, true},
		{"MCP tools", mcpToolTokens, colorSuccess, true},
		{"Memory files", memTokens, colorWarning, true},
		{"Messages", convoTok, colorAssistant, true},
	}

	used := 0
	for _, b := range buckets {
		used += b.tokens
	}
	free := max(window-used, 0)
	buckets = append(buckets, contextBucket{"Free space", free, colorDim, false})

	var out strings.Builder
	out.WriteString(styleSplashTitle.Render("Context Usage"))
	out.WriteString("\n\n")

	out.WriteString(renderContextSegmentedBar(buckets, window, m.width))
	out.WriteString("  ")
	out.WriteString(stylePaletteEmpty.Render(contextPercentLabel(used, window)))
	out.WriteString("\n")
	out.WriteString(stylePaletteEmpty.Render(contextSummaryLine(used, window, m.modelName)))
	out.WriteString("\n\n")

	out.WriteString(stylePaletteEmpty.Render("Estimated usage by category"))
	out.WriteString("\n")
	out.WriteString(renderContextLegend(buckets, window))

	out.WriteString("\n\n")
	out.WriteString(renderContextMCPSection(m))

	out.WriteString("\n\n")
	out.WriteString(renderContextMemorySection(memFiles))

	out.WriteString("\n\n")
	out.WriteString(renderContextSkillsSection(skillRows))

	return strings.TrimRight(out.String(), "\n")
}

// contextToolTokens splits the registry's advertised tools into built-
// in (the agent loop's native registrations) and MCP-derived (tools
// the bridge published under the `mcp/<server>/` namespace). Returns
// (0, 0) when the registry isn't wired in (test fixtures, headless
// runs).
func contextToolTokens(m *Model) (sysTokens, mcpTokens int) {
	if m.cfg.Registry == nil {
		return 0, 0
	}
	all := m.cfg.Registry.AsAdapterTools()
	var sys, mcp []adapter.Tool
	for _, t := range all {
		if strings.HasPrefix(t.Name, "mcp/") {
			mcp = append(mcp, t)
		} else {
			sys = append(sys, t)
		}
	}
	return contextwindow.EstimateToolSchemas(sys),
		contextwindow.EstimateToolSchemas(mcp)
}

// loadContextMemoryFiles reads the same memory sources SystemPrompt
// injects, returning one entry per discovered file. Missing files are
// silently skipped (memory.Load already handles that path).
func loadContextMemoryFiles(cwd string) []memoryFileEntry {
	mem, err := memory.Load(cwd)
	if err != nil {
		return nil
	}
	var out []memoryFileEntry
	if mem.UserText != "" {
		out = append(out, memoryFileEntry{
			label:  displayPath(mem.UserPath, cwd),
			tokens: contextwindow.EstimateText(mem.UserText),
		})
	}
	if mem.ProjectText != "" {
		out = append(out, memoryFileEntry{
			label:  displayPath(mem.ProjectPath, cwd),
			tokens: contextwindow.EstimateText(mem.ProjectText),
		})
	}
	if mem.UserMemoryIndex != "" {
		out = append(out, memoryFileEntry{
			label:  displayPath(mem.UserMemoryDir+"/MEMORY.md", cwd),
			tokens: contextwindow.EstimateText(mem.UserMemoryIndex),
		})
	}
	if mem.ProjectMemoryIndex != "" {
		out = append(out, memoryFileEntry{
			label:  displayPath(mem.ProjectMemoryDir+"/MEMORY.md", cwd),
			tokens: contextwindow.EstimateText(mem.ProjectMemoryIndex),
		})
	}
	return out
}

// loadContextSkillEntries collects every skill + custom command into a
// flat list with sub-group labels. Built-in / User / Project groups
// come from m.skills (the loader sets Source); custom commands get
// their own "Custom" group. The per-entry token figure is the body's
// estimated *on-demand* cost — what one Skill invocation would add to
// the window that turn — falling back to the help string for items
// where the body isn't kept in memory. It is NOT charged to the live
// window: the caller renders these as inventory only and excludes them
// from the usage total (see renderContextReport's bucket comment).
func loadContextSkillEntries(m *Model) []skillEntry {
	var out []skillEntry
	for _, s := range m.skills {
		group := contextSkillGroupLabel(s.Source)
		out = append(out, skillEntry{
			group:  group,
			name:   s.Name,
			tokens: contextwindow.EstimateText(s.Body),
		})
	}
	for _, c := range m.customSlash {
		if !c.IsCustom {
			continue
		}
		out = append(out, skillEntry{
			group:  "Custom",
			name:   c.Name,
			tokens: contextwindow.EstimateText(c.Help),
		})
	}
	return out
}

func contextSkillGroupLabel(src skills.Scope) string {
	switch src {
	case skills.ScopeBuiltin:
		return "Built-in"
	case skills.ScopeUser:
		return "User"
	case skills.ScopeProject:
		return "Project"
	default:
		return "Other"
	}
}

// contextBarWidth resolves the bar width from the terminal width with
// floor / ceiling clamps. width=0 (the bubbletea pre-WindowSizeMsg
// state) falls through to a sensible default.
func contextBarWidth(termWidth int) int {
	w := termWidth - 4
	if termWidth <= 0 {
		w = 64
	}
	if w > contextBarMax {
		w = contextBarMax
	}
	if w < contextBarMin {
		w = contextBarMin
	}
	return w
}

// renderContextSegmentedBar paints one cell per `window/width` tokens
// in the bucket's color, left-to-right in bucket order. Free space
// renders as dim `░`. Non-zero buckets get at least one cell so they
// don't disappear entirely on a large window — accuracy gives way to
// "this bucket exists and is non-empty" at the tail.
func renderContextSegmentedBar(buckets []contextBucket, window, termWidth int) string {
	width := contextBarWidth(termWidth)
	if window <= 0 {
		return lipgloss.NewStyle().Foreground(colorDim).Render(strings.Repeat("░", width))
	}
	var bar strings.Builder
	cellsUsed := 0
	for _, b := range buckets {
		if !b.used || b.tokens <= 0 {
			continue
		}
		cells := int(math.Round(float64(b.tokens) / float64(window) * float64(width)))
		if cells == 0 {
			cells = 1
		}
		if cellsUsed+cells > width {
			cells = width - cellsUsed
		}
		if cells <= 0 {
			continue
		}
		bar.WriteString(lipgloss.NewStyle().Foreground(b.color).Render(
			strings.Repeat("█", cells)))
		cellsUsed += cells
	}
	if cellsUsed < width {
		bar.WriteString(lipgloss.NewStyle().Foreground(colorDim).Render(
			strings.Repeat("░", width-cellsUsed)))
	}
	return bar.String()
}

// renderContextLegend prints one row per bucket: colored mark + label
// + value + percentage. Column widths align so the eye can scan
// straight down the value column.
func renderContextLegend(buckets []contextBucket, window int) string {
	// First pass: compute label column width so the percent column
	// lines up regardless of which translations / future bucket
	// labels we add.
	labelWidth := 0
	for _, b := range buckets {
		if w := len(b.label); w > labelWidth {
			labelWidth = w
		}
	}
	valueWidth := 0
	values := make([]string, len(buckets))
	for i, b := range buckets {
		values[i] = formatTokens(b.tokens)
		if w := len(values[i]); w > valueWidth {
			valueWidth = w
		}
	}

	var out strings.Builder
	for i, b := range buckets {
		mark := "■"
		if !b.used {
			mark = "░"
		}
		markStyled := lipgloss.NewStyle().Foreground(b.color).Render(mark)
		pct := 0.0
		if window > 0 {
			pct = float64(b.tokens) / float64(window) * 100
		}
		fmt.Fprintf(&out, "  %s %-*s   %*s  (%.1f%%)\n",
			markStyled, labelWidth, b.label+":", valueWidth, values[i], pct)
	}
	return strings.TrimRight(out.String(), "\n")
}

func contextSummaryLine(used, window int, model string) string {
	if model == "" {
		model = "(no model)"
	}
	return fmt.Sprintf("%s / %s  ·  %s",
		formatTokens(used), formatTokens(window), model)
}

func contextPercentLabel(used, window int) string {
	if window <= 0 {
		return "—"
	}
	// Cap at 100% for visual consistency with the status-bar `ctx`
	// segment (watermark.go's renderContextBar does the same). The
	// rough 4-chars/token heuristic can drift above the real window
	// by ~15%; without the cap a 110% reading would suggest a precision
	// the estimate doesn't have.
	pct := min(int(math.Round(float64(used)/float64(window)*100)), 100)
	return fmt.Sprintf("%d%%", pct)
}

// renderContextMCPSection lists configured MCP servers and their tool
// counts + token cost. Mirrors Claude's `MCP tools · /mcp` block. When
// no servers are configured, prints the empty-state hint that points
// at /mcp add.
func renderContextMCPSection(m *Model) string {
	var out strings.Builder
	out.WriteString(styleSplashTitle.Render("MCP tools"))
	out.WriteString(stylePaletteEmpty.Render("  · /mcp"))
	out.WriteString("\n")
	if m.mcpManager == nil {
		out.WriteString("  ")
		out.WriteString(stylePaletteEmpty.Render(
			"(no MCP servers configured — run /mcp add)"))
		return out.String()
	}
	statuses := m.mcpManager.Statuses()
	if len(statuses) == 0 {
		out.WriteString("  ")
		out.WriteString(stylePaletteEmpty.Render(
			"(no MCP servers configured — run /mcp add)"))
		return out.String()
	}
	// Compute aligned columns.
	nameWidth := 0
	for _, s := range statuses {
		if w := len(s.Name); w > nameWidth {
			nameWidth = w
		}
	}
	for i, s := range statuses {
		prefix := "├ "
		if i == len(statuses)-1 {
			prefix = "└ "
		}
		out.WriteString("  ")
		out.WriteString(stylePaletteEmpty.Render(prefix))
		var statusTag string
		switch {
		case s.Err != nil:
			statusTag = styleError.Render("error")
		default:
			statusTag = stylePaletteEmpty.Render(
				fmt.Sprintf("%d tools", s.ToolCount))
		}
		fmt.Fprintf(&out, "%-*s   %s\n", nameWidth, s.Name, statusTag)
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderContextMemorySection lists the loaded memory files with their
// token cost. Header mirrors `Memory files · /memory`. The token
// column is right-aligned for the same scan-down reason as the
// legend.
func renderContextMemorySection(files []memoryFileEntry) string {
	var out strings.Builder
	out.WriteString(styleSplashTitle.Render("Memory files"))
	out.WriteString(stylePaletteEmpty.Render("  · /memory"))
	out.WriteString("\n")
	if len(files) == 0 {
		out.WriteString("  ")
		out.WriteString(stylePaletteEmpty.Render(
			"(no memory files — USER.md, YOTTACODE.md, or MEMORY.md will appear here)"))
		return out.String()
	}
	pathWidth := 0
	for _, f := range files {
		if w := len(f.label); w > pathWidth {
			pathWidth = w
		}
	}
	for i, f := range files {
		prefix := "├ "
		if i == len(files)-1 {
			prefix = "└ "
		}
		out.WriteString("  ")
		out.WriteString(stylePaletteEmpty.Render(prefix))
		fmt.Fprintf(&out, "%-*s   %s tokens\n",
			pathWidth, f.label, formatTokens(f.tokens))
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderContextSkillsSection groups entries by sub-header (Built-in /
// User / Project / Custom) and lists each with its body token estimate.
// Mirrors Claude's `Skills · /skills` block. This section is purely an
// on-disk inventory: skill bodies load on demand via the Skill tool, so
// the per-row figure is the *per-invocation* cost, NOT a slice of the
// current window — which is why Skills is absent from the bar / legend /
// total above. Each row is tagged `(on demand)` to make that explicit
// where the number is shown.
func renderContextSkillsSection(rows []skillEntry) string {
	var out strings.Builder
	out.WriteString(styleSplashTitle.Render("Skills"))
	out.WriteString(stylePaletteEmpty.Render("  · /skills · loaded on demand"))
	out.WriteString("\n")
	if len(rows) == 0 {
		out.WriteString("  ")
		out.WriteString(stylePaletteEmpty.Render(
			"(no skills loaded — place SKILL.md under ~/.yottacode/skills/ or .yottacode/skills/)"))
		return out.String()
	}

	groups := map[string][]skillEntry{}
	var order []string
	for _, r := range rows {
		if _, seen := groups[r.group]; !seen {
			order = append(order, r.group)
		}
		groups[r.group] = append(groups[r.group], r)
	}
	// Stable display order: Built-in first, then User, Project, then
	// any others (Custom) appended after. This matches Claude's
	// Plugin/Built-in convention of putting the platform-shipped set
	// at the top.
	preferred := []string{"Built-in", "User", "Project", "Custom"}
	sort.SliceStable(order, func(i, j int) bool {
		ri := indexOf(preferred, order[i])
		rj := indexOf(preferred, order[j])
		if ri == rj {
			return order[i] < order[j]
		}
		return ri < rj
	})

	for gi, g := range order {
		if gi > 0 {
			out.WriteString("\n")
		}
		out.WriteString("  ")
		out.WriteString(stylePaletteEmpty.Render(g))
		out.WriteString("\n")
		entries := groups[g]
		nameWidth := 0
		for _, e := range entries {
			if w := len(e.name); w > nameWidth {
				nameWidth = w
			}
		}
		onDemand := stylePaletteEmpty.Render("(on demand)")
		for i, e := range entries {
			prefix := "├ "
			if i == len(entries)-1 {
				prefix = "└ "
			}
			out.WriteString("  ")
			out.WriteString(stylePaletteEmpty.Render(prefix))
			fmt.Fprintf(&out, "%-*s   %s tokens   %s\n",
				nameWidth, e.name, formatTokens(e.tokens), onDemand)
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func indexOf(ss []string, s string) int {
	for i, x := range ss {
		if x == s {
			return i
		}
	}
	return len(ss)
}
