package permissions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PolicyFileReport describes one project permission policy source without
// changing the active permission store. Missing files are valid and reported
// separately so doctor can distinguish an absent policy from a broken one.
type PolicyFileReport struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Status      string   `json:"status"`
	Diagnostics []string `json:"diagnostics,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// ValidationReport is the read-only result of validating all project policy
// sources. Issues block startup-quality confidence; warnings are advisory.
type ValidationReport struct {
	Status   string             `json:"status"`
	Files    []PolicyFileReport `json:"files"`
	Issues   []string           `json:"issues,omitempty"`
	Warnings []string           `json:"warnings,omitempty"`
}

// Validate checks both project permission files independently. It never writes
// files or changes the policy used by the running agent.
func Validate(cwd string) ValidationReport {
	return ValidateWithSystemPath(cwd, "/etc/yottacode/permissions.json")
}

// ValidateWithSystemPath validates system, shared, and local policy files.
// An empty systemPath omits the optional system source for isolated callers.
func ValidateWithSystemPath(cwd, systemPath string) ValidationReport {
	if cwd == "" {
		return ValidationReport{Status: "issue", Issues: []string{"permissions: cwd is required"}}
	}
	dir := filepath.Join(cwd, ".yottacode")
	files := make([]struct{ name, path string }, 0, 3)
	if systemPath != "" {
		files = append(files, struct{ name, path string }{"system", systemPath})
	}
	files = append(files,
		struct{ name, path string }{"shared", filepath.Join(dir, "permissions.json")},
		struct{ name, path string }{"local", filepath.Join(dir, "permissions.local.json")},
	)
	report := ValidationReport{Files: make([]PolicyFileReport, 0, len(files))}
	// Load all valid files into one merged policy so cross-file shadowing and
	// precedence warnings match the runtime evaluator.
	merged := LoadEmpty(cwd)
	for _, file := range files {
		if result := validateFile(cwd, file.name, file.path); result.Status == "issue" {
			report.Files = append(report.Files, result)
			report.Issues = append(report.Issues, result.Diagnostics...)
		} else {
			report.Files = append(report.Files, result)
			if file.path != "" && result.Status != "missing" && result.Status != "empty" {
				_ = merged.loadFile(file.path, file.path)
			}
		}
	}
	report.Warnings = merged.LintWarnings()
	switch {
	case len(report.Issues) > 0:
		report.Status = "issue"
	case len(report.Warnings) > 0:
		report.Status = "warning"
	default:
		report.Status = "ok"
	}
	return report
}

func validateFile(cwd, name, path string) PolicyFileReport {
	result := PolicyFileReport{Name: name, Path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		result.Status = "missing"
		return result
	}
	if err != nil {
		result.Status = "issue"
		result.Diagnostics = []string{fmt.Sprintf("%s: read %s: %v", name, path, err)}
		return result
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		result.Status = "empty"
		return result
	}

	p := LoadEmpty(cwd)
	if err := p.loadFile(path, path); err != nil {
		result.Status = "issue"
		result.Diagnostics = []string{err.Error()}
		return result
	}
	result.Warnings = p.LintWarnings()
	if len(result.Warnings) > 0 {
		result.Status = "warning"
	} else {
		result.Status = "ok"
	}
	return result
}
