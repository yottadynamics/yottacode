package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/recall"
	"github.com/yottadynamics/yottacode/internal/session"
)

// recallPreviewPageSize is the number of hit cards shown in preview at once.
// It keeps the overlay bounded while PgUp/PgDn still moves through long result
// sets quickly.
const recallPreviewPageSize = 5

// recallPickerState holds one transient /recall results overlay. The hits are
// search/navigation context, so the picker deliberately renders below the
// cmdline instead of appending results to the persistent conversation transcript.
type recallPickerState struct {
	query  string
	groups []recallSessionGroup
	cursor int

	// preview flips the overlay from the grouped session list into a focused
	// preview of the selected session's matching snippets. The first Enter only
	// previews; a second Enter from preview resumes, preventing accidental
	// session switches while browsing search results.
	preview bool

	// previewOffset scrolls the selected session's matching snippets inside the
	// preview panel. Long-running sessions can produce many hits, so the preview
	// needs local navigation without losing the selected session row.
	previewOffset int

	// summarized mirrors /sessions' summarized-resume toggle for recall results.
	// Large remembered sessions are often exactly where summarized resume is
	// safest, so the preview exposes a one-key opt-in before Enter resumes.
	summarized bool
}

// recallSessionGroup collapses multiple matching messages from the same saved
// session into one picker row. This avoids noisy duplicate rows when a query
// matches several turns in one long conversation.
type recallSessionGroup struct {
	SessionID   string
	SessionName string
	Model       string
	Created     time.Time
	Hits        []recall.Hit
	Messages    []adapter.Message
}

// openRecallPicker installs a results overlay for a successful /recall query.
// The search has already run; this method groups hits by session so the user
// chooses a conversation first, then previews its matching snippets.
func (m *Model) openRecallPicker(query string, hits []recall.Hit) {
	m.recallPicker = &recallPickerState{
		query:  query,
		groups: groupRecallHits(hits),
		cursor: 0,
	}
	m.recallPickerOpen = true
}

// groupRecallHits preserves the search ranking order while collapsing later
// hits for a session into the first row for that session.
func groupRecallHits(hits []recall.Hit) []recallSessionGroup {
	byID := make(map[string]int, len(hits))
	groups := make([]recallSessionGroup, 0, len(hits))
	for _, h := range hits {
		if idx, ok := byID[h.SessionID]; ok {
			groups[idx].Hits = append(groups[idx].Hits, h)
			continue
		}
		byID[h.SessionID] = len(groups)
		groups = append(groups, recallSessionGroup{
			SessionID:   h.SessionID,
			SessionName: h.SessionName,
			Model:       h.Model,
			Created:     h.Created,
			Hits:        []recall.Hit{h},
		})
	}
	for i := range groups {
		groups[i].loadContext()
	}
	return groups
}

// loadContext best-effort loads the saved session messages for richer previews.
// Recall still works if the session file disappeared or is unreadable; the
// renderer falls back to the indexed FTS snippet in that case.
func (g *recallSessionGroup) loadContext() {
	loaded, err := session.Load(g.SessionID)
	if err != nil || loaded == nil {
		return
	}
	g.Messages = loaded.Messages
	if g.SessionName == "" {
		g.SessionName = loaded.Name
	}
	if g.Model == "" {
		g.Model = loaded.Model
	}
	if g.Created.IsZero() {
		g.Created = loaded.Created
	}
}

// updateRecallPicker handles the /recall results overlay. Enter previews the
// selected session first; Enter again from preview resumes through the same path
// as /sessions so save/load/rebuild logic stays centralized. Esc returns to the
// list from preview, or to the slash palette from the list.
func (m Model) updateRecallPicker(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.recallPicker
	if p == nil {
		m.recallPickerOpen = false
		return m, nil
	}
	if p.preview {
		return m.updateRecallPreview(msg)
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.recallPickerOpen = false
		m.recallPicker = nil
		m.openSlashPalette()
		return m, nil
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
			p.previewOffset = 0
		}
		return m, nil
	case tea.KeyDown:
		if p.cursor < len(p.groups)-1 {
			p.cursor++
			p.previewOffset = 0
		}
		return m, nil
	case tea.KeyEnter:
		if p.cursor < len(p.groups) {
			p.preview = true
			p.previewOffset = 0
		}
		return m, nil
	}
	return m, nil
}

func (m Model) updateRecallPreview(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.recallPicker
	if p == nil {
		m.recallPickerOpen = false
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		p.preview = false
		return m, nil
	case tea.KeyUp:
		if p.previewOffset > 0 {
			p.previewOffset--
		}
		return m, nil
	case tea.KeyDown:
		if p.cursor < len(p.groups) && p.previewOffset < len(p.groups[p.cursor].Hits)-1 {
			p.previewOffset++
		}
		return m, nil
	case tea.KeyPgUp:
		p.previewOffset = max(0, p.previewOffset-recallPreviewPageSize)
		return m, nil
	case tea.KeyPgDown:
		if p.cursor < len(p.groups) {
			p.previewOffset = min(max(0, len(p.groups[p.cursor].Hits)-1), p.previewOffset+recallPreviewPageSize)
		}
		return m, nil
	case tea.KeyEnter:
		if p.cursor >= len(p.groups) {
			return m, nil
		}
		id := p.groups[p.cursor].SessionID
		summarized := p.summarized
		m.recallPickerOpen = false
		m.recallPicker = nil
		return m.resumeSession(id, summarized)
	}
	if msg.Type == tea.KeyRunes && string(msg.Runes) == "s" {
		p.summarized = !p.summarized
		return m, nil
	}
	return m, nil
}

func renderRecallPicker(p *recallPickerState, width int) string {
	_ = width
	if p.preview {
		return renderRecallPreview(p)
	}
	return renderRecallList(p)
}

func renderRecallList(p *recallPickerState) string {
	var b strings.Builder
	matchCount := 0
	for _, g := range p.groups {
		matchCount += len(g.Hits)
	}
	b.WriteString(renderMenuHeader("Recall",
		fmt.Sprintf("%d session(s), %d hit(s) for %q — Enter previews the selected session.", len(p.groups), matchCount, p.query)))
	b.WriteString("\n")
	if len(p.groups) == 0 {
		b.WriteString(styleEmpty.Render("  (no matches)"))
	} else {
		for i, g := range p.groups {
			b.WriteString(renderMenuItem(menuItemOpts{
				Label:      recallGroupLabel(g),
				LabelWidth: 28,
				Desc:       recallGroupDesc(g),
				Cursor:     i == p.cursor,
			}))
			b.WriteString("\n")
			if snippet := recallPickerSnippet(g.Hits[0].Snippet); snippet != "" {
				b.WriteString(styleMeta.Render("    " + snippet))
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n")
	b.WriteString(styleFooter.Render("↵ preview · esc back · ↑↓ navigate"))
	return strings.TrimRight(b.String(), "\n")
}

func renderRecallPreview(p *recallPickerState) string {
	var b strings.Builder
	if p.cursor >= len(p.groups) {
		b.WriteString(styleEmpty.Render("(no selected recall result)"))
		return b.String()
	}
	g := p.groups[p.cursor]
	start := min(max(0, p.previewOffset), max(0, len(g.Hits)-1))
	end := min(len(g.Hits), start+recallPreviewPageSize)
	state := "off"
	if p.summarized {
		state = "on"
	}
	b.WriteString(renderMenuHeader("Recall preview",
		fmt.Sprintf("%s — showing hits %d-%d of %d. Enter resumes this session.", recallGroupLabel(g), start+1, end, len(g.Hits))))
	b.WriteString("\n")
	b.WriteString(styleMeta.Render("  " + recallGroupDesc(g)))
	b.WriteString("\n\n")
	for i := start; i < end; i++ {
		h := g.Hits[i]
		role := string(h.Role)
		if role == "" {
			role = "message"
		}
		fmt.Fprintf(&b, "  %s\n", styleUserHeader.Render(fmt.Sprintf("%d. %s", i+1, role)))
		b.WriteString(renderRecallHitContext(g, h))
	}
	b.WriteString("\n")
	b.WriteString(styleFooter.Render(fmt.Sprintf("↵ resume · s summarized (%s) · ↑↓/pg scroll · esc results", state)))
	return strings.TrimRight(b.String(), "\n")
}

func renderRecallHitContext(g recallSessionGroup, h recall.Hit) string {
	if len(g.Messages) == 0 || h.MsgIndex < 0 || h.MsgIndex >= len(g.Messages) {
		if snippet := recallPickerSnippet(h.Snippet); snippet != "" {
			return "    " + snippet + "\n"
		}
		return ""
	}
	start := max(0, h.MsgIndex-1)
	end := min(len(g.Messages), h.MsgIndex+2)
	var b strings.Builder
	for idx := start; idx < end; idx++ {
		msg := g.Messages[idx]
		prefix := "    "
		if idx == h.MsgIndex {
			prefix = "  ❯ "
		}
		role := string(msg.Role)
		if role == "" {
			role = "message"
		}
		content := recallPickerSnippet(msg.Content)
		if content == "" {
			content = recallPickerMessageFallback(msg)
		}
		fmt.Fprintf(&b, "%s%s: %s\n", prefix, role, content)
	}
	return b.String()
}

func recallPickerMessageFallback(msg adapter.Message) string {
	if len(msg.ToolCalls) == 0 {
		return "(empty message)"
	}
	return fmt.Sprintf("%d tool call(s)", len(msg.ToolCalls))
}

func recallGroupLabel(g recallSessionGroup) string {
	if g.SessionName != "" {
		return g.SessionName
	}
	return g.SessionID
}

func recallGroupDesc(g recallSessionGroup) string {
	age := formatRecallAge(time.Since(g.Created))
	model := g.Model
	if model == "" {
		model = "unknown model"
	}
	id := g.SessionID
	if g.SessionName != "" {
		id = shortenMiddle(g.SessionID, 22)
	}
	hitLabel := "hit"
	if len(g.Hits) != 1 {
		hitLabel = "hits"
	}
	return fmt.Sprintf("%d %s · %s · %s · %s", len(g.Hits), hitLabel, id, age, model)
}

func recallPickerSnippet(snippet string) string {
	snippet = strings.Join(strings.Fields(strings.ReplaceAll(snippet, "\n", " ")), " ")
	return shortenMiddle(snippet, 120)
}
