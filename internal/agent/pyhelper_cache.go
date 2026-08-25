package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// pyHelperCacheDir returns ~/.yottacode/cache/doc-helpers, where a
// host-only session (no podman sandbox) materializes the embedded
// Python driver scripts documents.ResolvePyHelperScript needs —
// shared by ReadDocumentTool's PDF table-extraction tier and
// CreateDocumentTool's docx template-fill path so both cache into the
// same place rather than each picking its own.
func pyHelperCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir for python helper cache: %w", err)
	}
	return filepath.Join(home, ".yottacode", "cache", "doc-helpers"), nil
}
