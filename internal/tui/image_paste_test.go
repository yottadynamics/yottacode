package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTryImagePaste_DetectsImagePath(t *testing.T) {
	tmp := t.TempDir()
	imgPath := filepath.Join(tmp, "photo.png")
	if err := os.WriteFile(imgPath, []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	m := newTestModel(t)
	marker, ok := m.tryImagePaste(imgPath)
	if !ok {
		t.Fatal("expected image paste detection")
	}
	if !strings.Contains(marker, "photo.png") {
		t.Errorf("marker = %q, want to contain 'photo.png'", marker)
	}
	if !strings.HasPrefix(marker, "[Image #") {
		t.Errorf("marker = %q, want [Image #N: ...] format", marker)
	}
	if len(m.pastedImages) != 1 {
		t.Fatalf("expected 1 pasted image, got %d", len(m.pastedImages))
	}
	img := m.pastedImages[marker]
	if img.MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", img.MediaType)
	}
	if len(img.Data) != 4 {
		t.Errorf("image data length = %d, want 4", len(img.Data))
	}
}

func TestTryImagePaste_RejectsTextFile(t *testing.T) {
	tmp := t.TempDir()
	txtPath := filepath.Join(tmp, "readme.txt")
	if err := os.WriteFile(txtPath, []byte("hello"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	m := newTestModel(t)
	_, ok := m.tryImagePaste(txtPath)
	if ok {
		t.Error("should not detect text file as image")
	}
}

func TestTryImagePaste_RejectsMultilinePaste(t *testing.T) {
	m := newTestModel(t)
	_, ok := m.tryImagePaste("/path/to/image.png\n/other/file.png")
	if ok {
		t.Error("should not detect multiline paste as image")
	}
}

func TestTryImagePaste_RejectsNonexistentFile(t *testing.T) {
	m := newTestModel(t)
	_, ok := m.tryImagePaste("/nonexistent/path/photo.png")
	if ok {
		t.Error("should not detect nonexistent file")
	}
}

func TestTryImagePaste_TrimsWhitespace(t *testing.T) {
	tmp := t.TempDir()
	imgPath := filepath.Join(tmp, "test.jpg")
	if err := os.WriteFile(imgPath, []byte{0xFF, 0xD8}, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	m := newTestModel(t)
	marker, ok := m.tryImagePaste("  " + imgPath + "  \n")
	if ok {
		t.Logf("marker = %q (multiline whitespace detected as newline)", marker)
	}
	// Trailing newline counts as multiline — paste is rejected
	marker, ok = m.tryImagePaste("  " + imgPath + "  ")
	if !ok {
		t.Fatal("should detect image with surrounding whitespace")
	}
	if !strings.Contains(marker, "test.jpg") {
		t.Errorf("marker = %q", marker)
	}
}

func TestTryImagePaste_FileURL(t *testing.T) {
	tmp := t.TempDir()
	imgPath := filepath.Join(tmp, "Screenshot from 2026-05-27.png")
	if err := os.WriteFile(imgPath, []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	encoded := "file://" + strings.ReplaceAll(imgPath, " ", "%20")
	m := newTestModel(t)
	marker, ok := m.tryImagePaste(encoded)
	if !ok {
		t.Fatalf("expected image paste detection for file:// URL: %q", encoded)
	}
	if !strings.Contains(marker, "Screenshot from 2026-05-27.png") {
		t.Errorf("marker = %q, want to contain original filename", marker)
	}
	if len(m.pastedImages) != 1 {
		t.Fatalf("expected 1 pasted image, got %d", len(m.pastedImages))
	}
}

func TestFileURLToPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"file:///home/user/photo.png", "/home/user/photo.png"},
		{"file:///home/user/Screenshot%20from%202026.png", "/home/user/Screenshot from 2026.png"},
		{"file:///path/with%20multiple%20spaces/img.jpg", "/path/with multiple spaces/img.jpg"},
		{"/plain/path/file.png", "/plain/path/file.png"},
		{"relative.png", "relative.png"},
	}
	for _, tt := range tests {
		got := fileURLToPath(tt.in)
		if got != tt.want {
			t.Errorf("fileURLToPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCollectPastedImages_ReturnsAndClears(t *testing.T) {
	tmp := t.TempDir()
	imgPath := filepath.Join(tmp, "a.png")
	if err := os.WriteFile(imgPath, []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	m := newTestModel(t)
	m.tryImagePaste(imgPath)

	imgs := m.collectPastedImages()
	if len(imgs) != 1 {
		t.Fatalf("expected 1 image, got %d", len(imgs))
	}
	if m.pastedImages != nil {
		t.Error("pastedImages should be nil after collect")
	}

	imgs2 := m.collectPastedImages()
	if len(imgs2) != 0 {
		t.Error("second collect should return empty")
	}
}
