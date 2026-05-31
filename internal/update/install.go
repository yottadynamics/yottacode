package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/version"
)

// releaseDownloadBase is the GitHub release asset root. Overridable in tests.
var releaseDownloadBase = "https://github.com/yottadynamics/yottacode/releases/download"

// binaryName is the name of the executable inside the release archive.
const binaryName = "yottacode"

// maxDownloadBytes caps each release download so a compromised or
// misbehaving server can't stream unbounded bytes into memory before the
// checksum is even checked. Release archives are tens of MB; 256 MiB is
// generous headroom for both the archive and the (tiny) SHA256SUMS file.
const maxDownloadBytes = 256 << 20

// InstallRelease upgrades the running binary to the given version by
// downloading the matching release archive, verifying it against the
// release's SHA256SUMS file, extracting the binary, and atomically
// replacing the current executable.
//
// This replaces the old `curl … | bash` install path: nothing is piped to
// a shell, and the download is rejected unless its SHA-256 matches the
// SHA256SUMS checksum manifest published alongside the release. The
// manifest is fetched over HTTPS from the same GitHub release, so this
// verifies integrity (no corrupt or partial download installs) and pins
// the exact version — it is not an end-to-end signature check, which
// would require a separately-trusted signing key. Any failure — network,
// checksum mismatch, missing asset — returns an error without touching
// the installed binary.
//
// ver is the target version without a leading "v" (e.g. "0.3.0"), matching
// update.Result.Latest.
func InstallRelease(ctx context.Context, ver string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}
	// Resolve symlinks so we replace the real file, not a launcher link.
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}

	bin, err := downloadVerifiedBinary(ctx, ver)
	if err != nil {
		return err
	}
	return replaceExecutable(exePath, bin)
}

// downloadVerifiedBinary fetches the release archive for ver + the current
// platform, checks its SHA-256 against the release's SHA256SUMS manifest,
// and returns the extracted binary bytes. It performs no filesystem
// mutation, which keeps the verification path unit-testable.
func downloadVerifiedBinary(ctx context.Context, ver string) ([]byte, error) {
	asset := fmt.Sprintf("%s_%s_%s_%s.tar.gz", binaryName, ver, runtime.GOOS, runtime.GOARCH)
	base := fmt.Sprintf("%s/v%s", releaseDownloadBase, ver)

	sums, err := httpGetBytes(ctx, base+"/SHA256SUMS")
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return nil, err
	}

	archive, err := httpGetBytes(ctx, base+"/"+asset)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	got := sha256.Sum256(archive)
	gotHex := hex.EncodeToString(got[:])
	if !strings.EqualFold(gotHex, want) {
		return nil, fmt.Errorf("checksum mismatch for %s: got %s, want %s — refusing to install", asset, gotHex, want)
	}

	bin, err := extractBinary(archive, binaryName)
	if err != nil {
		return nil, err
	}
	return bin, nil
}

// checksumFor returns the hex SHA-256 recorded for asset in a goreleaser
// SHA256SUMS file ("<sha256>  <filename>" per line; a BSD-mode leading "*"
// on the filename is tolerated).
func checksumFor(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("parse checksums: %w", err)
	}
	return "", fmt.Errorf("no checksum entry for %s", asset)
}

// extractBinary pulls the named regular file out of a .tar.gz archive,
// matching on base name so a possible wrapping directory doesn't matter.
func extractBinary(targz []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(targz))
	if err != nil {
		return nil, fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != name {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", name, err)
		}
		return data, nil
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}

// replaceExecutable atomically swaps the file at path with data. It writes a
// temp file in the same directory (so os.Rename is atomic on the same
// filesystem) and renames it over the target. Replacing a running binary is
// safe on Linux/macOS: the kernel keeps the open inode for the live process
// while the path points at the new file.
func replaceExecutable(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".yottacode-upgrade-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w (need write access to the install directory)", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("flush new binary: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return fmt.Errorf("chmod new binary: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func httpGetBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yottacode/"+version.Current)
	// Generous timeout: release archives are tens of MB. The default
	// client follows GitHub's redirect to the CDN object store.
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	// Bound the read: a compromised or misbehaving server must not be able
	// to exhaust memory before the checksum check runs. Read one byte past
	// the cap so an oversized body surfaces as a clear error rather than a
	// silently-truncated (and then checksum-mismatching) download.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxDownloadBytes {
		return nil, fmt.Errorf("GET %s: response exceeds %d-byte cap", url, maxDownloadBytes)
	}
	return data, nil
}
