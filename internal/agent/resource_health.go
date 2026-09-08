package agent

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type ResourceFailureKind string

const (
	ResourceFailureUnknown    ResourceFailureKind = "unknown"
	ResourceFailurePIDs       ResourceFailureKind = "pids"
	ResourceFailureZombies    ResourceFailureKind = "zombies"
	ResourceFailureFilesystem ResourceFailureKind = "filesystem"
	ResourceFailurePath       ResourceFailureKind = "path"

	// Keep enough PID headroom for the parent UI, command recovery, and process
	// cleanup. Background fan-out is optional work and must not consume this
	// absolute reserve just because the cgroup limit happens to be large.
	backgroundPIDReserve int64 = 32
)

type ResourceHealthError struct {
	Kind   ResourceFailureKind
	Detail string
}

func (e *ResourceHealthError) Error() string {
	return fmt.Sprintf("resource preflight failed (%s): %s", e.Kind, e.Detail)
}

// ClassifyResourceFailure maps common process-start/runtime diagnostics to a
// short stable vocabulary. Order is intentional so mixed output is classified
// deterministically by the most actionable resource.
func ClassifyResourceFailure(output string) ResourceFailureKind {
	s := strings.ToLower(output)
	switch {
	case strings.Contains(s, "runtime: failed to create new os thread"), strings.Contains(s, "fatal error: newosproc"), strings.Contains(s, "pthread_create failed: resource temporarily unavailable"), strings.Contains(s, "cgroup pid limit reached"):
		return ResourceFailurePIDs
	case strings.Contains(s, "resource preflight failed (zombies)"):
		return ResourceFailureZombies
	case strings.Contains(s, "go: failed to trim cache: write") && strings.Contains(s, "no space left on device"), strings.Contains(s, "runtime: cannot allocate memory"):
		return ResourceFailureFilesystem
	case strings.Contains(s, "resource preflight failed (path)"):
		return ResourceFailurePath
	default:
		return ResourceFailureUnknown
	}
}

// classifyProcessStartFailure is used only when exec could not start a process.
// Generic phrases in a command's output are deliberately not considered here.
func classifyProcessStartFailure(err error) ResourceFailureKind {
	if err == nil {
		return ResourceFailureUnknown
	}
	if errors.Is(err, syscall.EAGAIN) {
		return ResourceFailurePIDs
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) {
		return ResourceFailureFilesystem
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.ENOTDIR) {
		return ResourceFailurePath
	}
	return ResourceFailureUnknown
}

func resourcePreflight(root string) error {
	if err := checkExecutionPath(root); err != nil {
		return err
	}
	if cgroupDir, ok := currentCgroupV2Dir("/proc", "/sys/fs/cgroup"); ok {
		if err := checkCgroupPIDs(cgroupDir); err != nil {
			return err
		}
	}
	if err := checkZombieProcesses("/proc"); err != nil {
		return err
	}
	return checkFilesystemSpace(root)
}

// currentCgroupV2Dir resolves this process's unified hierarchy membership.
// Delegated/user slices commonly place the process below the mount root, so
// reading counters directly from /sys/fs/cgroup silently checks the wrong pool.
func currentCgroupV2Dir(procRoot, cgroupRoot string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(procRoot, "self", "cgroup"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || parts[0] != "0" || parts[1] != "" {
			continue
		}
		rel := strings.TrimPrefix(filepath.Clean(parts[2]), string(filepath.Separator))
		if rel == "." {
			rel = ""
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		return filepath.Join(cgroupRoot, rel), true
	}
	return "", false
}

func checkExecutionPath(root string) error {
	if !filepath.IsAbs(root) {
		return &ResourceHealthError{Kind: ResourceFailurePath, Detail: "working directory must be absolute"}
	}
	info, err := os.Stat(root)
	if err != nil {
		return &ResourceHealthError{Kind: ResourceFailurePath, Detail: fmt.Sprintf("working directory unavailable: %v", err)}
	}
	if !info.IsDir() {
		return &ResourceHealthError{Kind: ResourceFailurePath, Detail: "working directory is not a directory"}
	}
	return nil
}

func checkCgroupPIDs(root string) error {
	return checkCgroupPIDReserve(root, 0)
}

func checkCgroupPIDReserve(root string, reserve int64) error {
	current, errCurrent := readResourceInt(filepath.Join(root, "pids.current"))
	maxData, errMax := os.ReadFile(filepath.Join(root, "pids.max"))
	if errCurrent != nil || errMax != nil { // absent/non-cgroup hosts are healthy-unknown
		return nil
	}
	maxText := strings.TrimSpace(string(maxData))
	if maxText == "max" {
		return nil
	}
	max, err := strconv.ParseInt(maxText, 10, 64)
	if err != nil || max <= 0 {
		return nil
	}
	if remaining := max - current; remaining <= reserve {
		if reserve > 0 {
			return &ResourceHealthError{Kind: ResourceFailurePIDs, Detail: fmt.Sprintf("cgroup has %d PID slots remaining; background work requires more than the %d-slot recovery reserve", remaining, reserve)}
		}
		return &ResourceHealthError{Kind: ResourceFailurePIDs, Detail: "cgroup PID limit reached"}
	}
	return nil
}

// backgroundResourcePreflight gates only optional detached work. Generic
// run_bash remains available so a user can inspect and recover a sick session.
func backgroundResourcePreflight() error {
	if cgroupDir, ok := currentCgroupV2Dir("/proc", "/sys/fs/cgroup"); ok {
		return checkCgroupPIDReserve(cgroupDir, backgroundPIDReserve)
	}
	return nil
}

func readResourceInt(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
}

func checkZombieProcesses(procRoot string) error {
	selfScope, ok := processCgroupScope(procRoot, "self")
	if !ok {
		// A host-wide count is not an admission signal: zombies in another
		// container or user slice cannot be reaped by this process.
		return nil
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil
	}
	zombies := 0
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		if scope, ok := processCgroupScope(procRoot, entry.Name()); !ok || scope != selfScope {
			continue
		}
		data, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "stat"))
		if err != nil {
			continue
		}
		// Field 2 (comm) is parenthesized and may contain spaces. State is the
		// first field after its closing parenthesis, not fields[2] of a whole-line
		// whitespace split.
		if state, ok := procStatState(data); ok && state == "Z" {
			zombies++
		}
	}
	if zombies >= 512 {
		return &ResourceHealthError{Kind: ResourceFailureZombies, Detail: fmt.Sprintf("%d zombie processes detected", zombies)}
	}
	return nil
}

func processCgroupScope(procRoot, pid string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(procRoot, pid, "cgroup"))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" && parts[1] == "" && parts[2] != "" {
			return filepath.Clean(parts[2]), true
		}
	}
	return "", false
}

func procStatState(data []byte) (string, bool) {
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 || i+1 >= len(data) {
		return "", false
	}
	fields := strings.Fields(string(data[i+1:]))
	if len(fields) == 0 {
		return "", false
	}
	return fields[0], true
}

func checkFilesystemSpace(path string) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return &ResourceHealthError{Kind: ResourceFailurePath, Detail: fmt.Sprintf("cannot inspect working directory: %v", err)}
		}
		return nil
	}
	available := stat.Bavail * uint64(stat.Bsize)
	if available < 64<<20 {
		return &ResourceHealthError{Kind: ResourceFailureFilesystem, Detail: fmt.Sprintf("only %d MiB available", available>>20)}
	}
	return nil
}
