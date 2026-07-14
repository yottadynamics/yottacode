package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/config"
	"github.com/yottadynamics/yottacode/internal/memory"
	"github.com/yottadynamics/yottacode/internal/wizard"
)

// projectMemorySizeWarnBytes is the chars-in-YOTTACODE.md (or USER.md)
// threshold above which we emit a "large file will impact performance"
// notice on startup.
const projectMemorySizeWarnBytes = 40_000

// editorDoneMsg fires when the /memory picker's vim subprocess returns.
type editorDoneMsg struct {
	err  error
	path string
}

// cmdMemory opens the memory picker overlay, or dispatches subcommands.
func cmdMemory(m Model, args []string) (Model, tea.Cmd) {
	if len(args) > 0 && args[0] == "search" {
		return m.openMemorySearch(strings.Join(args[1:], " "))
	}
	m.openMemoryPicker()
	return m, nil
}

// openMemorySearch scores saved memories against the query and shows the
// ranked hits in an interactive overlay — scroll with ↑↓, open one with
// Enter, Esc to go back. Results render in the picker overlay, NOT the
// session transcript, so a search never pollutes the conversation
// scrollback. An empty query just opens the picker root menu.
func (m Model) openMemorySearch(query string) (Model, tea.Cmd) {
	m.openMemoryPicker()
	query = strings.TrimSpace(query)
	if query == "" {
		return m, nil // no query → land on the picker root menu
	}
	p := m.memoryPicker
	p.mode = memorySearchMode
	p.searchQuery = query
	hits, err := rankMemories(m.cwd, query)
	if err != nil {
		p.searchMessage = "load failed: " + err.Error()
		return m, nil
	}
	p.searchResults = hits
	return m, nil
}

// rankMemories loads every saved memory and returns the matches (score >
// 0) for query, ranked by the same BM25 the agent's retrieval uses.
func rankMemories(cwd, query string) ([]memory.Scored, error) {
	loaded, err := memory.Load(cwd)
	if err != nil {
		return nil, err
	}
	var all []memory.MemoryEntry
	all = append(all, loaded.UserMemories...)
	all = append(all, loaded.ProjectMemories...)
	if len(all) == 0 {
		return nil, nil
	}
	corpus := memory.BuildCorpus(all)
	ranked := corpus.Rank(memory.StemExpandTokenize(query))
	var hits []memory.Scored
	for _, s := range ranked {
		if s.Score > 0 {
			hits = append(hits, s)
		}
	}
	return hits, nil
}

type memoryPickerMode int

const (
	memoryRootMode memoryPickerMode = iota
	memoryBrowseMode
	memorySearchInputMode
	memorySearchMode
)

type memoryPickerState struct {
	mode memoryPickerMode

	cursor int

	userPath    string
	projectPath string

	userMemoryDir    string
	projectMemoryDir string

	browseScope   string
	browseDir     string
	entries       []memory.MemoryEntry
	entryCursor   int
	browseMessage string

	// Search: `/memory search <q>` and the in-picker "Search memories" row
	// both land here. Results render in the overlay (scroll + open), never
	// the session transcript.
	searchInput   textinput.Model
	searchQuery   string
	searchResults []memory.Scored
	searchCursor  int
	searchMessage string

	showEnableSemanticRow bool
}

func (p *memoryPickerState) rowCount() int {
	n := 6
	if p.showEnableSemanticRow {
		n++
	}
	return n
}

func (m *Model) openMemoryPicker() {
	st := &memoryPickerState{}
	if home, err := os.UserHomeDir(); err == nil {
		st.userPath = filepath.Join(home, ".yottacode", "USER.md")
	}
	if m.cwd != "" {
		st.projectPath = filepath.Join(m.cwd, ".yottacode", "YOTTACODE.md")
	}
	if dir, err := memory.UserMemoryDir(); err == nil {
		st.userMemoryDir = dir
	}
	if dir, err := memory.ProjectMemoryDir(m.cwd); err == nil {
		st.projectMemoryDir = dir
	}
	st.showEnableSemanticRow = (m.embedClient == nil)
	m.memoryPicker = st
	m.memoryPickerOpen = true
}

func (m Model) updateMemoryPicker(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.memoryPicker == nil {
		m.memoryPickerOpen = false
		return m, nil
	}
	p := m.memoryPicker
	if p.mode == memoryBrowseMode {
		return m.updateMemoryBrowse(msg)
	}
	if p.mode == memorySearchInputMode {
		return m.updateMemorySearchInput(msg)
	}
	if p.mode == memorySearchMode {
		return m.updateMemorySearch(msg)
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.memoryPickerOpen = false
		m.memoryPicker = nil
		m.openSlashPalette()
		return m, nil
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if p.cursor < p.rowCount()-1 {
			p.cursor++
		}
		return m, nil
	case tea.KeyEnter:
		return m.commitMemoryPicker()
	}
	return m, nil
}

func (m Model) commitMemoryPicker() (Model, tea.Cmd) {
	p := m.memoryPicker
	if p == nil {
		m.memoryPickerOpen = false
		return m, nil
	}
	switch p.cursor {
	case 0:
		path := p.projectPath
		m.memoryPickerOpen = false
		m.memoryPicker = nil
		if path == "" {
			m.appendLine(styleError.Render("[memory] project path unavailable"))
			return m, nil
		}
		return m.openInVim(path)
	case 1:
		path := p.userPath
		m.memoryPickerOpen = false
		m.memoryPicker = nil
		if path == "" {
			m.appendLine(styleError.Render("[memory] user path unavailable"))
			return m, nil
		}
		return m.openInVim(path)
	case 2:
		return m.enterMemoryBrowse("user", p.userMemoryDir)
	case 3:
		return m.enterMemoryBrowse("project", p.projectMemoryDir)
	case 4:
		return m.enterMemorySearchInput()
	case 5:
		return m.runMemoryReindex()
	case 6:
		if p.showEnableSemanticRow {
			return m.runEmbedSetup()
		}
	}
	return m, nil
}

func (m Model) runMemoryReindex() (Model, tea.Cmd) {
	m.memoryPickerOpen = false
	m.memoryPicker = nil
	// The Available probe and per-memory Embed calls are blocking HTTP to
	// Ollama — running them on the Update goroutine froze the whole UI
	// (seconds to minutes). Do them in a tea.Cmd and render the result via
	// memoryReindexDoneMsg.
	m.appendLine(styleAuto.Render("[memory] reindexing embeddings…"))
	return m, memoryReindexCmd(m.parentCtx, m.embedClient, m.fileCfg.Retrieval.EmbeddingModel, m.cwd)
}

// memoryReindexDoneMsg carries the result lines from an off-thread memory
// reindex back to the Update goroutine.
type memoryReindexDoneMsg struct {
	lines   []string
	isError bool
}

// memoryReindexCmd runs the embedding-availability probe and the
// per-memory embed/write loop off the Update goroutine so a slow Ollama
// doesn't freeze the TUI.
func memoryReindexCmd(ctx context.Context, client *memory.EmbedClient, model, cwd string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			client = memory.NewEmbedClient("", model)
			if !client.Available(ctx) {
				return memoryReindexDoneMsg{isError: true, lines: []string{fmt.Sprintf(
					"[memory] embedding model %q not available — is Ollama running with the model installed?  Try: ollama pull %s",
					client.Model, client.Model)}}
			}
		}
		loaded, err := memory.Load(cwd)
		if err != nil {
			return memoryReindexDoneMsg{isError: true, lines: []string{"[memory] " + err.Error()}}
		}
		var all []memory.MemoryEntry
		all = append(all, loaded.UserMemories...)
		all = append(all, loaded.ProjectMemories...)
		if len(all) == 0 {
			return memoryReindexDoneMsg{lines: []string{"[memory] no memories to index"}}
		}
		var indexed, skipped int
		for _, e := range all {
			vecPath := memory.VecPath(e.Path)
			if !memory.NeedsReembed(vecPath, client.Model) {
				skipped++
				continue
			}
			text := e.Name + " " + e.Description + " " + e.Body
			vec, err := client.Embed(ctx, text)
			if err != nil {
				continue
			}
			if err := memory.WriteVecWithModel(vecPath, vec, client.Model); err != nil {
				continue
			}
			indexed++
		}
		return memoryReindexDoneMsg{lines: []string{fmt.Sprintf("[memory] reindex: %d embedded, %d up-to-date", indexed, skipped)}}
	}
}

func (m Model) enterMemoryBrowse(scope, dir string) (Model, tea.Cmd) {
	p := m.memoryPicker
	if p == nil {
		return m, nil
	}
	entries, err := scanMemoryEntriesForBrowse(scope, m.cwd)
	if err != nil {
		m.memoryPickerOpen = false
		m.memoryPicker = nil
		m.appendLine(styleError.Render("[memory] " + err.Error()))
		return m, nil
	}
	p.browseScope = scope
	p.browseDir = dir
	p.entries = entries
	p.entryCursor = 0
	p.browseMessage = ""
	p.mode = memoryBrowseMode
	return m, nil
}

func scanMemoryEntriesForBrowse(scope, cwd string) ([]memory.MemoryEntry, error) {
	loaded, err := memory.Load(cwd)
	if err != nil {
		return nil, err
	}
	switch scope {
	case "user":
		return loaded.UserMemories, nil
	case "project":
		return loaded.ProjectMemories, nil
	default:
		return nil, fmt.Errorf("unknown scope %q", scope)
	}
}

func (m Model) updateMemoryBrowse(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.memoryPicker
	if p == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		p.mode = memoryRootMode
		p.entries = nil
		p.browseMessage = ""
		p.browseScope = ""
		return m, nil
	case tea.KeyUp:
		if p.entryCursor > 0 {
			p.entryCursor--
		}
		return m, nil
	case tea.KeyDown:
		if p.entryCursor < len(p.entries)-1 {
			p.entryCursor++
		}
		return m, nil
	case tea.KeyEnter:
		return m.commitMemoryBrowseOpen()
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case 'd':
			return m.commitMemoryBrowseDelete()
		case 'f':
			return m.commitMemoryBrowseOpenFolder()
		}
	}
	return m, nil
}

func (m Model) commitMemoryBrowseOpen() (Model, tea.Cmd) {
	p := m.memoryPicker
	if p == nil || p.entryCursor >= len(p.entries) {
		return m, nil
	}
	path := p.entries[p.entryCursor].Path
	m.memoryPickerOpen = false
	m.memoryPicker = nil
	return m.openInVim(path)
}

func (m Model) commitMemoryBrowseDelete() (Model, tea.Cmd) {
	p := m.memoryPicker
	if p == nil || p.entryCursor >= len(p.entries) {
		return m, nil
	}
	target := p.entries[p.entryCursor]
	if err := os.Remove(target.Path); err != nil {
		p.browseMessage = "couldn't delete " + target.Name + ": " + err.Error()
		return m, nil
	}
	memory.DeleteVec(target.Path)
	if err := memory.RegenerateMemoryIndex(p.browseScope, m.cwd); err != nil {
		p.browseMessage = "deleted " + target.Name + " but index update failed: " + err.Error()
	} else {
		p.browseMessage = "deleted " + target.Name
	}
	entries, _ := scanMemoryEntriesForBrowse(p.browseScope, m.cwd)
	p.entries = entries
	if p.entryCursor >= len(p.entries) && p.entryCursor > 0 {
		p.entryCursor = len(p.entries) - 1
	}
	if len(p.entries) == 0 {
		p.entryCursor = 0
	}
	m, _ = reloadMemoryNow(m, "")
	m.memoryPicker = p
	return m, nil
}

func (m Model) commitMemoryBrowseOpenFolder() (Model, tea.Cmd) {
	p := m.memoryPicker
	if p == nil || p.browseDir == "" {
		return m, nil
	}
	_ = os.MkdirAll(p.browseDir, 0o700)
	openInFileManager(p.browseDir)
	p.browseMessage = "opened " + abbrevHome(p.browseDir)
	return m, nil
}

func (m Model) openInVim(path string) (Model, tea.Cmd) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.appendLine(styleError.Render("[memory] mkdir: " + err.Error()))
		return m, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// 0o600 to match tool-written memory files (atomicWrite uses
		// 0o600); these hold the same user data class.
		if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
			m.appendLine(styleError.Render("[memory] create: " + err.Error()))
			return m, nil
		}
	}
	editor := "vim"
	if _, err := exec.LookPath(editor); err != nil {
		editor = "vi"
	}
	cmd := exec.Command(editor, path)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{err: err, path: path}
	})
}

func openInFileManager(path string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("explorer", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	_ = cmd.Start()
	go func() { _ = cmd.Wait() }()
}

func renderMemoryPicker(p *memoryPickerState, _ int) string {
	if p.mode == memoryBrowseMode {
		return renderMemoryBrowse(p)
	}
	if p.mode == memorySearchInputMode {
		return renderMemorySearchInput(p)
	}
	if p.mode == memorySearchMode {
		return renderMemorySearch(p)
	}
	var b strings.Builder
	b.WriteString(renderMenuHeader("Memory",
		"Pick a memory file to edit, or browse agent-managed memories."))
	b.WriteString("\n")

	rows := []struct {
		label string
		desc  string
	}{
		{"Project context", memoryRowDesc("Saved at", p.projectPath)},
		{"User preferences", memoryRowDesc("Saved at", p.userPath)},
		{"Browse user memories", memoryDirRowDesc(p.userMemoryDir, "cross-project agent memories")},
		{"Browse project memories", memoryDirRowDesc(p.projectMemoryDir, "this-repo agent memories")},
		{"Search memories", "rank saved memories by relevance, then open one"},
		{"Reindex embeddings", "regenerate .vec sidecars for semantic retrieval"},
	}
	if p.showEnableSemanticRow {
		rows = append(rows, struct {
			label string
			desc  string
		}{"Enable semantic search", "pull an embedding model via Ollama"})
	}
	for i, r := range rows {
		b.WriteString(renderMenuItem(menuItemOpts{
			Number:     i + 1,
			Label:      r.label,
			LabelWidth: 26,
			Desc:       r.desc,
			Cursor:     i == p.cursor,
		}))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleFooter.Render("↵ confirm · esc cancel · ↑↓ navigate"))

	return strings.TrimRight(b.String(), "\n")
}

func renderMemoryBrowse(p *memoryPickerState) string {
	var b strings.Builder
	header := "Browse " + p.browseScope + "-scope memories"
	b.WriteString(renderMenuHeader(header,
		"Pick a memory to open, delete, or open the folder in your file manager."))
	b.WriteString("\n")
	if p.browseDir != "" {
		fmt.Fprintf(&b, "  %s\n\n", styleAuto.Render(abbrevHome(p.browseDir)))
	}
	if p.browseMessage != "" {
		b.WriteString(styleAuto.Render("  · " + p.browseMessage))
		b.WriteString("\n\n")
	}
	if len(p.entries) == 0 {
		b.WriteString(styleEmpty.Render("  (no memories)"))
	} else {
		for i, e := range p.entries {
			label := e.Name
			if e.Type != "" {
				label = e.Name + " [" + e.Type + "]"
			}
			desc := e.Description
			if desc == "" {
				desc = abbrevHome(e.Path)
			}
			b.WriteString(renderMenuItem(menuItemOpts{
				Label:      label,
				LabelWidth: 32,
				Desc:       desc,
				Cursor:     i == p.entryCursor,
			}))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(styleFooter.Render("↵ open · d delete · f open folder · esc back · ↑↓ navigate"))
	return strings.TrimRight(b.String(), "\n")
}

// enterMemorySearchInput switches the picker into a text-input prompt for
// a search query (the "Search memories" root row). Reuses the bubbles
// textinput like the other pickers.
func (m Model) enterMemorySearchInput() (Model, tea.Cmd) {
	p := m.memoryPicker
	if p == nil {
		return m, nil
	}
	in := textinput.New()
	in.Placeholder = "search saved memories…"
	in.CharLimit = 128
	in.Focus()
	p.searchInput = in
	p.searchQuery = ""
	p.searchResults = nil
	p.searchCursor = 0
	p.searchMessage = ""
	p.mode = memorySearchInputMode
	return m, nil
}

// updateMemorySearchInput drives the query text box: Enter runs the
// search and switches to the results list, Esc returns to the root menu,
// everything else is fed to the textinput.
func (m Model) updateMemorySearchInput(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.memoryPicker
	if p == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		p.searchInput.Blur()
		p.mode = memoryRootMode
		return m, nil
	case tea.KeyEnter:
		q := strings.TrimSpace(p.searchInput.Value())
		if q == "" {
			p.searchMessage = "type a query, then press enter"
			return m, nil
		}
		p.searchQuery = q
		p.searchCursor = 0
		hits, err := rankMemories(m.cwd, q)
		if err != nil {
			p.searchMessage = "load failed: " + err.Error()
		} else {
			p.searchResults = hits
			p.searchMessage = ""
		}
		p.searchInput.Blur()
		p.mode = memorySearchMode
		return m, nil
	}
	var cmd tea.Cmd
	p.searchInput, cmd = p.searchInput.Update(msg)
	return m, cmd
}

// updateMemorySearch drives the ranked results list. Enter opens the
// highlighted memory in the editor but KEEPS the picker open in search
// mode, so exiting the editor returns to these same results instead of
// dropping back to the conversation (the query is never lost).
func (m Model) updateMemorySearch(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.memoryPicker
	if p == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		// Back to the root menu (mirrors browse-mode Esc).
		p.mode = memoryRootMode
		p.searchResults = nil
		p.searchQuery = ""
		p.searchCursor = 0
		p.searchMessage = ""
		return m, nil
	case tea.KeyUp:
		if p.searchCursor > 0 {
			p.searchCursor--
		}
		return m, nil
	case tea.KeyDown:
		if p.searchCursor < len(p.searchResults)-1 {
			p.searchCursor++
		}
		return m, nil
	case tea.KeyEnter:
		if p.searchCursor < len(p.searchResults) {
			// Do NOT close the picker: editorDoneMsg reloads memory and the
			// overlay (still in search mode) re-renders these results, so the
			// user lands back on their search after exiting the editor.
			return m.openInVim(p.searchResults[p.searchCursor].Entry.Path)
		}
	}
	return m, nil
}

func renderMemorySearchInput(p *memoryPickerState) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Search memories",
		"Type a query and press enter. Esc to go back."))
	b.WriteString("\n")
	if p.searchMessage != "" {
		b.WriteString(styleError.Render("  · " + p.searchMessage))
		b.WriteString("\n\n")
	}
	b.WriteString("  " + p.searchInput.View())
	b.WriteString("\n\n")
	b.WriteString(styleFooter.Render("↵ search · esc back"))
	return strings.TrimRight(b.String(), "\n")
}

func renderMemorySearch(p *memoryPickerState) string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Search memories",
		fmt.Sprintf("Results for %q — open one, or Esc to go back.", p.searchQuery)))
	b.WriteString("\n")
	if p.searchMessage != "" {
		b.WriteString(styleError.Render("  · " + p.searchMessage))
		b.WriteString("\n\n")
	}
	if len(p.searchResults) == 0 {
		b.WriteString(styleEmpty.Render("  (no matches)"))
	} else {
		for i, s := range p.searchResults {
			e := s.Entry
			label := e.Name
			if e.Type != "" {
				label = e.Name + " [" + e.Type + "]"
			}
			scope := e.Scope
			if scope == "" {
				scope = "?"
			}
			desc := fmt.Sprintf("%s · %.2f", scope, s.Score)
			if e.Description != "" {
				desc += " · " + e.Description
			}
			b.WriteString(renderMenuItem(menuItemOpts{
				Label:      label,
				LabelWidth: 32,
				Desc:       desc,
				Cursor:     i == p.searchCursor,
			}))
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(styleFooter.Render("↵ open · esc back · ↑↓ navigate"))
	return strings.TrimRight(b.String(), "\n")
}

func memoryRowDesc(prefix, path string) string {
	if path == "" {
		return "(unavailable)"
	}
	return prefix + " " + abbrevHome(path)
}

func memoryDirRowDesc(dir, fallback string) string {
	if dir == "" {
		return fallback
	}
	return abbrevHome(dir)
}

func reloadMemoryNow(m Model, notice string) (Model, tea.Cmd) {
	mem, err := memory.Load(m.cwd)
	if err != nil {
		m.appendLine(styleError.Render("[memory] reload failed: " + err.Error()))
		return m, nil
	}
	composed := composeSystemPrompt(m.baseSystemPrompt, m.providerProfile)
	newSys := memory.SystemPrompt(composed, mem)
	// Hold histMu across the system-message rewrite: reloadMemoryNow can run
	// (via /model, /memory) while a just-cancelled turn's agent goroutine is
	// still appending to sess.Messages under the same lock. An append that
	// reallocates copies every element, racing this Content/CacheHeadBytes
	// write. Mirrors recomposeSystemPromptWithSkills (skills_picker.go).
	m.histMu.Lock()
	for i := range m.sess.Messages {
		if m.sess.Messages[i].Role == adapter.RoleSystem {
			m.sess.Messages[i].Content = newSys
			m.sess.Messages[i].CacheHeadBytes = len(composed)
			break
		}
	}
	m.histMu.Unlock()
	m.memorySummary = mem.Summary().String()
	if notice != "" {
		summary := m.memorySummary
		if summary == "" {
			summary = "(no memory)"
		}
		m.appendLine(styleAuto.Render(notice + " — " + summary))
	}
	return m, nil
}

func (m *Model) emitMemorySizeWarnings() {
	type entry struct {
		label string
		path  string
	}
	var entries []entry
	if home, err := os.UserHomeDir(); err == nil {
		entries = append(entries, entry{"USER.md", filepath.Join(home, ".yottacode", "USER.md")})
	}
	if m.cwd != "" {
		entries = append(entries, entry{"YOTTACODE.md", filepath.Join(m.cwd, ".yottacode", "YOTTACODE.md")})
	}
	for _, e := range entries {
		info, err := os.Stat(e.path)
		if err != nil || info.IsDir() {
			continue
		}
		size := info.Size()
		if size <= projectMemorySizeWarnBytes {
			continue
		}
		m.appendLine(styleWatermark.Render(formatMemorySizeWarning(e.label, size)))
	}
}

func formatMemorySizeWarning(label string, size int64) string {
	return fmt.Sprintf("Large %s will impact performance (%.1fk chars > %.1fk) · /memory to edit",
		label,
		float64(size)/1000.0,
		float64(projectMemorySizeWarnBytes)/1000.0)
}

// ---------------------------------------------------------------------------
// Embed setup overlay — "Enable semantic search" from /memory picker
// ---------------------------------------------------------------------------

type embedSetupDoneMsg struct {
	model string
	err   error
}

func (m Model) runEmbedSetup() (Model, tea.Cmd) {
	m.memoryPickerOpen = false
	m.memoryPicker = nil
	m.embedSetupOpen = true
	m.embedSetupCursor = 0
	m.embedSetupPulling = false
	return m, nil
}

func (m Model) updateEmbedSetup(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.embedSetupPulling {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.embedSetupOpen = false
		return m, nil
	case tea.KeyUp:
		if m.embedSetupCursor > 0 {
			m.embedSetupCursor--
		}
	case tea.KeyDown:
		if m.embedSetupCursor < 1 {
			m.embedSetupCursor++
		}
	case tea.KeyEnter:
		models := []string{
			wizard.KnownEmbeddingModels[0].Name,
			wizard.KnownEmbeddingModels[1].Name,
		}
		chosen := models[m.embedSetupCursor]
		m.embedSetupPulling = true
		return m, func() tea.Msg {
			err := wizard.OllamaPull(m.parentCtx, "", chosen)
			return embedSetupDoneMsg{model: chosen, err: err}
		}
	}
	return m, nil
}

func (m Model) handleEmbedSetupDone(msg embedSetupDoneMsg) (Model, tea.Cmd) {
	m.embedSetupOpen = false
	m.embedSetupPulling = false
	if msg.err != nil {
		m.appendLine(styleError.Render("[memory] pull failed: " + msg.err.Error()))
		return m, nil
	}
	m.fileCfg.Retrieval.Strategy = "auto"
	m.fileCfg.Retrieval.EmbeddingModel = msg.model
	m.embedClient = memory.NewEmbedClient("", msg.model)

	cfgPath, _ := config.DefaultPath()
	if cfgPath != "" {
		if cfg, err := config.Load(cfgPath); err == nil {
			cfg.Retrieval.Strategy = "auto"
			cfg.Retrieval.EmbeddingModel = msg.model
			_ = config.Save(cfg, cfgPath)
		}
	}

	m.appendLine(styleAuto.Render(fmt.Sprintf(
		"[memory] %s pulled — semantic search enabled. Running reindex...", msg.model)))
	return m.runMemoryReindex()
}

func (m Model) renderEmbedSetup() string {
	var b strings.Builder
	b.WriteString(renderMenuHeader("Enable Semantic Search",
		"Pick an embedding model to pull via Ollama."))
	b.WriteString("\n")

	if m.embedSetupPulling {
		b.WriteString("  " + m.spinner.View() + " pulling model...\n")
		return strings.TrimRight(b.String(), "\n")
	}

	for i, em := range wizard.KnownEmbeddingModels {
		b.WriteString(renderMenuItem(menuItemOpts{
			Number:     i + 1,
			Label:      em.Name,
			LabelWidth: 22,
			Desc:       em.Desc + " · " + em.Size,
			Cursor:     i == m.embedSetupCursor,
		}))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleFooter.Render("enter pull · esc cancel · up/down navigate"))

	return strings.TrimRight(b.String(), "\n")
}
