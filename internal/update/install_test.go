package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeArchive builds a .tar.gz containing a single regular file named `name`
// with the given contents, mirroring the goreleaser release archive layout.
func makeArchive(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Typeflag: tar.TypeReg,
		Mode:     0o755,
		Size:     int64(len(body)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serveRelease stands up an httptest server exposing /v<ver>/<asset> and
// /v<ver>/SHA256SUMS, computing the manifest from sumsBody (so a test can
// deliberately publish a wrong checksum). Returns the version string used.
func serveRelease(t *testing.T, archive []byte, sumChecksum string) (*httptest.Server, string) {
	t.Helper()
	ver := "9.9.9"
	asset := fmt.Sprintf("%s_%s_%s_%s.tar.gz", binaryName, ver, runtime.GOOS, runtime.GOARCH)
	sums := fmt.Sprintf("%s  %s\n", sumChecksum, asset)
	mux := http.NewServeMux()
	mux.HandleFunc("/v"+ver+"/SHA256SUMS", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sums))
	})
	mux.HandleFunc("/v"+ver+"/"+asset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	})
	srv := httptest.NewServer(mux)
	return srv, ver
}

func TestDownloadVerifiedBinary_HappyPath(t *testing.T) {
	want := []byte("#!/fake/yottacode binary v9.9.9\n")
	archive := makeArchive(t, binaryName, want)
	sum := sha256.Sum256(archive)
	srv, ver := serveRelease(t, archive, hex.EncodeToString(sum[:]))
	defer srv.Close()

	old := releaseDownloadBase
	releaseDownloadBase = srv.URL
	defer func() { releaseDownloadBase = old }()

	got, err := downloadVerifiedBinary(context.Background(), ver)
	if err != nil {
		t.Fatalf("downloadVerifiedBinary: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("extracted binary = %q, want %q", got, want)
	}
}

// The security-critical case: a tampered archive whose bytes don't match the
// published checksum must be rejected, never installed.
func TestDownloadVerifiedBinary_ChecksumMismatchRejected(t *testing.T) {
	archive := makeArchive(t, binaryName, []byte("malicious payload"))
	// Publish a checksum for different content, simulating a tampered or
	// MITM'd archive that doesn't match the signed manifest.
	wrong := sha256.Sum256([]byte("the legitimate release"))
	srv, ver := serveRelease(t, archive, hex.EncodeToString(wrong[:]))
	defer srv.Close()

	old := releaseDownloadBase
	releaseDownloadBase = srv.URL
	defer func() { releaseDownloadBase = old }()

	_, err := downloadVerifiedBinary(context.Background(), ver)
	if err == nil {
		t.Fatal("expected checksum mismatch to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want a checksum mismatch", err)
	}
}

func TestChecksumFor(t *testing.T) {
	sums := []byte("aaaa  yottacode_1.0.0_linux_amd64.tar.gz\n" +
		"bbbb  yottacode_1.0.0_darwin_arm64.tar.gz\n")
	got, err := checksumFor(sums, "yottacode_1.0.0_darwin_arm64.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != "bbbb" {
		t.Fatalf("checksum = %q, want bbbb", got)
	}
	if _, err := checksumFor(sums, "yottacode_1.0.0_windows_amd64.tar.gz"); err == nil {
		t.Fatal("expected error for an asset with no checksum entry")
	}
}

func TestExtractBinary_MissingFile(t *testing.T) {
	archive := makeArchive(t, "some-other-file", []byte("x"))
	if _, err := extractBinary(archive, binaryName); err == nil {
		t.Fatal("expected error when the named binary is absent from the archive")
	}
}

// replaceExecutable must atomically swap file contents and preserve the exec
// bit, the property the in-process upgrade relies on.
func TestReplaceExecutable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yottacode")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(path, []byte("new binary")); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Fatalf("contents = %q, want %q", got, "new binary")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("replaced binary is not executable: mode %v", info.Mode())
	}
}
