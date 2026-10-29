package journal

import (
	"path/filepath"
	"testing"

	coderacp "github.com/coder/acp-go-sdk"
)

func TestStorePersistsAndDetectsGaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxEvents+2; i++ {
		if _, err := s.Append(coderacp.UpdateAgentMessageText("event")); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	events, latest, gap := reopened.Since(0)
	if !gap || latest != MaxEvents+2 || events != nil {
		t.Fatalf("gap=%v latest=%d events=%d", gap, latest, len(events))
	}
	events, latest, gap = reopened.Since(2)
	if gap || latest != MaxEvents+2 || len(events) != MaxEvents {
		t.Fatalf("gap=%v latest=%d events=%d", gap, latest, len(events))
	}
}
