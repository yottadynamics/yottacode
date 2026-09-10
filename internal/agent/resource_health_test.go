package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestClassifyResourceFailure(t *testing.T) {
	cases := []struct {
		output string
		want   ResourceFailureKind
	}{
		{"runtime: failed to create new OS thread (have 8 already)", ResourceFailurePIDs},
		{"resource preflight failed (zombies): 512 zombie processes detected", ResourceFailureZombies},
		{"go: failed to trim cache: write /cache: no space left on device", ResourceFailureFilesystem},
		{"resource preflight failed (path): working directory is not a directory", ResourceFailurePath},
		{"tests failed", ResourceFailureUnknown},
		{"assertion: permission denied", ResourceFailureUnknown},
		{"wanted no such file or directory", ResourceFailureUnknown},
		{"message contains fork/exec and resource temporarily unavailable", ResourceFailureUnknown},
		{"write fixture: no space left on device", ResourceFailureUnknown},
	}
	for _, tc := range cases {
		if got := ClassifyResourceFailure(tc.output); got != tc.want {
			t.Errorf("ClassifyResourceFailure(%q) = %q, want %q", tc.output, got, tc.want)
		}
	}
}

func TestCurrentCgroupV2Dir(t *testing.T) {
	proc := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, "self", "cgroup"), []byte("0::/user.slice/session.scope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := currentCgroupV2Dir(proc, "/sys/fs/cgroup")
	if !ok || got != "/sys/fs/cgroup/user.slice/session.scope" {
		t.Fatalf("currentCgroupV2Dir = %q, %v", got, ok)
	}
}

func TestProcStatStateCommMayContainSpaces(t *testing.T) {
	for _, stat := range []string{"42 (ordinary) S 1 2", "42 (worker name with spaces) Z 1 2", "42 (name ) paren) R 1 2"} {
		state, ok := procStatState([]byte(stat))
		if !ok {
			t.Fatalf("procStatState(%q) failed", stat)
		}
		want := "S"
		if strings.Contains(stat, ") Z ") {
			want = "Z"
		} else if strings.Contains(stat, ") R ") {
			want = "R"
		}
		if state != want {
			t.Errorf("procStatState(%q) = %q, want %q", stat, state, want)
		}
	}
}

func TestCheckZombieProcessesIsCgroupScoped(t *testing.T) {
	proc := t.TempDir()
	writeProc := func(pid, scope string) {
		dir := filepath.Join(proc, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte("0::"+scope+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if pid != "self" {
			if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(pid+" (zombie) Z 1 2"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeProc("self", "/ours")
	for i := 1; i <= 512; i++ {
		writeProc(strconv.Itoa(i), "/theirs")
	}
	if err := checkZombieProcesses(proc); err != nil {
		t.Fatalf("unrelated zombies blocked admission: %v", err)
	}
	// Unknown scope is advisory rather than falling back to a host-wide count.
	if err := os.Remove(filepath.Join(proc, "self", "cgroup")); err != nil {
		t.Fatal(err)
	}
	if err := checkZombieProcesses(proc); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCgroupPIDs(t *testing.T) {
	for _, tc := range []struct {
		name, current, max string
		want               ResourceFailureKind
		reserve            int64
	}{
		{"healthy", "10", "100", "", 0},
		{"unlimited", "100", "max", "", 32},
		{"exhausted", "100", "100", ResourceFailurePIDs, 0},
		{"reserve", "69", "100", ResourceFailurePIDs, 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.WriteFile(filepath.Join(dir, "pids.current"), []byte(tc.current), 0o644)
			_ = os.WriteFile(filepath.Join(dir, "pids.max"), []byte(tc.max), 0o644)
			err := checkCgroupPIDReserve(dir, tc.reserve)
			if tc.want == "" && err != nil {
				t.Fatal(err)
			}
			if tc.want != "" {
				var health *ResourceHealthError
				if !errors.As(err, &health) || health.Kind != tc.want {
					t.Fatalf("error = %v, want kind %q", err, tc.want)
				}
			}
		})
	}
}
