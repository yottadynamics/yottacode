package skills

import (
	"testing"

	"github.com/yottadynamics/yottacode/internal/config"
)

// TestEnableDefaultOn verifies the CLI-install-enables-for-next-session
// path: adds a name, is idempotent on repeat, and merges (sorted) rather
// than clobbering an existing list. Uses a sandboxed HOME so it can never
// touch the real ~/.yottacode/config.toml.
func TestEnableDefaultOn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := EnableDefaultOn("brainstorming"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	cfg, err := config.LoadDefault()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Skills.DefaultOn; len(got) != 1 || got[0] != "brainstorming" {
		t.Fatalf("default_on = %v, want [brainstorming]", got)
	}

	// Repeat call is a no-op, not a duplicate entry.
	if err := EnableDefaultOn("brainstorming"); err != nil {
		t.Fatalf("enable (repeat): %v", err)
	}
	// A second name merges alongside the first instead of replacing it.
	if err := EnableDefaultOn("ansible-automation"); err != nil {
		t.Fatalf("enable (second name): %v", err)
	}
	cfg, err = config.LoadDefault()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"ansible-automation", "brainstorming"}
	got := cfg.Skills.DefaultOn
	if len(got) != len(want) {
		t.Fatalf("default_on = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("default_on = %v, want %v", got, want)
		}
	}
}
