package memory

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// vecMagic identifies the header format. Legacy files (raw float32)
// won't start with these bytes, so ReadVec can auto-detect.
var vecMagic = [4]byte{'Y', 'V', 'E', 'C'}

// VecMeta holds the metadata stored in a .vec file header.
type VecMeta struct {
	Model string
	Dims  int
}

// VecPath returns the sidecar vector file path for a memory file.
func VecPath(mdPath string) string {
	return strings.TrimSuffix(mdPath, ".md") + ".vec"
}

// WriteVec writes a float32 vector to disk with a header that records
// the embedding model name and dimension count.
func WriteVec(path string, vec []float32) error {
	return WriteVecWithModel(path, vec, "")
}

// WriteVecWithModel writes a vector with an explicit model name header.
//
// Staging uses a UNIQUE temp file (os.CreateTemp) and fsyncs before
// rename, for the same reasons as memory.AtomicWrite: a fixed
// "<path>.tmp" name let a concurrent writer's deferred cleanup delete
// this writer's in-flight temp (silently dropping the .vec), and the
// missing fsync left a crash window. The streaming header+binary layout
// can't go through AtomicWrite's single-buffer API, so the same
// guarantees are applied inline here.
func WriteVecWithModel(path string, vec []float32, model string) error {
	if len(model) > 1024 {
		return fmt.Errorf("vec: model name too long (%d bytes, max 1024)", len(model))
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("vec: create temp for %s: %w", path, err)
	}
	tmp := f.Name()
	committed := false
	defer func() {
		if !committed {
			f.Close()
			os.Remove(tmp)
		}
	}()

	if _, err := f.Write(vecMagic[:]); err != nil {
		return fmt.Errorf("vec: write header %s: %w", path, err)
	}
	modelBytes := []byte(model)
	if err := binary.Write(f, binary.LittleEndian, uint16(len(modelBytes))); err != nil {
		return fmt.Errorf("vec: write model len %s: %w", path, err)
	}
	if _, err := f.Write(modelBytes); err != nil {
		return fmt.Errorf("vec: write model %s: %w", path, err)
	}
	if err := binary.Write(f, binary.LittleEndian, uint32(len(vec))); err != nil {
		return fmt.Errorf("vec: write dims %s: %w", path, err)
	}
	if err := binary.Write(f, binary.LittleEndian, vec); err != nil {
		return fmt.Errorf("vec: write data %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("vec: sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("vec: close %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("vec: rename %s: %w", path, err)
	}
	committed = true
	syncDir(dir)
	return nil
}

// ReadVec reads a float32 vector from a .vec file. Handles both the
// new header format and legacy raw float32 files. Returns nil, nil if
// the file does not exist.
func ReadVec(path string) ([]float32, error) {
	vec, _, err := ReadVecMeta(path)
	return vec, err
}

// ReadVecMeta reads a vector and its metadata. Legacy files (no
// header) return an empty VecMeta — the model is unknown and the
// vector should be re-embedded.
func ReadVecMeta(path string) ([]float32, VecMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, VecMeta{}, nil
		}
		return nil, VecMeta{}, fmt.Errorf("vec: read %s: %w", path, err)
	}
	if len(data) < 4 {
		return nil, VecMeta{}, fmt.Errorf("vec: %s too small (%d bytes)", path, len(data))
	}

	if data[0] == vecMagic[0] && data[1] == vecMagic[1] &&
		data[2] == vecMagic[2] && data[3] == vecMagic[3] {
		return readHeaderFormat(path, data)
	}
	return readLegacyFormat(path, data)
}

func readHeaderFormat(path string, data []byte) ([]float32, VecMeta, error) {
	off := 4
	if len(data) < off+2 {
		return nil, VecMeta{}, fmt.Errorf("vec: %s truncated model length", path)
	}
	modelLen := int(binary.LittleEndian.Uint16(data[off:]))
	off += 2
	if len(data) < off+modelLen+4 {
		return nil, VecMeta{}, fmt.Errorf("vec: %s truncated model name or dims", path)
	}
	model := string(data[off : off+modelLen])
	off += modelLen
	dims := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	remaining := len(data) - off
	if remaining != dims*4 {
		return nil, VecMeta{}, fmt.Errorf("vec: %s expected %d floats (%d bytes), got %d bytes",
			path, dims, dims*4, remaining)
	}
	vec := make([]float32, dims)
	for i := range vec {
		bits := binary.LittleEndian.Uint32(data[off+i*4:])
		vec[i] = math.Float32frombits(bits)
	}
	return vec, VecMeta{Model: model, Dims: dims}, nil
}

func readLegacyFormat(path string, data []byte) ([]float32, VecMeta, error) {
	if len(data)%4 != 0 {
		return nil, VecMeta{}, fmt.Errorf("vec: %s has invalid size %d (not multiple of 4)", path, len(data))
	}
	n := len(data) / 4
	vec := make([]float32, n)
	for i := range vec {
		bits := binary.LittleEndian.Uint32(data[i*4:])
		vec[i] = math.Float32frombits(bits)
	}
	return vec, VecMeta{Dims: n}, nil
}

// NeedsReembed reports whether a .vec file should be re-embedded,
// either because it uses the legacy format (no model recorded) or
// because it was embedded with a different model.
func NeedsReembed(path, currentModel string) bool {
	_, meta, err := ReadVecMeta(path)
	if err != nil || meta.Model == "" {
		return true
	}
	return meta.Model != currentModel
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
