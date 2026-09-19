package permissions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateReportsFilesIndependently(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".yottacode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "permissions.json"), []byte(`{"permissions":{"allow":["Bash(go *)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "permissions.local.json"), []byte(`{"permissions":`), 0o644); err != nil {
		t.Fatal(err)
	}

	report := Validate(cwd)
	if report.Status != "issue" {
		t.Fatalf("status = %q, want issue", report.Status)
	}
	if len(report.Files) != 3 {
		t.Fatalf("files = %d, want 3", len(report.Files))
	}
	if report.Files[0].Status != "missing" || report.Files[1].Status != "ok" || report.Files[2].Status != "issue" {
		t.Fatalf("files = %+v", report.Files)
	}
}

func TestValidateMissingAndEmptyAreNotIssues(t *testing.T) {
	cwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, ".yottacode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".yottacode", "permissions.local.json"), []byte(" \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report := Validate(cwd)
	if report.Status != "ok" || len(report.Issues) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.Files[0].Status != "missing" || report.Files[1].Status != "missing" || report.Files[2].Status != "empty" {
		t.Fatalf("files = %+v", report.Files)
	}
}

func TestValidateReportsRuleSyntaxAndWarnings(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, ".yottacode", "permissions.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"permissions":{"allow":["NotATool(*)"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	report := Validate(cwd)
	if report.Status != "warning" || len(report.Warnings) == 0 {
		t.Fatalf("report = %+v", report)
	}
}
