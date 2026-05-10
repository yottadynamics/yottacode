// Package dotenv loads KEY=VALUE pairs from .env files into a map. It is
// deliberately minimal: yottacode uses .env exclusively for API keys, so
// the parser only needs to cover what dev tooling already produces — quoted
// values, comments, optional `export` prefix.
//
// The format follows the de-facto subset shared across Docker, direnv, and
// godotenv:
//
//	# comment line
//	KEY=value
//	KEY="quoted value"      # double quotes preserve as-is
//	KEY='single quoted'     # single quotes preserve as-is, no escapes
//	export KEY=value        # leading export is silently dropped
//	KEY=value # trailing comment after a space
//
// Multi-line values, escape sequences inside double quotes, and variable
// interpolation are NOT supported. The moment we need any of those we'll
// reach for github.com/joho/godotenv; today we don't.
package dotenv

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultPath returns ~/.yottacode/.env — the home-scoped, yottacode-
// managed env file where API keys land. Save/SetKey/DeleteKey use it
// as their default; callers needing a per-repo .env pass a path
// directly.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("dotenv: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".yottacode", ".env"), nil
}

// Save writes kv atomically to path at mode 0600. Keys are sorted so
// diffs read stably across writes; values are double-quoted so they
// survive any whitespace/special chars. A "managed" header tells
// users this file is rewritten by yottacode commands. Parent dirs
// are created at 0700 if missing. Empty kv writes just the header
// (a blank but valid yottacode-managed file).
func Save(path string, kv map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("dotenv: mkdir %s: %w", filepath.Dir(path), err)
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# yottacode-managed API keys.\n")
	b.WriteString("# Existing keys are preserved; the OS environment still wins at runtime.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%q\n", k, kv[k])
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("dotenv: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("dotenv: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// SetKey is the merge-and-write convenience: load existing entries
// from path (missing file = empty layer), set name=value, write back.
// Used by `provider add` flows that want to persist one new key
// without disturbing the rest.
func SetKey(path, name, value string) error {
	if !validKey(name) {
		return fmt.Errorf("dotenv: invalid key %q", name)
	}
	existing, err := Load(path)
	if err != nil {
		return err
	}
	existing[name] = value
	return Save(path, existing)
}

// DeleteKey removes name from path and writes back. Returns
// (true, nil) when the key was present and the file was rewritten,
// (false, nil) when the file or key is missing (a no-op, not an
// error — the caller's typical question is "is the key gone?" and
// the answer is yes either way), or (false, err) on a real I/O
// failure. Used by `provider remove` to clean up the matching API
// key from ~/.yottacode/.env so removing a profile actually removes
// its credential.
func DeleteKey(path, name string) (bool, error) {
	existing, err := Load(path)
	if err != nil {
		return false, err
	}
	if _, ok := existing[name]; !ok {
		return false, nil
	}
	delete(existing, name)
	if err := Save(path, existing); err != nil {
		return false, err
	}
	return true, nil
}

// Load parses the file at path and returns its KEY=VALUE pairs. A missing
// file is not an error — the caller can chain Load calls and treat absent
// files as empty layers.
func Load(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("dotenv: open %s: %w", path, err)
	}
	defer f.Close()
	return parse(f, path)
}

// LoadChain loads multiple .env files in order. Later files override earlier
// ones. Missing files are skipped silently. Callers pass the chain in
// "lowest precedence first" order — typically (user-global, project-local).
func LoadChain(paths ...string) (map[string]string, error) {
	merged := map[string]string{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		layer, err := Load(p)
		if err != nil {
			return merged, err
		}
		for k, v := range layer {
			merged[k] = v
		}
	}
	return merged, nil
}

// Apply populates os.Environ with values from m, but never overrides a
// variable that's already set. The OS environment wins so CI/CD secrets
// take precedence over a checked-in .env. Returns the list of keys
// actually applied (for diagnostics).
func Apply(m map[string]string) []string {
	applied := make([]string, 0, len(m))
	for k, v := range m {
		if _, ok := os.LookupEnv(k); ok {
			continue
		}
		_ = os.Setenv(k, v)
		applied = append(applied, k)
	}
	return applied
}

// LoadAndApply is the common-case helper: load a chain of files and merge
// into the live environment without overriding existing values.
func LoadAndApply(paths ...string) ([]string, error) {
	m, err := LoadChain(paths...)
	if err != nil {
		return nil, err
	}
	return Apply(m), nil
}

func parse(r io.Reader, source string) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")
		raw = strings.TrimSpace(raw)
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			return out, fmt.Errorf("dotenv: %s:%d: expected KEY=VALUE, got %q", source, line, raw)
		}
		key := strings.TrimSpace(raw[:eq])
		val := strings.TrimSpace(raw[eq+1:])
		if !validKey(key) {
			return out, fmt.Errorf("dotenv: %s:%d: invalid key %q", source, line, key)
		}
		out[key] = unquote(val)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("dotenv: read %s: %w", source, err)
	}
	return out, nil
}

// unquote strips matched surrounding quotes and (for unquoted values) drops
// any trailing `# comment` segment. We do NOT process escape sequences —
// API keys never contain them and supporting escapes invites confusion
// about whether `\n` means a literal backslash-n or a newline.
func unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return s[1 : len(s)-1]
		}
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// validKey rejects empty keys, keys starting with a digit, and keys
// containing characters outside [A-Za-z0-9_]. Mirrors POSIX env-var rules.
func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
