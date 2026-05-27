package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageMediaType(t *testing.T) {
	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{"photo.png", "image/png", true},
		{"photo.PNG", "image/png", true},
		{"photo.jpg", "image/jpeg", true},
		{"photo.jpeg", "image/jpeg", true},
		{"photo.gif", "image/gif", true},
		{"photo.webp", "image/webp", true},
		{"file.txt", "", false},
		{"file.go", "", false},
		{"file.pdf", "", false},
		{"noext", "", false},
	}
	for _, tt := range tests {
		got, ok := imageMediaType(tt.path)
		if ok != tt.ok || got != tt.want {
			t.Errorf("imageMediaType(%q) = (%q, %v), want (%q, %v)", tt.path, got, ok, tt.want, tt.ok)
		}
	}
}

func TestReadFileTool_ImageSupported(t *testing.T) {
	tmp := t.TempDir()
	imgData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	path := filepath.Join(tmp, "test.png")
	if err := os.WriteFile(path, imgData, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tool := &ReadFileTool{Cwd: NewCwdRef(tmp), SupportsImages: true}
	res, err := tool.ExecuteMultimodal(context.Background(), `{"path":"test.png"}`)
	if err != nil {
		t.Fatalf("ExecuteMultimodal: %v", err)
	}
	if !strings.Contains(res.Content, "[image:") {
		t.Errorf("expected image label in content, got %q", res.Content)
	}
	if len(res.Images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(res.Images))
	}
	if res.Images[0].MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", res.Images[0].MediaType)
	}
	if len(res.Images[0].Data) != len(imgData) {
		t.Errorf("image data length = %d, want %d", len(res.Images[0].Data), len(imgData))
	}
}

func TestReadFileTool_ImageUnsupported(t *testing.T) {
	tmp := t.TempDir()
	imgData := []byte{0x89, 'P', 'N', 'G'}
	path := filepath.Join(tmp, "test.png")
	if err := os.WriteFile(path, imgData, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tool := &ReadFileTool{Cwd: NewCwdRef(tmp), SupportsImages: false}
	res, err := tool.ExecuteMultimodal(context.Background(), `{"path":"test.png"}`)
	if err != nil {
		t.Fatalf("ExecuteMultimodal: %v", err)
	}
	if !strings.Contains(res.Content, "does not support image input") {
		t.Errorf("expected unsupported message, got %q", res.Content)
	}
	if len(res.Images) != 0 {
		t.Errorf("expected 0 images, got %d", len(res.Images))
	}
}

func TestReadFileTool_TextFileFallsThrough(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "readme.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tool := &ReadFileTool{Cwd: NewCwdRef(tmp), SupportsImages: true}
	res, err := tool.ExecuteMultimodal(context.Background(), `{"path":"readme.txt"}`)
	if err != nil {
		t.Fatalf("ExecuteMultimodal: %v", err)
	}
	if !strings.Contains(res.Content, "hello world") {
		t.Errorf("expected text content, got %q", res.Content)
	}
	if len(res.Images) != 0 {
		t.Errorf("expected 0 images for text file, got %d", len(res.Images))
	}
}

func TestReadFileTool_ExecuteFallsBackForImages(t *testing.T) {
	tmp := t.TempDir()
	imgData := []byte{0x89, 'P', 'N', 'G'}
	path := filepath.Join(tmp, "test.jpg")
	if err := os.WriteFile(path, imgData, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tool := &ReadFileTool{Cwd: NewCwdRef(tmp), SupportsImages: true}
	out, err := tool.Execute(context.Background(), `{"path":"test.jpg"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "[image:") {
		t.Errorf("expected image label, got %q", out)
	}
}

func TestReadFileTool_ImageDenyPath(t *testing.T) {
	tmp := t.TempDir()
	secret := filepath.Join(tmp, ".secret.png")
	if err := os.WriteFile(secret, []byte{0x89}, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tool := &ReadFileTool{
		Cwd:            NewCwdRef(tmp),
		DenyReadPaths:  []string{secret},
		SupportsImages: true,
	}
	_, err := tool.ExecuteMultimodal(context.Background(), `{"path":".secret.png"}`)
	if err == nil {
		t.Errorf("expected deny error for blocked path")
	}
}
