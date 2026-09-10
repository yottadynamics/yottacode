package codemap

import (
	"context"
	"strings"
	"testing"
)

func TestBuildSubsystemOverviewGoEntryPointAndSurface(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "cmd/tool/main.go", "package main\n\nimport \"example.com/test/internal/widget\"\n\nfunc main() { widget.New() }\n")
	write(t, root, "internal/widget/widget.go", "package widget\n\ntype Widget struct{}\n\nfunc New() *Widget { return &Widget{} }\nfunc helper() {}\n")
	write(t, root, "internal/widget/widget_test.go", "package widget\n\nimport \"testing\"\n\nfunc TestNew(t *testing.T) {}\n")
	write(t, root, "docs/widget.md", "# Widget\nCovers the widget subsystem.\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	cmdOverview, ok := BuildSubsystemOverview(idx, "cmd/tool", 15)
	if !ok {
		t.Fatal("expected cmd/tool to match a directory")
	}
	if len(cmdOverview.EntryPoints) != 1 || cmdOverview.EntryPoints[0].RelPath != "cmd/tool/main.go" {
		t.Fatalf("cmd/tool entry points = %+v, want main.go via func main()", cmdOverview.EntryPoints)
	}

	widgetOverview, ok := BuildSubsystemOverview(idx, "internal/widget", 15)
	if !ok {
		t.Fatal("expected internal/widget to match a directory")
	}
	if len(widgetOverview.PublicSurface) != 3 {
		t.Fatalf("public surface = %+v, want Widget type + New + TestNew (helper is unexported)", widgetOverview.PublicSurface)
	}
	if len(widgetOverview.CoreTypes) != 1 || widgetOverview.CoreTypes[0].Name != "Widget" {
		t.Fatalf("core types = %+v, want just the Widget type", widgetOverview.CoreTypes)
	}
	if len(widgetOverview.Tests) != 1 || widgetOverview.Tests[0].RelPath != "internal/widget/widget_test.go" {
		t.Fatalf("tests = %+v, want widget_test.go", widgetOverview.Tests)
	}
	if len(widgetOverview.KeyDependencies) != 0 {
		t.Fatalf("widget package has no internal deps, got %+v", widgetOverview.KeyDependencies)
	}

	cmdDeps := 0
	for _, n := range cmdOverview.KeyDependencies {
		if n.RelPath == "internal/widget/widget.go" {
			cmdDeps++
		}
	}
	if cmdDeps != 1 {
		t.Fatalf("cmd/tool key dependencies = %+v, want internal/widget/widget.go", cmdOverview.KeyDependencies)
	}
}

func TestBuildSubsystemOverviewFallsBackToUncalledFilesAsEntryPoints(t *testing.T) {
	root := t.TempDir()
	write(t, root, "src/index.ts", "export function used() {}\n")
	write(t, root, "src/leaf.ts", "export function unused() {}\n")

	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	overview, ok := BuildSubsystemOverview(idx, "src", 15)
	if !ok {
		t.Fatal("expected src to match a directory")
	}
	if len(overview.EntryPoints) != 2 {
		t.Fatalf("entry points = %+v, want both files (neither is imported in-repo)", overview.EntryPoints)
	}
}

func TestBuildSubsystemOverviewNoMatchForNonDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package main\nfunc A() {}\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := BuildSubsystemOverview(idx, "a.go", 15); ok {
		t.Fatal("a file path should not match as a subsystem directory")
	}
	if _, ok := BuildSubsystemOverview(idx, "does/not/exist", 15); ok {
		t.Fatal("a nonexistent directory should not match")
	}
}

func TestFormatSubsystemOverviewSections(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/test\n")
	write(t, root, "internal/widget/widget.go", "package widget\n\ntype Widget struct{}\n")
	idx, err := Build(context.Background(), BuildOptions{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	overview, ok := BuildSubsystemOverview(idx, "internal/widget", 15)
	if !ok {
		t.Fatal("expected match")
	}
	out := FormatSubsystemOverview(overview, 15)
	for _, want := range []string{"subsystem", "entry points", "public surface", "core types", "tests", "key dependencies", "Widget"} {
		if !strings.Contains(out, want) {
			t.Fatalf("FormatSubsystemOverview output missing %q: %q", want, out)
		}
	}
}
