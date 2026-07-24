package recall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
	"github.com/yottadynamics/yottacode/internal/session"
)

// TestBackfill_IgnoresArchivedSnapshots guards the interaction that broke
// when session.List started returning archives unconditionally.
//
// Backfill keys both its index and its prune on session.List ids. An
// archive's id resolves through loadSnapshot, which mints a session with a
// FRESH id — so IndexSession stored it under an id that was never in `live`,
// and pruneMissingSessions deleted it again in the same run. Net effect:
// every archive was parsed, indexed and dropped on every launch, and its
// content was never actually searchable. Backfill must see live sessions only.
func TestBackfill_IgnoresArchivedSnapshots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	parent, _ := session.New("m", "/proj")
	parent.Messages = []adapter.Message{
		{Role: adapter.RoleUser, Content: "live conversation"},
		{Role: adapter.RoleAssistant, Content: "ok"},
	}
	if err := parent.Save(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".yottacode", "sessions")
	body, _ := json.Marshal(map[string]any{
		"session_id": parent.ID,
		"captured":   time.Now().UTC(),
		"messages": []adapter.Message{
			{Role: adapter.RoleUser, Content: "archived pre-compaction detail"},
			{Role: adapter.RoleAssistant, Content: "archived reply"},
		},
	})
	snap := parent.ID + session.SnapshotMarker + "20260721-101010.000000000.json"
	if err := os.WriteFile(filepath.Join(dir, snap), body, 0o600); err != nil {
		t.Fatal(err)
	}

	idx, err := Open()
	if err != nil {
		t.Skip(err)
	}
	defer idx.Close()

	// Twice: a stable index across launches is the property that broke.
	for range 2 {
		if err := Backfill(idx); err != nil {
			t.Fatalf("Backfill: %v", err)
		}
	}

	rows, err := idx.db.Query(`SELECT id FROM sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if len(got) != 1 || got[0] != parent.ID {
		t.Errorf("indexed ids = %v, want exactly [%s] — no archive-derived rows", got, parent.ID)
	}
}
