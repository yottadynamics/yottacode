package skills

// Phase 2 surfaces: `skills check` reports drift between the
// installed bytes and the lockfile's recorded hash; `skills update`
// re-runs Install for each tracked entry, skipping any whose on-disk
// copy diverges from the recorded hash unless --force is set. The
// dirty-skip rule is the design note's "treat as user-modified and
// skip" — without it, an `update` could silently clobber a hand-edit
// the user made to tweak a vendored skill.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

// CheckStatus enumerates the outcomes Check can report for one
// skill. Stable strings so CLI scripting can grep on them.
type CheckStatus string

const (
	// CheckOK — disk hash matches the lockfile entry; nothing to do.
	CheckOK CheckStatus = "ok"
	// CheckModified — disk hash differs from the lockfile entry.
	// User edited the installed copy in place.
	CheckModified CheckStatus = "modified"
	// CheckMissingLock — the skill exists on disk but has no
	// lockfile entry (installed pre-Phase 2, or the lockfile was
	// deleted). Update can't refresh it; user must reinstall to
	// re-record provenance.
	CheckMissingLock CheckStatus = "missing-lock"
	// CheckOrphanedLock — the lockfile has an entry but the dir is
	// gone (someone `rm -rf`'d the install). Update can refetch from
	// the recorded source.
	CheckOrphanedLock CheckStatus = "orphaned-lock"
	// CheckHashError — couldn't compute the disk hash (permissions,
	// truncated file, etc.). Surfaced verbatim so the user can fix
	// the underlying problem.
	CheckHashError CheckStatus = "hash-error"
)

// CheckResult is one line of the `skills check` report.
type CheckResult struct {
	Name     string
	Status   CheckStatus
	Lock     *LockEntry // nil when missing-lock
	DiskHash string     // empty when missing-lock or orphaned-lock
	Dir      string     // absolute path under UserSkillsDir, even when missing
	Error    string     // populated for hash-error
}

// CheckOptions tunes Check. DestRoot defaults to UserSkillsDir; tests
// inject a tempdir. Name filters to a single skill when non-empty.
type CheckOptions struct {
	DestRoot string
	Name     string
}

// Check inspects every user-scope skill (or just the named one) and
// reports its drift status. The returned slice is name-sorted; an
// empty result means there are no user-installed skills at all (and
// no lockfile entries to compare against).
func Check(opts CheckOptions) ([]CheckResult, error) {
	destRoot := opts.DestRoot
	if destRoot == "" {
		d, err := UserSkillsDir()
		if err != nil {
			return nil, err
		}
		destRoot = d
	}
	lf, err := LoadLockfile(destRoot)
	if err != nil {
		return nil, err
	}
	// Universe = union(on-disk dirs, lockfile entries). Either side
	// alone misses orphans (lockfile without dir) or missing-lock
	// (dir without lockfile).
	names := map[string]bool{}
	if entries, err := os.ReadDir(destRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() && skillNamePattern.MatchString(e.Name()) {
				names[e.Name()] = true
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for n := range lf.Entries {
		names[n] = true
	}
	if opts.Name != "" {
		if !names[opts.Name] {
			return nil, fmt.Errorf("no skill named %q found on disk or in lockfile", opts.Name)
		}
		names = map[string]bool{opts.Name: true}
	}
	out := make([]CheckResult, 0, len(names))
	for n := range names {
		out = append(out, checkOne(destRoot, n, lf))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// checkOne produces the CheckResult for a single skill name. Pure
// function modulo filesystem reads — no mutation of lf.
func checkOne(destRoot, name string, lf *Lockfile) CheckResult {
	dir := filepath.Join(destRoot, name)
	res := CheckResult{Name: name, Dir: dir}
	entry, hasLock := lf.Get(name)
	if hasLock {
		res.Lock = &entry
	}
	info, statErr := os.Stat(dir)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		if hasLock {
			res.Status = CheckOrphanedLock
		} else {
			res.Status = CheckMissingLock // shouldn't happen — names set is union; safety net
		}
		return res
	case statErr != nil:
		res.Status = CheckHashError
		res.Error = statErr.Error()
		return res
	case !info.IsDir():
		res.Status = CheckHashError
		res.Error = "not a directory"
		return res
	}
	disk, err := HashInstalledDir(dir)
	if err != nil {
		res.Status = CheckHashError
		res.Error = err.Error()
		return res
	}
	res.DiskHash = disk
	switch {
	case !hasLock:
		res.Status = CheckMissingLock
	case entry.Hash == "":
		// Lockfile entry without a hash (older entry, or a prior
		// install where the hash computation failed). Treat as
		// modified — can't prove the copy is clean.
		res.Status = CheckModified
	case entry.Hash != disk:
		res.Status = CheckModified
	default:
		res.Status = CheckOK
	}
	return res
}

// UpdateStatus enumerates the outcomes Update can report for one
// skill. Mirrors CheckStatus where applicable; adds the
// installer-specific outcomes (updated, unchanged, skipped-dirty).
type UpdateStatus string

const (
	// UpdateUpdated — re-fetched and the new bytes differ from what
	// was on disk before. Lockfile entry refreshed.
	UpdateUpdated UpdateStatus = "updated"
	// UpdateUnchanged — re-fetched and the bytes are identical to
	// what was already installed. Lockfile InstalledAt is refreshed
	// but nothing else moves.
	UpdateUnchanged UpdateStatus = "unchanged"
	// UpdateSkippedDirty — on-disk copy differs from the recorded
	// hash and --force was not set. The user-modified bytes are
	// preserved; no fetch happens.
	UpdateSkippedDirty UpdateStatus = "skipped-user-modified"
	// UpdateSkippedNoLock — no lockfile entry, so Update can't know
	// where to refetch from. Surface the actionable hint
	// ("reinstall to record provenance").
	UpdateSkippedNoLock UpdateStatus = "skipped-no-lockfile"
	// UpdateError — the re-fetch failed (network, parse, etc.).
	// Existing bytes are preserved when possible.
	UpdateError UpdateStatus = "error"
)

// UpdateResult is one line of the `skills update` report.
type UpdateResult struct {
	Name    string
	Status  UpdateStatus
	OldHash string
	NewHash string
	Message string
}

// UpdateOptions tunes Update. DestRoot defaults to UserSkillsDir;
// HTTPClient is forwarded to the installer for the URL/GitHub paths.
// Name filters to one skill (no-arg `skills update` updates every
// tracked entry). Force overrides the user-modified skip.
type UpdateOptions struct {
	DestRoot   string
	Name       string
	Force      bool
	HTTPClient *http.Client
}

// Update re-runs Install for each lockfile entry, refreshing the
// installed bytes from the originally-recorded source. The dirty
// detection ensures we never clobber a hand-edit unless the user
// explicitly opts in via Force.
func Update(opts UpdateOptions) ([]UpdateResult, error) {
	destRoot := opts.DestRoot
	if destRoot == "" {
		d, err := UserSkillsDir()
		if err != nil {
			return nil, err
		}
		destRoot = d
	}
	lf, err := LoadLockfile(destRoot)
	if err != nil {
		return nil, err
	}
	var targets []LockEntry
	if opts.Name != "" {
		entry, ok := lf.Get(opts.Name)
		if !ok {
			return nil, fmt.Errorf("no lockfile entry for %q — reinstall to record provenance", opts.Name)
		}
		targets = []LockEntry{entry}
	} else {
		targets = lf.AllSorted()
	}
	out := make([]UpdateResult, 0, len(targets))
	for _, entry := range targets {
		out = append(out, updateOne(destRoot, entry, opts))
	}
	return out, nil
}

// updateOne handles one lockfile entry end-to-end: drift check,
// refetch, hash comparison, result construction. The refetch goes
// through the regular Install path (with Force=true so the existing
// dir is replaced) so URL/GitHub auth, atomic staging, etc. behave
// identically to the initial install.
func updateOne(destRoot string, entry LockEntry, opts UpdateOptions) UpdateResult {
	res := UpdateResult{Name: entry.Name}
	dir := filepath.Join(destRoot, entry.Name)
	oldHash := entry.Hash
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		disk, herr := HashInstalledDir(dir)
		if herr != nil {
			res.Status = UpdateError
			res.Message = "hash current install: " + herr.Error()
			return res
		}
		oldHash = disk
		if !opts.Force && entry.Hash != "" && disk != entry.Hash {
			res.Status = UpdateSkippedDirty
			res.OldHash = disk
			res.Message = "on-disk hash differs from lockfile; pass --force to overwrite"
			return res
		}
	}
	installRes, err := Install(InstallOptions{
		Source:     entry.Source,
		Force:      true,
		DestRoot:   destRoot,
		HTTPClient: opts.HTTPClient,
	})
	if err != nil {
		res.Status = UpdateError
		res.Message = err.Error()
		res.OldHash = oldHash
		return res
	}
	res.OldHash = oldHash
	res.NewHash = installRes.Lock.Hash
	if installRes.Lock.Hash != "" && installRes.Lock.Hash == oldHash {
		res.Status = UpdateUnchanged
	} else {
		res.Status = UpdateUpdated
	}
	if len(installRes.Warnings) > 0 {
		res.Message = installRes.Warnings[0]
	}
	return res
}
