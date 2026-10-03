package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yottadynamics/yottacode/internal/edit/hashline"
)

// mustOversizedFile creates a sparse file one byte over hashline's edit size
// cap, so the test costs no real disk I/O or memory despite the file
// reporting a 64 MiB+ size.
func mustOversizedFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(hashline.MaxEditFileBytes + 1); err != nil {
		t.Skipf("sparse file not supported on this filesystem: %v", err)
	}
	return path
}

func TestApplyHashlineTool_RejectsOversizedFileWithoutReadingIt(t *testing.T) {
	tmp := t.TempDir()
	mustOversizedFile(t, tmp, "big.bin")

	args := `{"path":"big.bin","offset":0,"length":1,"hash":"e3b0c44298fc1c14","old":"x","new":"y"}`
	_, err := newApplyHashline(tmp).Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "file_too_large") {
		t.Fatalf("err = %v, want a file_too_large error", err)
	}
	if !strings.Contains(err.Error(), "run_bash") {
		t.Errorf("err should suggest an alternative for large files: %v", err)
	}
}

func TestEditAnchoredTool_RejectsOversizedFileWithoutReadingIt(t *testing.T) {
	tmp := t.TempDir()
	mustOversizedFile(t, tmp, "big.bin")

	tool := &EditAnchoredTool{Cwd: NewCwdRef(tmp), WriteOpts: WritePathOptions{Cwd: NewCwdRef(tmp)}}
	args := `{"path":"big.bin","operations":[{"anchor":"1#00000000","new_text":"x"}]}`
	_, err := tool.Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "file_too_large") {
		t.Fatalf("err = %v, want a file_too_large error", err)
	}
}
