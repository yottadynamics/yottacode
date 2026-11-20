package filerefs

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePNG(t *testing.T, path string, img image.Image) int {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return buf.Len()
}

func noiseImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(1))
	for i := range img.Pix {
		img.Pix[i] = byte(rng.Intn(256))
	}
	return img
}

// An image over the send limit is resized to fit instead of being refused
// (a provider-rejected image would fail the whole request).
func TestLoad_OversizedImageIsShrunkNotRefused(t *testing.T) {
	cwd := t.TempDir()
	size := writePNG(t, filepath.Join(cwd, "big.png"), noiseImage(1800, 1800))
	if size <= MaxImageSize {
		t.Skipf("noise PNG only %d bytes; cannot exercise the shrink path", size)
	}

	r := Load(Parse("@big.png"), cwd)[0]
	if !r.Loaded || !r.IsImage {
		t.Fatalf("ref = %+v", r)
	}
	if r.MediaType != "image/jpeg" || len(r.Data) > MaxImageSize {
		t.Fatalf("media=%s bytes=%d (limit %d)", r.MediaType, len(r.Data), MaxImageSize)
	}
	if !strings.Contains(r.Note, "resized") {
		t.Fatalf("note = %q", r.Note)
	}
	if _, err := jpeg.Decode(bytes.NewReader(r.Data)); err != nil {
		t.Fatalf("shrunk bytes are not a valid JPEG: %v", err)
	}
	if text, _ := Describe(r); !strings.Contains(text, "resized") {
		t.Fatalf("Describe hides the resize: %q", text)
	}
}

// A small file with a side past the provider maximum (8000 px) is resized
// even though it is tiny on disk.
func TestLoad_OverDimensionImageIsResized(t *testing.T) {
	cwd := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 8100, 12))
	for x := 0; x < 8100; x++ {
		for y := 0; y < 12; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: 40, B: 200, A: 255})
		}
	}
	if size := writePNG(t, filepath.Join(cwd, "wide.png"), img); size > MaxImageSize {
		t.Fatalf("setup: %d bytes", size)
	}
	r := Load(Parse("@wide.png"), cwd)[0]
	if !r.Loaded || r.MediaType != "image/jpeg" {
		t.Fatalf("ref = %+v", r)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(r.Data))
	if err != nil || cfg.Width > shrinkStartDim {
		t.Fatalf("decoded cfg=%+v err=%v", cfg, err)
	}
}

// A small, valid image within limits is sent untouched.
func TestLoad_NormalImageIsNotReencoded(t *testing.T) {
	cwd := t.TempDir()
	writePNG(t, filepath.Join(cwd, "ok.png"), image.NewRGBA(image.Rect(0, 0, 40, 40)))
	r := Load(Parse("@ok.png"), cwd)[0]
	if !r.Loaded || r.MediaType != "image/png" || r.Note != "" {
		t.Fatalf("ref = %+v", r)
	}
}

// A decompression bomb (tiny file, gigapixel canvas) is refused without
// allocating the canvas.
func TestLoad_PixelBombRefused(t *testing.T) {
	cwd := t.TempDir()
	writePNG(t, filepath.Join(cwd, "bomb.png"), image.NewGray(image.Rect(0, 0, 9000, 9000)))
	r := Load(Parse("@bomb.png"), cwd)[0]
	if r.Loaded || !strings.Contains(r.Error, "too large to resize") {
		t.Fatalf("ref = %+v", r)
	}
}

func TestLoad_CorruptOversizedImageGivesClearError(t *testing.T) {
	cwd := t.TempDir()
	f, err := os.Create(filepath.Join(cwd, "bad.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(bytes.Repeat([]byte("not-an-image"), (MaxImageSize+1024)/len("not-an-image")+1)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	r := Load(Parse("@bad.png"), cwd)[0]
	if r.Loaded || !strings.Contains(r.Error, "could not decode image") {
		t.Fatalf("ref = %+v", r)
	}
}

func TestShrinkImage_FlattensAlphaOverWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64)) // fully transparent
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	out, mt, _, err := shrinkImage(buf.Bytes())
	if err != nil || mt != "image/jpeg" {
		t.Fatalf("err=%v mt=%s", err, mt)
	}
	dec, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := dec.At(10, 10).RGBA()
	if r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Fatalf("transparent pixel not flattened to white: %d %d %d", r>>8, g>>8, b>>8)
	}
}
