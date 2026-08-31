package agent

import (
	"testing"
	"time"

	"github.com/yottadynamics/yottacode/internal/adapter"
)

// TestStampNow_SetsOnlyWhenUnset confirms the single-source-of-truth rule:
// a message with no Timestamp gets one, and a message that already carries
// one (e.g. a partial reply preserved from a cancelled stream, built ahead
// of the append) keeps it untouched.
func TestStampNow_SetsOnlyWhenUnset(t *testing.T) {
	before := time.Now()
	got := stampNow(adapter.Message{Role: adapter.RoleAssistant, Content: "hi"})
	after := time.Now()
	if got.Timestamp == nil {
		t.Fatal("stampNow should set Timestamp on an unstamped message")
	}
	if got.Timestamp.Before(before) || got.Timestamp.After(after) {
		t.Errorf("Timestamp %v not within [%v, %v]", got.Timestamp, before, after)
	}

	fixed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	got = stampNow(adapter.Message{Role: adapter.RoleAssistant, Content: "hi", Timestamp: &fixed})
	if got.Timestamp == nil || !got.Timestamp.Equal(fixed) {
		t.Errorf("stampNow should not overwrite an existing Timestamp, got %v, want %v", got.Timestamp, fixed)
	}
}

// TestAppendHistory_StampsMessages is the wiring regression: every message
// appended through appendHistory — the choke point every RoleAssistant/
// RoleTool append in the loop passes through — comes out with a Timestamp,
// without callers having to remember to set one.
func TestAppendHistory_StampsMessages(t *testing.T) {
	cfg := LoopConfig{}
	var hist []adapter.Message
	appendHistory(cfg, &hist, adapter.Message{Role: adapter.RoleAssistant, Content: "hi"})
	if len(hist) != 1 || hist[0].Timestamp == nil {
		t.Fatalf("appendHistory should stamp the appended message, got %+v", hist)
	}
}
