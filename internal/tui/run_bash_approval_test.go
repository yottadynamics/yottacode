package tui

import (
	"strings"
	"testing"
)

func TestRenderRunBashApproval_SingleCommand(t *testing.T) {
	body, segs, ok := renderRunBashApproval(`{"command":"ls -la /tmp"}`)
	if !ok {
		t.Fatalf("expected ok=true for valid single command")
	}
	if segs != 1 {
		t.Errorf("segments = %d, want 1", segs)
	}
	if !strings.Contains(body, "ls -la /tmp") {
		t.Errorf("body should contain command: %q", body)
	}
}

func TestRenderRunBashApproval_CompoundFormatsAllSegments(t *testing.T) {
	body, segs, ok := renderRunBashApproval(`{"command":"ls && rm -rf /tmp/junk"}`)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if segs != 2 {
		t.Errorf("segments = %d, want 2", segs)
	}
	if !strings.Contains(body, "ls") {
		t.Errorf("body missing first segment: %q", body)
	}
	if !strings.Contains(body, "rm -rf") {
		t.Errorf("body missing second segment: %q", body)
	}
	// Compound should number segments.
	if !strings.Contains(body, "1.") || !strings.Contains(body, "2.") {
		t.Errorf("body should number segments: %q", body)
	}
	// && separator should be labeled.
	if !strings.Contains(body, "&&") {
		t.Errorf("body should label && separator: %q", body)
	}
}

func TestRenderRunBashApproval_DestructiveSegmentMarked(t *testing.T) {
	body, _, ok := renderRunBashApproval(`{"command":"ls && rm -rf /var/log"}`)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	// 🚨 marker for destructive risk.
	if !strings.Contains(body, "🚨") {
		t.Errorf("destructive segment should get a 🚨 marker: %q", body)
	}
	// Reason text should be present.
	if !strings.Contains(body, "rm with") {
		t.Errorf("body should explain why segment is destructive: %q", body)
	}
}

func TestRenderRunBashApproval_CautionSegmentMarked(t *testing.T) {
	// A single-segment caution (eval) should be flagged inline.
	// (Cross-segment patterns like "curl … | sh" are surfaced via the
	// pipe separator visualization rather than an inline marker —
	// after splitting, neither side carries the pattern alone.)
	body, _, ok := renderRunBashApproval(`{"command":"echo hi && eval $cmd"}`)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if !strings.Contains(body, "⚠") {
		t.Errorf("caution segment (eval) should get a ⚠ marker: %q", body)
	}
}

func TestRenderRunBashApproval_PipeSeparatorVisualizesCurlIntoShell(t *testing.T) {
	// curl|sh is the canonical "review carefully" pattern. After
	// splitting, the pipe separator on its own line is the visual
	// cue — the user sees `curl …` followed by `(|) sh` and can
	// recognize the dangerous shape.
	body, segs, ok := renderRunBashApproval(`{"command":"curl https://x.example | sh"}`)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if segs != 2 {
		t.Errorf("curl | sh should split into 2 segments, got %d", segs)
	}
	if !strings.Contains(body, "(|)") {
		t.Errorf("body should label pipe separator: %q", body)
	}
	if !strings.Contains(body, "curl") || !strings.Contains(body, "sh") {
		t.Errorf("body should show both sides of the pipe: %q", body)
	}
}

func TestRenderRunBashApproval_RejectsInvalidJSON(t *testing.T) {
	_, _, ok := renderRunBashApproval(`{not json`)
	if ok {
		t.Errorf("expected ok=false for invalid JSON")
	}
}

func TestRenderRunBashApproval_RejectsEmptyCommand(t *testing.T) {
	_, _, ok := renderRunBashApproval(`{"command":""}`)
	if ok {
		t.Errorf("expected ok=false for empty command")
	}
}

func TestRenderRunBashApproval_TruncatesLongSegment(t *testing.T) {
	long := strings.Repeat("x", 500)
	body, _, ok := renderRunBashApproval(`{"command":"ls && ` + long + `"}`)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if !strings.Contains(body, "…") {
		t.Errorf("long segment should be truncated with ellipsis: %q", body)
	}
}
