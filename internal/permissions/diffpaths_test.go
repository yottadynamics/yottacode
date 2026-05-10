package permissions

import (
	"reflect"
	"testing"
)

func TestParseDiffPaths_Modify(t *testing.T) {
	diff := "diff --git a/foo.txt b/foo.txt\n" +
		"index 1..2 100644\n" +
		"--- a/foo.txt\n" +
		"+++ b/foo.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old\n+new\n"
	got := ParseDiffPaths(diff)
	want := []string{"foo.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDiffPaths_NewFile(t *testing.T) {
	diff := "diff --git a/new.go b/new.go\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ b/new.go\n" +
		"@@ -0,0 +1 @@\n+x\n"
	got := ParseDiffPaths(diff)
	if !reflect.DeepEqual(got, []string{"new.go"}) {
		t.Errorf("got %v", got)
	}
}

func TestParseDiffPaths_DeletedFile(t *testing.T) {
	diff := "diff --git a/old.go b/old.go\n" +
		"deleted file mode 100644\n" +
		"--- a/old.go\n" +
		"+++ /dev/null\n" +
		"@@ -1 +0,0 @@\n-x\n"
	got := ParseDiffPaths(diff)
	if !reflect.DeepEqual(got, []string{"old.go"}) {
		t.Errorf("got %v", got)
	}
}

func TestParseDiffPaths_Rename(t *testing.T) {
	diff := "diff --git a/old.go b/new.go\n" +
		"similarity index 100%\n" +
		"rename from old.go\n" +
		"rename to new.go\n"
	got := ParseDiffPaths(diff)
	want := []string{"old.go", "new.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDiffPaths_MultipleFiles(t *testing.T) {
	diff := "--- a/one.go\n+++ b/one.go\n@@ -1 +1 @@\n-a\n+b\n" +
		"--- a/two.go\n+++ b/two.go\n@@ -1 +1 @@\n-a\n+b\n"
	got := ParseDiffPaths(diff)
	want := []string{"one.go", "two.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseDiffPaths_HeaderlessDiff(t *testing.T) {
	got := ParseDiffPaths("garbage\nno headers here\n")
	if len(got) != 0 {
		t.Errorf("expected no paths, got %v", got)
	}
}

func TestParseDiffPaths_TrailingTimestamp(t *testing.T) {
	// Some tools emit `--- a/foo\t<timestamp>`; we strip past the tab.
	diff := "--- a/foo.txt\t2024-01-01 00:00:00\n+++ b/foo.txt\t2024-01-02 00:00:00\n"
	got := ParseDiffPaths(diff)
	if !reflect.DeepEqual(got, []string{"foo.txt"}) {
		t.Errorf("got %v", got)
	}
}

func TestParseDiffPaths_QuotedPath(t *testing.T) {
	diff := "--- \"a/has space.txt\"\n+++ \"b/has space.txt\"\n"
	got := ParseDiffPaths(diff)
	if !reflect.DeepEqual(got, []string{"has space.txt"}) {
		t.Errorf("got %v", got)
	}
}

func TestParseDiffPaths_PathTraversal(t *testing.T) {
	// Validation against DefaultDenyPaths happens later in
	// ValidateWritePath; the parser surfaces the path verbatim so the
	// validator can resolve and refuse it.
	diff := "--- a/../../etc/passwd\n+++ b/../../etc/passwd\n"
	got := ParseDiffPaths(diff)
	if !reflect.DeepEqual(got, []string{"../../etc/passwd"}) {
		t.Errorf("got %v", got)
	}
}
