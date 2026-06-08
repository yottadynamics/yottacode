package ychome

import (
	"path/filepath"
	"testing"
)

func TestDir_DefaultsToDotYottacodeUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")

	got, err := Dir("plans")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join(home, ".yottacode", "plans"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}

func TestDir_HonorsYottacodeHomeOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	override := t.TempDir()
	t.Setenv("YOTTACODE_HOME", "  "+override+"  ") // TrimSpace is part of the contract

	got, err := Dir("memory")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if want := filepath.Join(override, "memory"); got != want {
		t.Errorf("Dir = %q, want %q", got, want)
	}
}
