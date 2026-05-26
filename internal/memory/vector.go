package memory

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"
)

// VecPath returns the sidecar vector file path for a memory file.
// Replaces the .md extension with .vec.
func VecPath(mdPath string) string {
	return strings.TrimSuffix(mdPath, ".md") + ".vec"
}

// WriteVec writes a float32 vector to disk as raw little-endian binary.
func WriteVec(path string, vec []float32) error {
	f, err := os.Create(path + ".tmp")
	if err != nil {
		return fmt.Errorf("vec: create %s: %w", path, err)
	}
	if err := binary.Write(f, binary.LittleEndian, vec); err != nil {
		f.Close()
		os.Remove(f.Name())
		return fmt.Errorf("vec: write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("vec: close %s: %w", path, err)
	}
	return os.Rename(path+".tmp", path)
}

// ReadVec reads a float32 vector from a raw little-endian binary file.
// Returns nil, nil if the file does not exist.
func ReadVec(path string) ([]float32, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("vec: read %s: %w", path, err)
	}
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("vec: %s has invalid size %d (not multiple of 4)", path, len(data))
	}
	n := len(data) / 4
	vec := make([]float32, n)
	for i := range vec {
		bits := binary.LittleEndian.Uint32(data[i*4 : (i+1)*4])
		vec[i] = math.Float32frombits(bits)
	}
	return vec, nil
}

// DeleteVec removes a sidecar vector file if it exists.
func DeleteVec(mdPath string) {
	os.Remove(VecPath(mdPath))
}

// CosineSimilarity returns the cosine similarity between two vectors.
// Returns 0 if either vector is zero-length or the vectors have
// different dimensions.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
