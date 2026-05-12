package tui

import (
	"os/exec"
	"strings"
	"testing"
)

// hasLess gates the resolver tests that depend on `less` being on
// PATH. Most CI/dev environments have it; tests that need a
// less-less environment should set $PATH explicitly via t.Setenv.
func hasLess() bool {
	_, err := exec.LookPath("less")
	return err == nil
}

func TestResolvePagerCommand_LessDefaultIsScrollable(t *testing.T) {
	if !hasLess() {
		t.Skip("less not available on PATH")
	}
	// No $PAGER set → falls through to the built-in less default.
	t.Setenv("YOTTACODE_PAGER", "")
	t.Setenv("PAGER", "")

	argv := resolvePagerCommand("/tmp/x.md", false)
	if len(argv) == 0 {
		t.Fatalf("expected a pager command for less; got empty")
	}
	if argv[0] != "less" {
		t.Errorf("argv[0] = %q, want less", argv[0])
	}
	joined := strings.Join(argv, " ")
	// -R: ANSI escapes through. Must be present.
	if !strings.Contains(joined, "-R") {
		t.Errorf("argv missing -R flag; got %v", argv)
	}
	// MUST NOT contain:
	//   -F  (auto-quit on one-screen: flashed and closed on
	//        just-started subagents with header-only transcripts);
	//   -RF (the combined form is just -R + -F, same regression);
	//   +F  (follow mode broke scrolling discoverability);
	//   +G  (combined with -F it caused instant-quit on short
	//        transcripts);
	//   -P  (custom-prompt parsing was fragile across less versions).
	for _, bad := range []string{"-F", "-RF", "+F", "+G", "-P"} {
		for _, a := range argv {
			if a == bad {
				t.Errorf("argv contains %q which caused a regression earlier; got %v", bad, argv)
			}
		}
	}
	if argv[len(argv)-1] != "/tmp/x.md" {
		t.Errorf("last argv element = %q, want path", argv[len(argv)-1])
	}
}

func TestResolvePagerCommand_LessDefaultIgnoresRunningHint(t *testing.T) {
	// The `running` parameter is reserved for future use but does
	// not currently change behavior — same scroll-from-top default
	// for both running and completed subagents.
	if !hasLess() {
		t.Skip("less not available on PATH")
	}
	t.Setenv("YOTTACODE_PAGER", "")
	t.Setenv("PAGER", "")

	runArgv := resolvePagerCommand("/tmp/x.md", true)
	doneArgv := resolvePagerCommand("/tmp/x.md", false)
	if strings.Join(runArgv, " ") != strings.Join(doneArgv, " ") {
		t.Errorf("running and completed should produce identical pager args; got\n  running: %v\n  done:    %v", runArgv, doneArgv)
	}
}

func TestResolvePagerCommand_HonorsEnvPagerVerbatim(t *testing.T) {
	// $PAGER must be honored verbatim — the user picked their pager;
	// we don't inject our own flags into it.
	t.Setenv("YOTTACODE_PAGER", "")
	t.Setenv("PAGER", "cat -n")

	argv := resolvePagerCommand("/tmp/x.md", false)
	if argv[0] != "cat" {
		t.Errorf("argv[0] = %q, want cat (from $PAGER)", argv[0])
	}
	for _, a := range argv {
		if a == "+G" || a == "+F" || a == "-RF" || a == "-P" {
			t.Errorf("custom $PAGER must not have less flags injected: %v", argv)
		}
	}
}

func TestResolvePagerCommand_EnvPagerLessPassesFlagsThrough(t *testing.T) {
	// User has $PAGER=less with their own flags. Those flags pass
	// through; we don't inject our flags on top.
	t.Setenv("YOTTACODE_PAGER", "")
	t.Setenv("PAGER", "less -FRSX")

	argv := resolvePagerCommand("/tmp/x.md", true)
	if argv[0] != "less" {
		t.Fatalf("argv[0] = %q, want less", argv[0])
	}
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "-FRSX") {
		t.Errorf("user's flags from $PAGER should pass through; got %v", argv)
	}
	if strings.Contains(joined, "+G") || strings.Contains(joined, "+F") || strings.Contains(joined, "-P") {
		t.Errorf("$PAGER-supplied less should not get extra flags injected: %v", argv)
	}
}

func TestResolvePagerCommand_YottacodePagerWinsOverPager(t *testing.T) {
	t.Setenv("YOTTACODE_PAGER", "bat -p")
	t.Setenv("PAGER", "less")

	argv := resolvePagerCommand("/tmp/x.md", false)
	if argv[0] != "bat" {
		t.Errorf("argv[0] = %q, want bat (YOTTACODE_PAGER overrides PAGER)", argv[0])
	}
}
