package adapter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestMessage_TimestampOmittedWhenNil locks the documented guarantee: a
// Message with no Timestamp (every message recorded before this field
// existed) round-trips without a "timestamp" key, so existing session
// files load byte-identical. Timestamp is a pointer specifically because
// encoding/json's omitempty has no effect on a zero-valued struct field
// like a bare time.Time — only the pointer form is actually omitted.
func TestMessage_TimestampOmittedWhenNil(t *testing.T) {
	b, err := json.Marshal(Message{Role: RoleUser, Content: "hi"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "timestamp") {
		t.Errorf("expected no timestamp key for a nil Timestamp, got %s", b)
	}
}

// TestMessage_TimestampRoundTrips confirms a set Timestamp survives a
// marshal/unmarshal cycle intact.
func TestMessage_TimestampRoundTrips(t *testing.T) {
	ts := time.Date(2026, 8, 27, 16, 52, 41, 0, time.UTC)
	b, err := json.Marshal(Message{Role: RoleAssistant, Content: "hi", Timestamp: &ts})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Message
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Timestamp == nil || !got.Timestamp.Equal(ts) {
		t.Errorf("Timestamp round-trip = %v, want %v", got.Timestamp, ts)
	}
}
