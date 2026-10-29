package daemon

import (
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := WriteState(path, "127.0.0.1:1234"); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.PID <= 0 || state.Address != "127.0.0.1:1234" {
		t.Fatalf("state=%+v", state)
	}
}
