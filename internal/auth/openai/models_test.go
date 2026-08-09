package openai

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDefaultModelsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	got, err := DefaultModelsPath()
	if err != nil {
		t.Fatalf("DefaultModelsPath: %v", err)
	}
	want := filepath.Join(home, ".yottacode", "auth", "openai-auth-models.json")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestSaveLoadModelsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")

	in := ModelsFile{
		ScannedAt:    time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
		Models:       []string{"gpt-5.5"},
		DisplayNames: map[string]string{"gpt-5.5": "GPT-5.5"},
	}
	if err := SaveModels(path, in); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}
	out, err := LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if !out.ScannedAt.Equal(in.ScannedAt) {
		t.Errorf("ScannedAt = %v, want %v", out.ScannedAt, in.ScannedAt)
	}
	if len(out.Models) != 1 || out.Models[0] != "gpt-5.5" {
		t.Errorf("Models = %v, want [gpt-5.5]", out.Models)
	}
	if out.DisplayNames["gpt-5.5"] != "GPT-5.5" {
		t.Errorf("DisplayNames[gpt-5.5] = %q, want GPT-5.5", out.DisplayNames["gpt-5.5"])
	}
}

func TestSaveModelsMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits don't match on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := SaveModels(path, ModelsFile{Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 0600", perm)
	}
}

func TestSaveModelsCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "models.json")
	if err := SaveModels(path, ModelsFile{Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatalf("SaveModels: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file to exist: %v", err)
	}
}

func TestLoadModelsNotFound(t *testing.T) {
	_, err := LoadModels(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, ErrModelsNotFound) {
		t.Errorf("got %v, want ErrModelsNotFound", err)
	}
}

func TestLoadModelsCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := LoadModels(path)
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if errors.Is(err, ErrModelsNotFound) {
		t.Errorf("got ErrModelsNotFound, want decode error")
	}
}

func TestSaveModelsAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")

	first := ModelsFile{Models: []string{"gpt-5.5"}}
	if err := SaveModels(path, first); err != nil {
		t.Fatalf("first save: %v", err)
	}
	second := ModelsFile{Models: []string{"gpt-5.5", "gpt-5.4"}}
	if err := SaveModels(path, second); err != nil {
		t.Fatalf("second save: %v", err)
	}
	out, err := LoadModels(path)
	if err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
	if len(out.Models) != 2 {
		t.Errorf("Models = %v, want 2 entries", out.Models)
	}
}
