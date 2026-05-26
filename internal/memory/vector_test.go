package memory

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestVecPath(t *testing.T) {
	got := VecPath("/home/user/.yottacode/memory/feedback_testing.md")
	want := "/home/user/.yottacode/memory/feedback_testing.vec"
	if got != want {
		t.Errorf("VecPath = %q, want %q", got, want)
	}
}

func TestWriteReadVec_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.vec")
	want := []float32{0.1, -0.5, 3.14, 0, -1e-7}

	if err := WriteVec(path, want); err != nil {
		t.Fatalf("WriteVec: %v", err)
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
	os.WriteFile(path, []byte{1, 2, 3}, 0o600) // 3 bytes, not multiple of 4

	_, err := ReadVec(path)
	if err == nil {
		t.Fatal("expected error for invalid vec size")
	}
}

func TestDeleteVec(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "test.md")
	vecPath := VecPath(mdPath)

	os.WriteFile(vecPath, []byte{0, 0, 0, 0}, 0o600)
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
