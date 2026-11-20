package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestTranscriptSyncThrottledOnLargeTranscript(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	for i := 0; i < transcriptThrottleRows+10; i++ {
		m.queuePrintln("row")
	}
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.refreshTranscriptViewport()

	m.queuePrintln("late")
	m.transcriptSyncedAt = time.Now()
	m, cmd := applyMsg(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !m.transcriptDirty || cmd == nil || !m.transcriptFlushPending {
		t.Fatalf("expected deferred sync with a flush scheduled: dirty=%v pending=%v", m.transcriptDirty, m.transcriptFlushPending)
	}

	m.transcriptSyncedAt = time.Now().Add(-time.Second)
	m, _ = applyMsg(m, transcriptFlushMsg{})
	if m.transcriptDirty || m.transcriptFlushPending {
		t.Fatalf("flush should sync rows: dirty=%v pending=%v", m.transcriptDirty, m.transcriptFlushPending)
	}
}

func TestTranscriptSyncImmediateOnSmallTranscript(t *testing.T) {
	m := newTestModel(t)
	m.enteredConversation = true
	m.queuePrintln("a")
	m.transcriptSyncedAt = time.Now()
	m, _ = applyMsg(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.transcriptDirty {
		t.Fatal("small transcript must sync immediately")
	}
}
