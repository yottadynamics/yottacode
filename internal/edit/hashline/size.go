package hashline

import (
	"fmt"
	"os"
)

// MaxEditFileBytes bounds how large a file this package will read fully into
// memory to apply a hash-anchored edit. This is not the same budget as the
// read tools' receipt-window caps: those bound what one read call returns to
// the model, while an edit target can be a much larger file than any single
// read window (offset can start anywhere in it). The bound here exists to
// reject an implausible target — a stray multi-GB log or data file someone
// points the tool at — before it is loaded into memory, not to constrain
// ordinary source files.
const MaxEditFileBytes = 64 * 1024 * 1024 // 64 MiB

// ErrFileTooLarge means the target file exceeds MaxEditFileBytes.
const ErrFileTooLarge ErrorKind = "file_too_large"

// ReadFileForEdit reads path as the source of a hash-anchored edit (for
// Apply, ApplyFile, or a manual Apply+ReplaceFileIfUnchanged sequence),
// refusing a file over MaxEditFileBytes before it is loaded into memory.
func ReadFileForEdit(path string) ([]byte, error) {
	return readFileForEditLimited(path, MaxEditFileBytes)
}

// readFileForEditLimited is ReadFileForEdit with an injectable limit, so
// tests can exercise the reject path without creating a MaxEditFileBytes
// file.
func readFileForEditLimited(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, &ApplyError{
			Kind: ErrFileTooLarge,
			Message: fmt.Sprintf("file is %d bytes, over the %d byte (%d MiB) limit for hash-anchored edits",
				info.Size(), maxBytes, maxBytes/(1024*1024)),
		}
	}
	return os.ReadFile(path)
}
