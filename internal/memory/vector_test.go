package memory

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestVecPath(t *testing.T) {
	got := VecPath("/home/user/.yottacode/memory/user/feedback_testing.md")
	want := "/home/user/.yottacode/memory/user/feedback_testing.vec"
	if got != want {
		t.Errorf("VecPath = %q, want %q", got, want)
	}
}

func TestWriteReadVec_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.vec")
	want := []float32{0.1, -0.5, 3.14, 0, -1e-7}

	if err := WriteVecWithModel(path, want, "nomic-embed-text"); err != nil {
		t.Fatalf("WriteVecWithModel: %v", err)
	}

	got, err := ReadVec(path)
	if err != nil {
		t.Fatalf("ReadVec: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %f, want %f", i, got[i], want[i])
		}
	}
}

func TestWriteReadVecMeta_ModelPreserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.vec")
	vec := []float32{1.0, 2.0, 3.0}

	if err := WriteVecWithModel(path, vec, "nomic-embed-text"); err != nil {
		t.Fatalf("WriteVecWithModel: %v", err)
	}

	_, meta, err := ReadVecMeta(path)
	if err != nil {
		t.Fatalf("ReadVecMeta: %v", err)
	}
	if meta.Model != "nomic-embed-text" {
		t.Errorf("Model = %q, want %q", meta.Model, "nomic-embed-text")
	}
	if meta.Dims != 3 {
		t.Errorf("Dims = %d, want 3", meta.Dims)
	}
}

func TestReadVec_LegacyFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.vec")

	// Write raw float32 bytes without header (legacy format)
	vec := []float32{1.0, 2.0, 3.0}
	f, _ := os.Create(path)
	for _, v := range vec {
		b := [4]byte{}
		bits := math.Float32bits(v)
		b[0] = byte(bits)
		b[1] = byte(bits >> 8)
		b[2] = byte(bits >> 16)
		b[3] = byte(bits >> 24)
		f.Write(b[:])
	}
	f.Close()

	got, meta, err := ReadVecMeta(path)
	if err != nil {
		t.Fatalf("ReadVecMeta (legacy): %v", err)
	}
	if meta.Model != "" {
		t.Errorf("legacy file should have empty model; got %q", meta.Model)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 floats; got %d", len(got))
	}
	for i, w := range vec {
		if got[i] != w {
			t.Errorf("[%d] = %f, want %f", i, got[i], w)
		}
	}
}

func TestNeedsReembed_NoFile(t *testing.T) {
	if !NeedsReembed("/nonexistent.vec", "nomic-embed-text") {
		t.Error("missing file should need re-embed")
	}
}

func TestNeedsReembed_LegacyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.vec")
	os.WriteFile(path, []byte{0, 0, 128, 63, 0, 0, 0, 64}, 0o600) // raw float32

	if !NeedsReembed(path, "nomic-embed-text") {
		t.Error("legacy file (no model) should need re-embed")
	}
}

func TestNeedsReembed_SameModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.vec")
	WriteVecWithModel(path, []float32{1, 2, 3}, "nomic-embed-text")

	if NeedsReembed(path, "nomic-embed-text") {
		t.Error("same model should not need re-embed")
	}
}

func TestNeedsReembed_DifferentModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.vec")
	WriteVecWithModel(path, []float32{1, 2, 3}, "nomic-embed-text")

	if !NeedsReembed(path, "all-minilm") {
		t.Error("different model should need re-embed")
	}
}

func TestReadVec_NotExist(t *testing.T) {
	got, err := ReadVec("/nonexistent/path.vec")
	if err != nil {
		t.Fatalf("expected nil error for missing file; got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil vec for missing file; got %v", got)
	}
}

func TestReadVec_InvalidSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.vec")
	os.WriteFile(path, []byte{1, 2, 3}, 0o600)

	_, err := ReadVec(path)
	if err == nil {
		t.Fatal("expected error for invalid vec size")
	}
}

func TestDeleteVec(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "test.md")
	vecPath := VecPath(mdPath)

	WriteVecWithModel(vecPath, []float32{1}, "test")
	DeleteVec(mdPath)

	if _, err := os.Stat(vecPath); !os.IsNotExist(err) {
		t.Error("DeleteVec should remove the .vec file")
	}
}

func TestCosineSimilarity_Identical(t *testing.T) {
	v := []float32{1, 2, 3}
	got := CosineSimilarity(v, v)
	if math.Abs(got-1.0) > 1e-6 {
		t.Errorf("identical vectors should have cosine 1.0; got %f", got)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	got := CosineSimilarity(a, b)
	if math.Abs(got) > 1e-6 {
		t.Errorf("orthogonal vectors should have cosine 0; got %f", got)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{-1, -2, -3}
	got := CosineSimilarity(a, b)
	if math.Abs(got+1.0) > 1e-6 {
		t.Errorf("opposite vectors should have cosine -1.0; got %f", got)
	}
}

func TestCosineSimilarity_DifferentLength(t *testing.T) {
	a := []float32{1, 2}
	b := []float32{1, 2, 3}
	got := CosineSimilarity(a, b)
	if got != 0 {
		t.Errorf("different-length vectors should return 0; got %f", got)
	}
}

func TestCosineSimilarity_ZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	got := CosineSimilarity(a, b)
	if got != 0 {
		t.Errorf("zero vector should return 0; got %f", got)
	}
}

func TestCosineSimilarity_Empty(t *testing.T) {
	got := CosineSimilarity(nil, nil)
	if got != 0 {
		t.Errorf("empty vectors should return 0; got %f", got)
	}
}
