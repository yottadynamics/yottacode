package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"

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
	color  compat.AdaptiveColor
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
// tokens is the item's in-window cost. onDemand marks rows whose cost is
// only paid when invoked (custom commands). enabled is whether a skill's
// metadata is actually loaded this session — disabled skills cost nothing
// until toggled on via /skills (onDemand rows are always "enabled" since
// the flag doesn't apply to them).
type skillEntry struct {
	group    string
	name     string
	tokens   int
	onDemand bool
	enabled  bool
}

// cmdContext renders the /context breakdown onto the inline-overlay
// surface (above the cmdline) rather than into chat
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
	// then a muted "esc to close" cues that the overlay owns input until
	// dismissed (any KeyMsg closes it, but the hint names esc — the key
	// people reach for — matching /usage and the cheatsheet; see Update).
	report := renderContextReport(&m) +
		"\n\n" + styleHint.Render("esc to close")
	m.contextReportBody = report
	m.contextReportOpen = true
	return m, nil
}

// renderContextReport assembles the full /context view: header,
// segmented bar, totals line, operational diagnostics, category legend,
// and the breakdown sections (MCP / Memory / Skills).
func renderContextReport(m *Model) string {
	window := m.contextWindow()
	msgs := m.lockedMessages()
	sysTok, convoTok := contextwindow.SplitMessages(msgs)

	sysToolTokens, mcpToolTokens := contextToolTokens(m)

	memFiles := loadContextMemoryFiles(m.cwd)
	memTokens := 0
	for _, f := range memFiles {
		memTokens += f.tokens
	}

	// Skills get their own bucket, carved OUT of System tools rather than
	// added on top. An enabled skill's in-window cost — its name+description
	// metadata line — rides inside the Skill tool's schema description,
	// which contextToolTokens counts under System tools. Summing that
	// metadata into a separate Skills bucket *and* leaving it in System
	// tools would double-count it, so we subtract it back out. The figure
	// is the sum of the enabled skill rows (built-in + user + project);
	// disabled skills aren't in the window (the system prompt no longer
	// enumerates them — see appendSkillsSection) and contribute 0, and
	// bodies load on demand so they're never counted here.
	skillRows := loadContextSkillEntries(m)
	skillTokens := 0
	for _, r := range skillRows {
		if !r.onDemand && r.enabled {
			skillTokens += r.tokens
		}
	}
	sysToolTokens = max(sysToolTokens-skillTokens, 0)

	// Bucket colors are picked for mutual contrast in the bar + legend.
	// Memory files (was Warning/yellow) and Messages (was Assistant/teal)
	// previously read too close to Skills' peach and MCP's green/Accent's
	// blue respectively, so they take the two otherwise-unused palette
	// hues — Error (red) and Content (near-white) — which sit clearly apart
	// from the rest.
	buckets := []contextBucket{
		{"System prompt", sysTok, colorDim, true},
		{"System tools", sysToolTokens, colorAccent, true},
		{"Skills", skillTokens, colorWarm, true},
		{"MCP tools", mcpToolTokens, colorSuccess, true},
		{"Memory files", memTokens, colorError, true},
		{"Messages", convoTok, colorContent, true},
	}

	used := 0
	for _, b := range buckets {
		used += b.tokens
	}
	free := max(window-used, 0)
	buckets = append(buckets, contextBucket{"Free space", free, colorDim, false})

	var out strings.Builder
	out.WriteString(renderMenuHeader("Context", "Window usage, compaction status, and prompt contributors.", m.popupWidth()))
	out.WriteString("\n")

	out.WriteString(renderContextDiagnostics(m, buckets, used, window, sysTok, sysToolTokens+skillTokens+mcpToolTokens, convoTok))
	out.WriteString("\n\n")

	out.WriteString(styleSplashTitle.Render("Estimated usage by category"))
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

// renderContextDiagnostics explains the control-plane decisions behind the
// usage bar: which thresholds are active, whether mid-turn compaction can run,
// and what recent compaction/summarization did. This is deliberately textual
// rather than another graph so users can paste it into bug reports.
func renderContextDiagnostics(m *Model, buckets []contextBucket, used, window, systemTokens, toolTokens, messageTokens int) string {
	var out strings.Builder
	out.WriteString(styleSplashTitle.Render("Diagnostics"))
	out.WriteString("\n")

	warn := thresholdStatus("warn", m.fileCfg.Context.WarnThreshold)
	auto := thresholdStatus("auto", m.fileCfg.Context.AutoThreshold)
	compact := thresholdStatus("compaction", m.fileCfg.Context.CompactionThreshold)
	fmt.Fprintf(&out, "  Window: %s · %s\n", formatTokens(window), contextWindowSource(m))
	fmt.Fprintf(&out, "  Thresholds: %s · %s · %s\n", warn, auto, compact)
	fmt.Fprintf(&out, "  Tool schema overhead: %s tokens\n", formatTokens(toolTokens))
	name, tokens := largestContextBucket(buckets)
	fmt.Fprintf(&out, "  Largest bucket: %s (%s tokens)\n", name, formatTokens(tokens))
	fmt.Fprintf(&out, "  Compaction: %s\n", contextCompactionStatus(m, used, window, systemTokens, toolTokens, messageTokens))
	fmt.Fprintf(&out, "  Last summarize: %s\n", emptyDash(m.lastContextSummary))
	fmt.Fprintf(&out, "  Last mid-turn compaction: %s\n\n", emptyDash(m.lastContextCompaction))
	out.WriteString("  ")
	out.WriteString(renderContextSegmentedBar(buckets, window, m.width))
	out.WriteString("  ")
	out.WriteString(styleMeta.Render(contextPercentLabel(used, window)))
	out.WriteString("\n")
	out.WriteString("  ")
	out.WriteString(styleMeta.Render(contextSummaryLine(used, window, m.modelName)))
	return out.String()
}

func thresholdStatus(name string, value float64) string {
	if value <= 0 || value >= 1.0 {
		return name + " off"
	}
	return fmt.Sprintf("%s %.0f%%", name, value*100)
}

func contextWindowSource(m *Model) string {
	if m.modelName == "" {
		return "no active model"
	}
	if override := m.fileCfg.ContextWindowOverride(m.modelName); override > 0 {
		return "configured/scanned window"
	}
	if key := m.driftKey(); key != "" {
		return "provider/catalog window for " + key
	}
	return "catalog/default window for " + m.modelName
}

func largestContextBucket(buckets []contextBucket) (string, int) {
	name := "none"
	tokens := 0
	for _, b := range buckets {
		if !b.used || b.tokens <= tokens {
			continue
		}
		name, tokens = b.label, b.tokens
	}
	return name, tokens
}

func contextCompactionStatus(m *Model, used, window, systemTokens, toolTokens, messageTokens int) string {
	cc := m.cfg.Compaction
	if cc == nil {
		return "disabled (not wired)"
	}
	if window <= 0 || cc.Window <= 0 {
		return "disabled (window unavailable or irreducible floor too high)"
	}
	threshold := m.fileCfg.Context.CompactionThreshold
	if threshold >= 1.0 || threshold <= 0 {
		return "preemptive off; provider-overflow recovery can force one attempt"
	}
	parts := []string{fmt.Sprintf("fires at %.0f%% (%s)", threshold*100, formatTokens(int(threshold*float64(window))))}
	if used >= int(threshold*float64(window)) {
		parts = append(parts, "currently eligible")
	} else {
		parts = append(parts, fmt.Sprintf("%s until trigger", formatTokens(max(int(threshold*float64(window))-used, 0))))
	}
	floor := systemTokens + toolTokens + int(contextCompactionTargetRatio(m.fileCfg.Context.CompactionTargetRatio)*float64(window))
	parts = append(parts, fmt.Sprintf("floor≈%s + messages %s", formatTokens(floor), formatTokens(messageTokens)))
	return strings.Join(parts, "; ")
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
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
// flat list with sub-group labels. Built-in / User / Project groups come
// from m.skills (the loader sets Source); custom commands get their own
// "Custom" group.
//
// For skills the per-entry figure is the *loaded metadata* cost — the
// `- name: description` line SkillTool.Description carries into the Skill
// tool schema, which is what's actually in the window. Only *enabled*
// skills are in that schema, so a disabled skill is marked enabled=false
// and contributes 0: it costs nothing until toggled on via /skills. The
// body always loads on demand, so it is never counted here.
//
// Custom commands are different: nothing about them is in the window until
// the user invokes one (the body is injected as a synthetic user message
// then), so they're flagged onDemand and sized by their help string — the
// closest always-available proxy for the per-invocation cost.
func loadContextSkillEntries(m *Model) []skillEntry {
	var out []skillEntry
	for _, s := range m.skills {
		// No skillTool wired (test fixtures, headless) → treat as loaded so
		// the metadata cost still shows rather than collapsing to "off".
		enabled := m.skillTool == nil || m.skillTool.IsEnabled(s.Name)
		tokens := 0
		if enabled {
			tokens = skillMetadataTokens(s.Name, s.Description)
		}
		out = append(out, skillEntry{
			group:   contextSkillGroupLabel(s.Source),
			name:    s.Name,
			tokens:  tokens,
			enabled: enabled,
		})
	}
	for _, c := range m.customSlash {
		if !c.IsCustom {
			continue
		}
		out = append(out, skillEntry{
			group:    "Custom",
			name:     c.Name,
			tokens:   contextwindow.EstimateText(c.Help),
			onDemand: true,
			enabled:  true,
		})
	}
	return out
}

// skillMetadataTokens estimates a skill's always-loaded cost: the
// `- name: description` metadata line, formatted exactly as
// appendSkillsSection / SkillTool.Description emit it so the figure
// tracks what the model actually receives. This is the metadata tier of
// progressive disclosure — small and always present — as opposed to the
// body, which only enters the window when the skill is invoked.
func skillMetadataTokens(name, desc string) int {
	return contextwindow.EstimateText(fmt.Sprintf("- %s: %s\n", name, desc))
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
	out.WriteString(styleMeta.Render("  · /mcp"))
	out.WriteString("\n")
	if m.mcpManager == nil {
		out.WriteString("  ")
		out.WriteString(styleMeta.Render(
			"(no MCP servers configured — run /mcp add)"))
		return out.String()
	}
	statuses := m.mcpManager.Statuses()
	if len(statuses) == 0 {
		out.WriteString("  ")
		out.WriteString(styleMeta.Render(
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
		out.WriteString(styleMeta.Render(prefix))
		var statusTag string
		switch {
		case s.Err != nil:
			statusTag = styleError.Render("error")
		default:
			statusTag = styleMeta.Render(
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
	out.WriteString(styleMeta.Render("  · /memory"))
	out.WriteString("\n")
	if len(files) == 0 {
		out.WriteString("  ")
		out.WriteString(styleMeta.Render(
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
		out.WriteString(styleMeta.Render(prefix))
		fmt.Fprintf(&out, "%-*s   %s tokens\n",
			pathWidth, f.label, formatTokens(f.tokens))
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderContextSkillsSection groups entries by sub-header (Built-in /
// User / Project / Custom) and lists each with its in-window token
// estimate. Mirrors Claude's `Skills · /skills` block. For an *enabled*
// skill the figure is its loaded metadata line (name + description) — the
// real slice of the window it occupies via the Skill tool schema; the
// body isn't counted because it only loads on invocation. A *disabled*
// skill shows `off` — it costs nothing until toggled on with /skills.
// Custom-command rows, whose cost is paid only when invoked, are tagged
// `(on demand)`. The enabled rows sum to the Skills bucket in the legend
// above (see renderContextReport) — this section is that bucket's per-skill
// breakdown, not an additional charge.
func renderContextSkillsSection(rows []skillEntry) string {
	var out strings.Builder
	out.WriteString(styleSplashTitle.Render("Skills"))
	out.WriteString(styleMeta.Render("  · /skills · enabled rows sum to the Skills bucket above; bodies on demand"))
	out.WriteString("\n")
	if len(rows) == 0 {
		out.WriteString("  ")
		out.WriteString(styleMeta.Render(
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
		out.WriteString(styleMeta.Render(g))
		out.WriteString("\n")
		entries := groups[g]
		nameWidth := 0
		for _, e := range entries {
			if w := len(e.name); w > nameWidth {
				nameWidth = w
			}
		}
		onDemandTag := "  " + styleMeta.Render("(on demand)")
		offValue := styleEmpty.Render("off · not loaded")
		for i, e := range entries {
			prefix := "├ "
			if i == len(entries)-1 {
				prefix = "└ "
			}
			out.WriteString("  ")
			out.WriteString(styleMeta.Render(prefix))
			// Disabled skill: nothing in the window until /skills toggles it
			// on, so show its off state instead of a phantom token figure.
			if !e.onDemand && !e.enabled {
				fmt.Fprintf(&out, "%-*s   %s\n", nameWidth, e.name, offValue)
				continue
			}
			tag := ""
			if e.onDemand {
				tag = onDemandTag
			}
			fmt.Fprintf(&out, "%-*s   %s tokens%s\n",
				nameWidth, e.name, formatTokens(e.tokens), tag)
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
