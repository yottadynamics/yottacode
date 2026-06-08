package usercmd

import (
	"path/filepath"
	"testing"
)

// UserCommandsDir resolves the global custom-command dir through
// ychome.Dir, so it must honor $YOTTACODE_HOME like skills, plans,
// agents, and the memory tree. These pin both branches.
func TestUserCommandsDir_DefaultsUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("YOTTACODE_HOME", "")
	got, err := UserCommandsDir()
	if err != nil {
		t.Fatalf("UserCommandsDir: %v", err)
	}
	if want := filepath.Join(home, ".yottacode", "commands"); got != want {
		t.Errorf("UserCommandsDir = %q, want %q", got, want)
	}
}

func TestUserCommandsDir_HonorsYottacodeHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	override := t.TempDir()
	t.Setenv("YOTTACODE_HOME", override)
	got, err := UserCommandsDir()
	if err != nil {
		t.Fatalf("UserCommandsDir: %v", err)
	}
	if want := filepath.Join(override, "commands"); got != want {
		t.Errorf("UserCommandsDir = %q, want %q", got, want)
	}
}
