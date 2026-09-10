package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/yottadynamics/yottacode/internal/codemap"
)

const codeMapVisibleRows = 28
const codemapDefaultImpactDepth = codemap.MaxDepthAll

// codeMapExportMax is the effective "unbounded" cap used when writing a
// diagram to disk via `/map diagram --export`, instead of the smaller cap
// used for the on-screen picker render.
const codeMapExportMax = 1_000_000

type codeMapMode string

const (
	codeMapModeStructure    codeMapMode = "structure"
	codeMapModeDependencies codeMapMode = "dependencies"
	codeMapModeDependents   codeMapMode = "dependents"
	codeMapModeImpact       codeMapMode = "impact"
	codeMapModeCycles       codeMapMode = "cycles"
	codeMapModeDiagram      codeMapMode = "diagram"
	codeMapModeHere         codeMapMode = "here"
)

type codeMapPickerState struct {
	index      *codemap.CodeIndex
	rows       []codeMapRow
	mode       codeMapMode
	depth      int
	expanded   map[codemap.NodeID]bool
	cursor     int
	filter     string
	hereFiles  []string
	diagram    string
	subsystem  string
	exportPath string
	status     string
	loading    bool
	err        string
}

type codeMapRow struct {
	id    codemap.NodeID
	depth int
	label string
}

type codeMapLoadedMsg struct {
	idx    *codemap.CodeIndex
	filter string
	mode   codeMapMode
	depth  int
	err    error
}

func (m Model) openCodeMapPicker(mode codeMapMode, filter string, depth int, exportPath string) (Model, tea.Cmd) {
	if m.codeMapProvider == nil {
		m.appendLine(styleError.Render(SysMsg(SysWarning, "map", "experimental feature required", "--experimental code_map")))
		return m, nil
	}
	hereFiles := []string(nil)
	if mode == codeMapModeHere {
		hereFiles = m.codeMapHereFiles(filter)
	}
	if exportPath != "" && !filepath.IsAbs(exportPath) {
		exportPath = filepath.Join(m.cwd, exportPath)
	}
	m.codeMapPicker = &codeMapPickerState{mode: mode, depth: depth, filter: filter, hereFiles: hereFiles, exportPath: exportPath, expanded: map[codemap.NodeID]bool{}, loading: true, status: "building code map…"}
	m.codeMapPickerOpen = true
	return m, m.loadCodeMapCmd(mode, filter, depth)
}

func (m Model) codeMapHereFiles(filter string) []string {
	if strings.TrimSpace(filter) != "" {
		return []string{filter}
	}
	cmd := exec.Command("git", "status", "--porcelain", "-z")
	cmd.Dir = m.cwd
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	parts := strings.Split(string(out), "\x00")
	seen := map[string]bool{}
	var files []string
	for _, part := range parts {
		if len(part) < 4 {
			continue
		}
		path := strings.TrimSpace(part[3:])
		if strings.Contains(path, " -> ") {
			chunks := strings.Split(path, " -> ")
			path = chunks[len(chunks)-1]
		}
		if path != "" && !seen[path] {
			seen[path] = true
			files = append(files, path)
		}
	}
	return files
}

func (m Model) loadCodeMapCmd(mode codeMapMode, filter string, depth int) tea.Cmd {
	provider := m.codeMapProvider
	return func() tea.Msg {
		idx, err := provider.Index(context.Background())
		return codeMapLoadedMsg{idx: idx, filter: filter, mode: mode, depth: depth, err: err}
	}
}

func (m Model) handleCodeMapLoaded(msg codeMapLoadedMsg) (Model, tea.Cmd) {
	if !m.codeMapPickerOpen || m.codeMapPicker == nil {
		return m, nil
	}
	p := m.codeMapPicker
	p.loading = false
	p.filter = msg.filter
	p.mode = msg.mode
	p.depth = msg.depth
	if msg.err != nil {
		p.err = msg.err.Error()
		p.status = "map build failed"
		return m, nil
	}
	p.index = msg.idx
	p.expanded = map[codemap.NodeID]bool{}
	if msg.idx != nil {
		p.expanded[msg.idx.Root()] = true
	}
	p.rebuildRows()
	if p.status == "" {
		p.status = fmt.Sprintf("%d nodes indexed", msg.idx.Count())
	}
	return m, nil
}

func (m Model) updateCodeMapPicker(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.codeMapPicker
	if p == nil {
		m.codeMapPickerOpen = false
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEsc:
		m.codeMapPickerOpen = false
		m.codeMapPicker = nil
		return m, nil
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if p.cursor < len(p.rows)-1 {
			p.cursor++
		}
		return m, nil
	case tea.KeyEnter:
		return m.acceptCodeMapSelection(), nil
	case tea.KeyBackspace:
		if p.filter != "" {
			p.filter = p.filter[:len(p.filter)-1]
			p.rebuildRows()
		}
		return m, nil
	}
	if msg.Text != "" {
		switch msg.Text {
		case "j":
			if p.cursor < len(p.rows)-1 {
				p.cursor++
			}
		case "k":
			if p.cursor > 0 {
				p.cursor--
			}
		case "r":
			p.loading = true
			p.status = "rebuilding code map…"
			return m, m.loadCodeMapCmd(p.mode, p.filter, p.depth)
		case "a":
			if p.mode == codeMapModeHere {
				return m.acceptCodeMapAttachAll(), nil
			}
			p.filter += msg.Text
			p.rebuildRows()
		default:
			p.filter += msg.Text
			p.rebuildRows()
		}
	}
	return m, nil
}

// updateCodeMapPickerPaste appends a bracketed paste to the filter
// buffer, mirroring the msg.Text accumulation at the tail of
// updateCodeMapPicker above. The filter is a plain string (no
// textinput.Model), so unlike the other popups this is a direct
// append rather than a delegated Update call.
func updateCodeMapPickerPaste(m Model, msg tea.PasteMsg) (Model, tea.Cmd) {
	p := m.codeMapPicker
	if p == nil || msg.Content == "" {
		return m, nil
	}
	p.filter += msg.Content
	p.rebuildRows()
	return m, nil
}

func (m Model) acceptCodeMapSelection() Model {
	p := m.codeMapPicker
	if p == nil || p.index == nil || p.cursor < 0 || p.cursor >= len(p.rows) {
		return m
	}
	row := p.rows[p.cursor]
	n, ok := p.index.Node(row.id)
	if !ok {
		return m
	}
	if n.Kind == codemap.NodeDirectory {
		p.expanded[row.id] = !p.expanded[row.id]
		p.rebuildRows()
		return m
	}
	path := n.RelPath
	if n.Kind == codemap.NodeSymbol {
		p.status = fmt.Sprintf("inserted @%s for %s at line %d", path, n.Name, n.Symbol.Range.Start.Line+1)
	} else {
		p.status = "inserted @" + path
	}
	m.insertCodeMapRef(path)
	m.codeMapPickerOpen = false
	m.codeMapPicker = nil
	return m
}

// acceptCodeMapAttachAll inserts an @path ref for every file-kind row
// currently listed in `/map here`'s suggested-context list, then closes the
// picker — the bulk counterpart to Enter's single-row insert-and-close.
func (m Model) acceptCodeMapAttachAll() Model {
	p := m.codeMapPicker
	if p == nil || p.index == nil {
		return m
	}
	attached := 0
	for _, row := range p.rows {
		n, ok := p.index.Node(row.id)
		if !ok || n.Kind != codemap.NodeFile {
			continue
		}
		m.insertCodeMapRef(n.RelPath)
		attached++
	}
	if attached == 0 {
		p.status = "no files to attach"
		return m
	}
	m.codeMapPickerOpen = false
	m.codeMapPicker = nil
	return m
}

func (m *Model) insertCodeMapRef(path string) {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return
	}
	val := strings.TrimRight(m.textInput.Value(), " \t\n")
	if val != "" {
		val += " "
	}
	m.textInput.SetValue(val + "@" + path + " ")
	m.textInput.CursorEnd()
	m.refreshFilePalette(m.textInput.Value())
}

func (p *codeMapPickerState) rebuildRows() {
	if p == nil || p.index == nil {
		return
	}
	p.diagram = ""
	p.subsystem = ""
	if p.mode != "" && p.mode != codeMapModeStructure {
		p.rows = p.rows[:0]
		if strings.TrimSpace(p.filter) == "" && p.mode != codeMapModeCycles && p.mode != codeMapModeDiagram && p.mode != codeMapModeHere {
			p.status = "enter a file/path after /map " + string(p.mode)
			return
		}
		var nodes []codemap.Node
		switch p.mode {
		case codeMapModeHere:
			p.rebuildHereRows()
			return
		case codeMapModeDependencies:
			nodes = p.index.Dependencies(p.filter, codeMapVisibleRows*2)
		case codeMapModeDependents:
			nodes = p.index.Dependents(p.filter, codeMapVisibleRows*2)
		case codeMapModeImpact:
			impact := p.index.Impact(p.filter, p.depth, codeMapVisibleRows)
			for _, n := range impact.DirectDependencies {
				p.rows = append(p.rows, codeMapRow{id: n.ID, depth: 0, label: "depends on"})
			}
			for _, n := range impact.DirectDependents {
				p.rows = append(p.rows, codeMapRow{id: n.ID, depth: 0, label: "depended on by"})
			}
			for _, n := range impact.TransitiveDependents {
				p.rows = append(p.rows, codeMapRow{id: n.ID, depth: 0, label: "transitive dependent"})
			}
			for _, n := range impact.LikelyTests {
				p.rows = append(p.rows, codeMapRow{id: n.ID, depth: 0, label: "likely test"})
			}
			for _, n := range impact.LikelyDocs {
				p.rows = append(p.rows, codeMapRow{id: n.ID, depth: 0, label: "likely docs"})
			}
			if len(p.rows) == 0 {
				p.status = "no impact found for " + p.filter
			} else {
				p.status = fmt.Sprintf("%d impact edges for %s · cycles=%d", len(p.rows), p.filter, len(impact.Cycles))
			}
			p.clampCursor()
			return
		case codeMapModeCycles:
			for _, cycle := range p.index.Cycles(p.filter, codeMapVisibleRows) {
				if len(cycle) > 0 {
					p.rows = append(p.rows, codeMapRow{id: cycle[0].ID, depth: 0, label: cycleLabel(cycle)})
				}
			}
		case codeMapModeDiagram:
			p.diagram = codemap.MermaidDiagram(p.index, p.filter, codeMapVisibleRows*2)
			p.status = "Mermaid diagram generated"
			if p.exportPath != "" {
				if strings.TrimSpace(p.filter) == "" {
					p.status = "diagram export needs a path to focus on: /map diagram --export <path> <file>"
				} else {
					p.status = p.exportDiagram()
				}
			}
			p.clampCursor()
			return
		}
		for _, n := range nodes {
			p.rows = append(p.rows, codeMapRow{id: n.ID, depth: 0})
		}
		if len(nodes) == 0 {
			p.status = "no " + string(p.mode) + " found for " + p.filter
		} else {
			p.status = fmt.Sprintf("%d %s for %s", len(nodes), p.mode, p.filter)
		}
		p.clampCursor()
		return
	}
	if strings.TrimSpace(p.filter) != "" {
		if overview, ok := codemap.BuildSubsystemOverview(p.index, p.filter, codeMapVisibleRows); ok {
			p.subsystem = codemap.FormatSubsystemOverview(overview, codeMapVisibleRows)
			p.status = "subsystem overview: " + overview.Dir.RelPath
			p.clampCursor()
			return
		}
		matches := p.index.Filter(p.filter, codeMapVisibleRows*2)
		p.rows = p.rows[:0]
		for _, n := range matches {
			p.rows = append(p.rows, codeMapRow{id: n.ID, depth: 0})
		}
		p.clampCursor()
		return
	}
	p.rows = p.rows[:0]
	var walk func(codemap.NodeID, int)
	walk = func(id codemap.NodeID, depth int) {
		p.rows = append(p.rows, codeMapRow{id: id, depth: depth})
		if !p.expanded[id] {
			return
		}
		for _, child := range p.index.Children(id) {
			walk(child, depth+1)
		}
	}
	walk(p.index.Root(), 0)
	p.clampCursor()
}

// exportDiagram writes the full, unbounded diagram for the current filter to
// p.exportPath (already resolved to an absolute path in openCodeMapPicker)
// and returns a status line describing the outcome.
func (p *codeMapPickerState) exportDiagram() string {
	full := codemap.MermaidDiagram(p.index, p.filter, codeMapExportMax)
	if err := os.MkdirAll(filepath.Dir(p.exportPath), 0o755); err != nil {
		return "diagram export failed: " + err.Error()
	}
	if err := os.WriteFile(p.exportPath, []byte(full), 0o644); err != nil {
		return "diagram export failed: " + err.Error()
	}
	return fmt.Sprintf("diagram exported to %s (%d bytes)", p.exportPath, len(full))
}

func (p *codeMapPickerState) rebuildHereRows() {
	if len(p.hereFiles) == 0 {
		p.status = "no changed files found; pass a path: /map here <path>"
		p.clampCursor()
		return
	}
	changed := make([]string, 0, len(p.hereFiles))
	for _, path := range p.hereFiles {
		matches := p.index.Filter(path, 20)
		for _, n := range matches {
			if n.Kind == codemap.NodeFile {
				changed = append(changed, n.RelPath)
			}
		}
	}
	items := codemap.SuggestedContext(p.index, changed, codeMapVisibleRows)
	for _, item := range items {
		p.rows = append(p.rows, codeMapRow{id: item.Node.ID, label: reasonsLabel(item.Reasons)})
	}
	if len(p.rows) == 0 {
		p.status = "no indexed matches for changed files"
	} else {
		p.status = fmt.Sprintf("suggested context: %d file(s) around %d changed", len(p.rows), len(changed))
	}
	p.clampCursor()
}

func reasonsLabel(reasons []codemap.Reason) string {
	parts := make([]string, 0, len(reasons))
	for _, r := range reasons {
		parts = append(parts, string(r))
	}
	return strings.Join(parts, ", ")
}

func (p *codeMapPickerState) clampCursor() {
	if p.cursor >= len(p.rows) {
		p.cursor = max(0, len(p.rows)-1)
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func renderCodeMapPicker(p *codeMapPickerState, width int, hits ...*pickerHits) string {
	var h *pickerHits
	if len(hits) > 0 {
		h = hits[0]
	}
	if p == nil {
		return styleEmpty.Render("(code map unavailable)")
	}
	var b strings.Builder
	desc := string(p.mode) + " · ↑↓/jk navigate · type filters · backspace edits · ↵ expand/select"
	if p.mode == codeMapModeHere {
		desc += " · a attach all"
	}
	desc += " · r rebuild · esc closes"
	if p.filter != "" {
		desc = fmt.Sprintf("filter %q · %s", p.filter, desc)
	}
	b.WriteString(renderMenuHeader("Code map", desc, width))
	if p.loading {
		b.WriteString(styleMeta.Render("  building code map…"))
		return strings.TrimRight(b.String(), "\n")
	}
	if p.err != "" {
		b.WriteString(styleError.Render("  " + p.err))
		return strings.TrimRight(b.String(), "\n")
	}
	if p.diagram != "" {
		b.WriteString(stylePaletteItem.Render(p.diagram))
		if p.status != "" {
			b.WriteString("\n" + styleMeta.Render("  "+p.status))
		}
		b.WriteString("\n" + styleFooter.Render("modes: /map · /map deps <path> · /map dependents <path> · /map impact [--depth N|all] <path> · /map cycles [path] · /map diagram [--export <path>] [path]"))
		return strings.TrimRight(b.String(), "\n")
	}
	if p.subsystem != "" {
		b.WriteString(stylePaletteItem.Render(p.subsystem))
		if p.status != "" {
			b.WriteString("\n" + styleMeta.Render("  "+p.status))
		}
		b.WriteString("\n" + styleFooter.Render("modes: /map · /map deps <path> · /map dependents <path> · /map impact [--depth N|all] <path> · /map cycles [path] · /map diagram [--export <path>] [path]"))
		return strings.TrimRight(b.String(), "\n")
	}
	if len(p.rows) == 0 {
		b.WriteString(styleEmpty.Render("  (no indexed files or symbols)"))
		return strings.TrimRight(b.String(), "\n")
	}
	start := 0
	if p.cursor >= codeMapVisibleRows {
		start = p.cursor - codeMapVisibleRows + 1
	}
	end := min(len(p.rows), start+codeMapVisibleRows)
	labelWidth := maxCodeMapLabelWidth(p, start, end)
	for i := start; i < end; i++ {
		row := p.rows[i]
		n, _ := p.index.Node(row.id)
		label := strings.Repeat("  ", row.depth) + codeMapMarker(p, row.id, n) + codeMapLabel(n)
		desc := codeMapDesc(n, width-labelWidth-8)
		if row.label != "" {
			desc = row.label + " · " + desc
		}
		bodyRow := strings.Count(b.String(), "\n")
		h.row(bodyRow, i)
		b.WriteString(renderMenuItem(menuItemOpts{Label: label, LabelWidth: labelWidth, Desc: desc, Cursor: i == p.cursor}))
		b.WriteString("\n")
	}
	if p.status != "" {
		b.WriteString("\n" + styleMeta.Render("  "+p.status))
	}
	b.WriteString("\n" + styleFooter.Render("modes: /map · /map deps <path> · /map dependents <path> · /map impact [--depth N|all] <path> · /map cycles [path] · /map diagram [--export <path>] [path]"))
	return strings.TrimRight(b.String(), "\n")
}

func cycleLabel(cycle []codemap.Node) string {
	parts := make([]string, 0, len(cycle)+1)
	for _, n := range cycle {
		parts = append(parts, n.RelPath)
	}
	if len(cycle) > 0 {
		parts = append(parts, cycle[0].RelPath)
	}
	return strings.Join(parts, " -> ")
}

func maxCodeMapLabelWidth(p *codeMapPickerState, start, end int) int {
	w := 20
	for i := start; i < end; i++ {
		n, _ := p.index.Node(p.rows[i].id)
		l := len(strings.Repeat("  ", p.rows[i].depth) + codeMapMarker(p, p.rows[i].id, n) + codeMapLabel(n))
		if l > w {
			w = l
		}
	}
	if w > 48 {
		return 48
	}
	return w
}

func codeMapMarker(p *codeMapPickerState, id codemap.NodeID, n codemap.Node) string {
	if n.Kind != codemap.NodeDirectory && n.Kind != codemap.NodeFile {
		return "  "
	}
	if p.expanded[id] {
		return "▾ "
	}
	return "▸ "
}

func codeMapLabel(n codemap.Node) string {
	switch n.Kind {
	case codemap.NodeDirectory:
		if n.RelPath == "." {
			return "."
		}
		return n.Name + "/"
	case codemap.NodeFile:
		return n.Name
	case codemap.NodeSymbol:
		return n.Name
	default:
		return n.Name
	}
}

func codeMapDesc(n codemap.Node, width int) string {
	if width < 20 {
		width = 20
	}
	var desc string
	switch n.Kind {
	case codemap.NodeDirectory:
		desc = fmt.Sprintf("dir · %d files · %d symbols · %d LOC", n.Stats.Files, n.Stats.Symbols, n.Stats.LOC)
	case codemap.NodeFile:
		desc = fmt.Sprintf("file · %s · %d LOC · %d symbols · %d exported", n.Language, n.Stats.LOC, n.Stats.Symbols, n.Stats.Exported)
	case codemap.NodeSymbol:
		desc = fmt.Sprintf("%s · %s:%d", n.Symbol.Kind, n.RelPath, n.Symbol.Range.Start.Line+1)
	}
	return truncateForRender(desc, width)
}
