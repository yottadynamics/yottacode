package tui

import "testing"

func TestSysMsgSkipsEmptyDetails(t *testing.T) {
	got := SysMsg(SysState, "lsp", "document synced", "", "internal/tui/model.go")
	want := "○ lsp · document synced · internal/tui/model.go"
	if got != want {
		t.Fatalf("SysMsg() = %q, want %q", got, want)
	}
}

func TestSysMsgAlignedPadsSource(t *testing.T) {
	got := SysMsgAligned(SysThought, "thought", "7s", "42 tokens")
	want := "◦ thought  · 7s · 42 tokens"
	if got != want {
		t.Fatalf("SysMsgAligned() = %q, want %q", got, want)
	}
}

func TestSysMsgQueueExamples(t *testing.T) {
	cases := map[string]string{
		SysMsg(SysQueue, "queue", "next tool round", "fix docs"):     "→ queue · next tool round · fix docs",
		SysMsg(SysWarning, "queue", "already waiting", "↑ to edit"):  "⚠ queue · already waiting · ↑ to edit",
		SysMsg(SysReturn, "redo", "message loaded", "edit & submit"): "↩ redo · message loaded · edit & submit",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("SysMsg() = %q, want %q", got, want)
		}
	}
}
