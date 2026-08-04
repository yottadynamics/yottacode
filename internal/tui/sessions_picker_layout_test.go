package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
)

func gistRows() []session.SessionInfo {
	return []session.SessionInfo{
		{ID: "20260721-120951.089901", Model: "gpt-5.5", Messages: 2,
			Created: time.Now().Add(-time.Hour), Summary: "add retry logic to the fetcher"},
		{ID: "20260721-030501.213050", Model: "nvidia/nemotron-3-ultra-550b-a55b", Messages: 4,
			Created: time.Now().Add(-9 * time.Hour), Summary: "why is the build flaky?"},
	}
}

// TestSessionsRowLayout_WideShowsGistAndModel: with room to spare, both the
// gist and the full metadata render.
func TestSessionsRowLayout_WideShowsGistAndModel(t *testing.T) {
	lay := sessionsRowLayout(gistRows(), 160)
	if lay.gistWidth < minSummaryWidth {
		t.Fatalf("wide terminal should get a gist column, got %d", lay.gistWidth)
	}
	if lay.compactMeta {
		t.Error("wide terminal should not need to drop the model name")
	}
	desc := sessionPickerDesc(gistRows()[0], lay)
	if !strings.Contains(desc, "add retry logic") || !strings.Contains(desc, "gpt-5.5") {
		t.Errorf("wide row should carry gist and model: %q", desc)
	}
}

// TestSessionsRowLayout_DropsModelBeforeGist is the priority rule: the gist
// is what identifies a session, so a squeezed row sheds the model name
// first. Regression guard — sizing from the untruncated max metadata let a
// single long model id delete the gist column for the whole list.
func TestSessionsRowLayout_DropsModelBeforeGist(t *testing.T) {
	rows := gistRows()
	lay := sessionsRowLayout(rows, 80)
	if lay.gistWidth < minSummaryWidth {
		t.Fatalf("80 cols should still afford a gist, got %d", lay.gistWidth)
	}
	if !lay.compactMeta {
		t.Fatal("80 cols should have dropped the model to keep the gist")
	}
	desc := sessionPickerDesc(rows[1], lay)
	if strings.Contains(desc, "nvidia") {
		t.Errorf("compact row should not carry the model name: %q", desc)
	}
	if !strings.Contains(desc, "why is the build flaky?") {
		t.Errorf("compact row should still carry the gist: %q", desc)
	}
}

// TestSessionsRowLayout_NarrowFallsBackToMeta: below the useful minimum the
// gist is dropped rather than shown as a stub.
func TestSessionsRowLayout_NarrowFallsBackToMeta(t *testing.T) {
	lay := sessionsRowLayout(gistRows(), 50)
	if lay.gistWidth != 0 {
		t.Errorf("narrow terminal should drop the gist, got width %d", lay.gistWidth)
	}
	desc := sessionPickerDesc(gistRows()[0], lay)
	if strings.Contains(desc, "add retry logic") {
		t.Errorf("narrow row should be metadata only: %q", desc)
	}
	if !strings.Contains(desc, "2 msgs") {
		t.Errorf("narrow row should still carry metadata: %q", desc)
	}
}

// TestSessionsRowLayout_NoGistsMeansNoColumn: sessions with no derivable
// gist shouldn't reserve an empty column.
func TestSessionsRowLayout_NoGistsMeansNoColumn(t *testing.T) {
	rows := gistRows()
	for i := range rows {
		rows[i].Summary = ""
	}
	if lay := sessionsRowLayout(rows, 160); lay.gistWidth != 0 {
		t.Errorf("no summaries should mean no gist column, got %d", lay.gistWidth)
	}
}

// TestSessionPickerDesc_MetaAlignsAcrossRows: the metadata must start at the
// same column on every row, including rows whose own gist is empty — that
// alignment is the point of budgeting once per list.
func TestSessionPickerDesc_MetaAlignsAcrossRows(t *testing.T) {
	rows := gistRows()
	rows[1].Summary = "" // one row with no gist
	lay := sessionsRowLayout(rows, 160)
	col := -1
	for _, r := range rows {
		got := strings.Index(sessionPickerDesc(r, lay), summarySep)
		if col == -1 {
			col = got
			continue
		}
		if got != col {
			t.Errorf("metadata column misaligned: %d vs %d", got, col)
		}
	}
}

// TestSessionPickerDesc_ZeroWidthUsesDefault guards the pre-WindowSizeMsg
// case, where m.width is still 0.
func TestSessionPickerDesc_ZeroWidthUsesDefault(t *testing.T) {
	if lay := sessionsRowLayout(gistRows(), 0); lay.gistWidth <= 0 {
		t.Error("zero width should fall back to defaultPickerWidth, not drop the gist")
	}
}

func TestRenderSessionsMenuHeaderSpansOverlayWidth(t *testing.T) {
	p := &sessionsPickerState{menuItems: []sessionsMenuItem{
		{Label: "Load", Subtitle: "pick a session from the recent list", Action: sessionsLoadListMode},
		{Label: "Resume", Subtitle: "type a session id or name to resume directly", Action: sessionsResumeInputMode},
	}}
	out := stripANSI(renderSessionsPicker(p, 140))
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("sessions picker rendered no lines")
	}
	if got := runeLen(lines[0]); got != 140 {
		t.Fatalf("sessions top divider width = %d, want 140", got)
	}
}

// TestResumeSession_DoesNotPersistEmptyCurrent is the regression guard for
// the save site missed when HasExchange gating went in. "Launch yottacode,
// open /sessions, load a previous conversation" is the picker's most common
// flow, and the session being replaced is almost always a system-prompt-only
// shell — saving it here re-created exactly the ~48KB files the gating was
// added to stop, invisibly, because the read-side filters hide them.
func TestResumeSession_DoesNotPersistEmptyCurrent(t *testing.T) {
	m := newTestModel(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".yottacode", "sessions")

	target, _ := session.New("m", "/x")
	target.Messages = []adapter.Message{
		{Role: adapter.RoleUser, Content: "hi"},
		{Role: adapter.RoleAssistant, Content: "hello"},
	}
	if err := target.Save(); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	emptyID := m.sess.ID
	if m.sess.HasExchange() {
		t.Fatalf("precondition: fresh test session should have no exchange")
	}
	m, _ = m.resumeSession(target.ID, false)

	if _, err := os.Stat(filepath.Join(dir, emptyID+".json")); err == nil {
		t.Errorf("resuming persisted the empty current session %s as a shell", emptyID)
	}
	if m.sess.ID != target.ID {
		t.Errorf("resume landed on %s, want %s", m.sess.ID, target.ID)
	}
}

// TestResumeSession_PersistsCurrentWithExchange is the other half: a real
// conversation must still be flushed before switching away, or switching
// sessions would lose the turns since the last auto-save.
func TestResumeSession_PersistsCurrentWithExchange(t *testing.T) {
	m := newTestModel(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".yottacode", "sessions")

	target, _ := session.New("m", "/x")
	target.Messages = []adapter.Message{
		{Role: adapter.RoleUser, Content: "hi"},
		{Role: adapter.RoleAssistant, Content: "hello"},
	}
	if err := target.Save(); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "real work in progress"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "on it"},
	)
	currentID := m.sess.ID
	m, _ = m.resumeSession(target.ID, false)

	if _, err := os.Stat(filepath.Join(dir, currentID+".json")); err != nil {
		t.Errorf("a session with real turns must be saved before switching away: %v", err)
	}
	reloaded, err := session.Load(currentID)
	if err != nil {
		t.Fatalf("outgoing session should be loadable: %v", err)
	}
	if len(reloaded.Messages) != 2 {
		t.Errorf("outgoing session saved %d messages, want 2", len(reloaded.Messages))
	}
}

// TestSessionsRename_EmptySessionKeepsNameWithoutWritingShell exercises the
// rename guard directly rather than through the picker.
//
// Worth being precise about why: this path is NOT reachable from the UI. The
// rename list is sourced from session.List, which filters out sessions with
// no exchange, so an empty current session is never a rename target. The
// guard is defense-in-depth for the day that filter changes — and this test
// is what keeps it honest rather than dead, untested code.
//
// Behavior pinned: the name lands on the live session, but nothing is
// written, since a shell on disk would be filtered straight back out of the
// picker and read as "the rename did nothing".
func TestSessionsRename_EmptySessionKeepsNameWithoutWritingShell(t *testing.T) {
	m := newTestModel(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".yottacode", "sessions")

	if m.sess.HasExchange() {
		t.Fatal("precondition: fresh test session should have no exchange")
	}
	in := textinput.New()
	in.SetValue("empty-label")
	m.sessionsPickerOpen = true
	m.sessionsPicker = &sessionsPickerState{
		mode:   sessionsRenameInputMode,
		picked: &session.SessionInfo{ID: m.sess.ID},
		input:  in,
	}
	m, _ = m.commitSessionsRename()

	if m.sess.Name != "empty-label" {
		t.Errorf("rename should set the name on the live session, got %q", m.sess.Name)
	}
	if _, err := os.Stat(filepath.Join(dir, m.sess.ID+".json")); err == nil {
		t.Errorf("renaming an empty session wrote a shell at %s", m.sess.ID)
	}
}

// TestSessionsRename_SessionWithExchangeIsPersisted: the other half — a real
// conversation's new name must reach disk immediately, not wait for a turn.
func TestSessionsRename_SessionWithExchangeIsPersisted(t *testing.T) {
	m := newTestModel(t)
	m.sess.Messages = append(m.sess.Messages,
		adapter.Message{Role: adapter.RoleUser, Content: "hi"},
		adapter.Message{Role: adapter.RoleAssistant, Content: "hello"},
	)
	in := textinput.New()
	in.SetValue("real-label")
	m.sessionsPickerOpen = true
	m.sessionsPicker = &sessionsPickerState{
		mode:   sessionsRenameInputMode,
		picked: &session.SessionInfo{ID: m.sess.ID},
		input:  in,
	}
	m, _ = m.commitSessionsRename()

	loaded, err := session.Load("real-label")
	if err != nil {
		t.Fatalf("renamed session should be loadable by name: %v", err)
	}
	if loaded.ID != m.sess.ID {
		t.Errorf("loaded %q, want %q", loaded.ID, m.sess.ID)
	}
}

// TestLoadRecentSessions_ArchivesArePerMode: archives belong in the Load and
// Export lists (the archive is often the only copy of that history) but never
// in Rename — an archive has no persisted Name, so renaming one would write a
// duplicate session rather than label anything.
func TestLoadRecentSessions_ArchivesArePerMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	parent, _ := session.New("m", "/x")
	parent.Messages = []adapter.Message{
		{Role: adapter.RoleUser, Content: "hi"},
		{Role: adapter.RoleAssistant, Content: "hello"},
	}
	if err := parent.Save(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".yottacode", "sessions")
	body, _ := json.Marshal(map[string]any{
		"session_id": parent.ID,
		"captured":   time.Now().UTC(),
		"messages": []adapter.Message{
			{Role: adapter.RoleUser, Content: "archived detail"},
			{Role: adapter.RoleAssistant, Content: "archived reply"},
		},
	})
	snap := parent.ID + session.SnapshotMarker + "20260721-101010.000000000.json"
	if err := os.WriteFile(filepath.Join(dir, snap), body, 0o600); err != nil {
		t.Fatal(err)
	}

	countArchived := func(rows []session.SessionInfo) int {
		n := 0
		for _, r := range rows {
			if r.Archived {
				n++
			}
		}
		return n
	}
	if got := countArchived(loadRecentSessions(true)); got != 1 {
		t.Errorf("Load/Export list should offer the archive, got %d archived rows", got)
	}
	if got := countArchived(loadRecentSessions(false)); got != 0 {
		t.Errorf("Rename list must not offer archives, got %d", got)
	}
}

// TestDefaultExportPath_ArchiveUsesItsOwnID: exporting an archive resolves
// through Load, which mints a fresh session — naming the file after that
// would give the user a timestamp they have never seen.
func TestDefaultExportPath_ArchiveUsesItsOwnID(t *testing.T) {
	archive := session.SessionInfo{
		ID:         "20260721-024553.488321-pre-summary-20260721-024959.625656829",
		Archived:   true,
		ArchivedOf: "20260721-024553.488321",
	}
	got := defaultExportPath("/tmp", archive)
	want := filepath.Join("/tmp", archive.ID+".md")
	if got != want {
		t.Errorf("defaultExportPath(archive) = %q, want %q", got, want)
	}
}

// TestSessionPickerMeta_OmitsEmptyModel: a session restored from an ORPHANED
// archive has no model — loadSnapshot inherits it from the parent, and an
// orphan has none left. The row must not render a hollow separator pair.
func TestSessionPickerMeta_OmitsEmptyModel(t *testing.T) {
	got := sessionPickerMeta(session.SessionInfo{Messages: 227, Created: time.Now()}, false)
	if strings.Contains(got, " ·  · ") || strings.HasPrefix(got, " · ") {
		t.Errorf("empty model should drop its segment, got %q", got)
	}
	if !strings.Contains(got, "227 msgs") {
		t.Errorf("meta lost the message count: %q", got)
	}
	// A present model still leads.
	withModel := sessionPickerMeta(session.SessionInfo{Model: "gpt-5.5", Messages: 2, Created: time.Now()}, false)
	if !strings.HasPrefix(withModel, "gpt-5.5 · ") {
		t.Errorf("model should lead the metadata, got %q", withModel)
	}
	// Archived still announces itself.
	arch := sessionPickerMeta(session.SessionInfo{Archived: true, Messages: 5, Created: time.Now()}, false)
	if !strings.HasPrefix(arch, "archived · ") {
		t.Errorf("archived row should lead with 'archived', got %q", arch)
	}
}
