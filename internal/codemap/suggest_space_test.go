package codemap

import (
	"context"
	"testing"
)

// Paths with whitespace used to be dropped from suggested context because
// the @-reference syntax could not express them. Quoted refs can, so they
// must now be suggested like any other file.
func TestSuggestedContextKeepsPathsWithSpaces(t *testing.T) {
	root := t.TempDir()
	write(t, root, "my pkg/app.go", "package app\n\nfunc Run() {}\n")
	write(t, root, "my pkg/app_test.go", "package app\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var got []string
	for _, s := range idx.SuggestedContext([]string{"my pkg/app.go"}, 8) {
		got = append(got, s.File.RelPath)
	}
	if len(got) == 0 || got[0] != "my pkg/app.go" {
		t.Fatalf("suggestions = %v, want the spaced changed file first", got)
	}
}
