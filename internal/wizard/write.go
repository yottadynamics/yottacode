package wizard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteOutcome reports what the writer actually did, so callers can
// surface the right "wrote /path" lines and the right backup hint.
type WriteOutcome struct {
	ConfigPath  string
	EnvPath     string
	BackupPath  string // empty when no backup was made
	WroteConfig bool
	WroteEnv    bool
	WroteBackup bool
}

// WriteOptions controls where the wizard writes and whether it backs
// up. Defaults aim at the standard locations.
type WriteOptions struct {
	// ConfigPath defaults to ~/.yottacode/config.toml. Override for
	// `--config /custom/path.toml` and tests.
	ConfigPath string

	// EnvPath defaults to ~/.yottacode/.env. Override for
	// `--env-file` and tests.
	EnvPath string

	// Force=true backs up an existing config.toml to
	// `<path>.bak-<unix-ts>` and replaces it. With Force=false the
	// writer attempts a structural merge (existing tunables
	// preserved; providers union'd by name).
	Force bool

	// SkipEnvWrite skips the .env step entirely. Mirrors
	// Plan.SkipEnvWrite — supplied separately so the CLI flag wins
	// independently of the Plan content.
	SkipEnvWrite bool
}

// Apply writes the Plan to disk per opts and returns a WriteOutcome.
// The function is the single entry point used by both the interactive
// wizard's "write" step and the non-interactive `yottacode setup -y`
// path. All writes are atomic (tmp+rename) and the .env permission is
// chmod'd to 0600 before rename.
func Apply(plan Plan, opts WriteOptions) (WriteOutcome, error) {
	if err := plan.Validate(); err != nil {
		return WriteOutcome{}, err
	}

	out := WriteOutcome{
		ConfigPath: opts.ConfigPath,
		EnvPath:    opts.EnvPath,
	}
	if out.ConfigPath == "" {
		return out, fmt.Errorf("wizard: ConfigPath required")
	}
	if out.EnvPath == "" {
		return out, fmt.Errorf("wizard: EnvPath required")
	}

	if err := os.MkdirAll(filepath.Dir(out.ConfigPath), 0o700); err != nil {
		return out, fmt.Errorf("wizard: mkdir config dir: %w", err)
	}

	body, backupTo, err := renderConfigFile(plan, out.ConfigPath, opts.Force)
	if err != nil {
		return out, err
	}
	if backupTo != "" {
		if err := os.Rename(out.ConfigPath, backupTo); err != nil {
			return out, fmt.Errorf("wizard: backup config: %w", err)
		}
		out.BackupPath = backupTo
		out.WroteBackup = true
	}
	if err := atomicWrite(out.ConfigPath, []byte(body), 0o644); err != nil {
		return out, fmt.Errorf("wizard: write config: %w", err)
	}
	out.WroteConfig = true

	if !opts.SkipEnvWrite {
		envBody := renderEnvFile(plan, out.EnvPath)
		if envBody != "" {
			if err := os.MkdirAll(filepath.Dir(out.EnvPath), 0o700); err != nil {
				return out, fmt.Errorf("wizard: mkdir env dir: %w", err)
			}
			if err := atomicWriteSecret(out.EnvPath, []byte(envBody)); err != nil {
				return out, fmt.Errorf("wizard: write .env: %w", err)
			}
			out.WroteEnv = true
		}
	}

	return out, nil
}

// renderConfigFile decides whether to emit a fresh file from the Plan
// or to merge the Plan into an existing one. Returns the body to
// write and a backup target path (non-empty only when Force=true and
// the target file exists).
func renderConfigFile(plan Plan, target string, force bool) (body string, backup string, err error) {
	existed := false
	if _, statErr := os.Stat(target); statErr == nil {
		existed = true
	}
	if !existed {
		return plan.RenderTOML(), "", nil
	}
	if force {
		ts := time.Now().Unix()
		return plan.RenderTOML(), fmt.Sprintf("%s.bak-%d", target, ts), nil
	}
	merged, err := mergeConfig(plan, target)
	if err != nil {
		return "", "", err
	}
	return merged, "", nil
}

// renderEnvFile decides what .env body to write. When the target
// already exists, we preserve any keys not in plan.EnvKeys and
// overwrite (or append) the ones that are. The result is a complete
// file body so the atomic-write path is uniform whether we merged or
// created.
func renderEnvFile(plan Plan, target string) string {
	if plan.SkipEnvWrite || len(plan.EnvKeys) == 0 {
		return ""
	}
	existing := readEnvFile(target)
	for k, v := range plan.EnvKeys {
		existing[k] = v
	}
	return renderEnvMap(existing)
}

// readEnvFile is a forgiving best-effort reader: it returns whatever
// well-formed KEY=VALUE pairs the file contains, ignoring comments,
// blank lines, and any line we don't understand. Used by the merge
// path so we don't blow away the user's hand-edited keys.
func readEnvFile(path string) map[string]string {
	out := map[string]string{}
	body, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		// Strip wrapping quotes — same minimal subset our dotenv
		// loader supports. We don't process escapes; API keys never
		// contain them.
		if len(v) >= 2 {
			f, l := v[0], v[len(v)-1]
			if (f == '"' && l == '"') || (f == '\'' && l == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// renderEnvMap emits the merged map in alphabetical order with the
// wizard's standard header.
func renderEnvMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Stable order so diffs read cleanly.
	sortStrings(keys)
	var b strings.Builder
	b.WriteString("# yottacode-managed API keys — written by `yottacode setup`.\n")
	b.WriteString("# Mode 0600. Add to git via .gitignore (yottacode does this for you).\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, m[k])
	}
	return b.String()
}

// atomicWrite writes body to path via tmp+rename. Mode applies to the
// final file. Caller is expected to have created the directory.
func atomicWrite(path string, body []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, mode); err != nil {
		return err
	}
	// Reapply mode in case umask trimmed it; harmless when it didn't.
	_ = os.Chmod(tmp, mode)
	return os.Rename(tmp, path)
}

// atomicWriteSecret is atomicWrite for files that hold secrets:
// permission is forced to 0600 and the rename happens after the
// chmod so a glance during the write window can never see a
// world-readable temp file.
func atomicWriteSecret(path string, body []byte) error {
	tmp := path + ".tmp"
	// Open with explicit 0600; some filesystems honor mode at create
	// time but not at chmod time (reluctant ACLs). Belt + suspenders.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func sortStrings(s []string) {
	// avoid pulling sort just for one call
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
