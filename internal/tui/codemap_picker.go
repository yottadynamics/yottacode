package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/codemap"
)

const codeMapVisibleRows = 28
const codemapDefaultImpactDepth = codemap.MaxDepthAll

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
	index     *codemap.CodeIndex
	rows      []codeMapRow
	mode      codeMapMode
	depth     int
	expanded  map[codemap.NodeID]bool
	cursor    int
	filter    string
	hereFiles []string
	diagram   string
	status    string
	loading   bool
	err       string
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

func (m Model) openCodeMapPicker(mode codeMapMode, filter string, depth int) (Model, tea.Cmd) {
	if m.codeMapProvider == nil {
		m.appendLine(styleError.Render("/map requires --experimental code_map"))
		return m, nil
	}
	hereFiles := []string(nil)
	if mode == codeMapModeHere {
		hereFiles = m.codeMapHereFiles(filter)
	}
	m.codeMapPicker = &codeMapPickerState{mode: mode, depth: depth, filter: filter, hereFiles: hereFiles, expanded: map[codemap.NodeID]bool{}, loading: true, status: "building code map…"}
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

func (m Model) updateCodeMapPicker(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.codeMapPicker
	if p == nil {
		m.codeMapPickerOpen = false
		return m, nil
	}
	switch msg.Type {
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
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		s := string(msg.Runes)
		switch s {
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
		default:
			p.filter += s
			p.rebuildRows()
		}
	}
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

func (p *codeMapPickerState) rebuildHereRows() {
	if len(p.hereFiles) == 0 {
		p.status = "no changed files found; pass a path: /map here <path>"
		p.clampCursor()
		return
	}
	seen := map[codemap.NodeID]bool{}
	for _, path := range p.hereFiles {
		matches := p.index.Filter(path, 20)
		for _, n := range matches {
			if n.Kind != codemap.NodeFile || seen[n.ID] {
				continue
			}
			p.rows = append(p.rows, codeMapRow{id: n.ID, label: "changed"})
			seen[n.ID] = true
			for _, dep := range p.index.Dependencies(n.RelPath, 8) {
				if !seen[dep.ID] {
					p.rows = append(p.rows, codeMapRow{id: dep.ID, label: "imports"})
					seen[dep.ID] = true
				}
			}
			for _, dependent := range p.index.Dependents(n.RelPath, 8) {
				if !seen[dependent.ID] {
					p.rows = append(p.rows, codeMapRow{id: dependent.ID, label: "imported by"})
					seen[dependent.ID] = true
				}
			}
			for _, sym := range p.index.SymbolsForFile(n.RelPath) {
				if !seen[sym.ID] {
					p.rows = append(p.rows, codeMapRow{id: sym.ID, depth: 1, label: "symbol"})
					seen[sym.ID] = true
				}
			}
		}
	}
	if len(p.rows) == 0 {
		p.status = "no indexed matches for changed files"
	} else {
		p.status = fmt.Sprintf("%d relevant entries around %d file(s)", len(p.rows), len(p.hereFiles))
	}
	p.clampCursor()
}

func (p *codeMapPickerState) clampCursor() {
	if p.cursor >= len(p.rows) {
		p.cursor = max(0, len(p.rows)-1)
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

func renderCodeMapPicker(p *codeMapPickerState, width int) string {
	if p == nil {
		return styleEmpty.Render("(code map unavailable)")
	}
	var b strings.Builder
	desc := string(p.mode) + " · ↑↓/jk navigate · type filters · backspace edits · ↵ expand/select · r rebuild · esc closes"
	if p.filter != "" {
		desc = fmt.Sprintf("filter %q · %s", p.filter, desc)
	}
	b.WriteString(renderMenuHeader("Code map", desc))
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
		b.WriteString("\n" + styleFooter.Render("modes: /map · /map deps <path> · /map dependents <path> · /map impact [--depth N|all] <path> · /map cycles [path] · /map diagram [path]"))
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
		b.WriteString(renderMenuItem(menuItemOpts{Label: label, LabelWidth: labelWidth, Desc: desc, Cursor: i == p.cursor}))
		b.WriteString("\n")
	}
	if p.status != "" {
		b.WriteString("\n" + styleMeta.Render("  "+p.status))
	}
	b.WriteString("\n" + styleFooter.Render("modes: /map · /map deps <path> · /map dependents <path> · /map impact [--depth N|all] <path> · /map cycles [path] · /map diagram [path]"))
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
