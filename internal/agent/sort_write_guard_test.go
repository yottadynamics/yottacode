package agent

import "testing"

// TestSortAttachedOutputFlagNotAutoSafe is a regression for the release
// review's sort -o bypass: the write-detection regex used to require
// whitespace before the flag (`\s(-o\b|--output\b)`), so GNU sort's
// attached-argument form `sort -ofile` evaded it and IsAutoModeSafeBash
// auto-approved a file-clobbering write with no modal. Every form that
// writes a file must NOT auto-approve.
func TestSortAttachedOutputFlagNotAutoSafe(t *testing.T) {
	cwd := NewCwdRef(t.TempDir())
	cases := []struct {
		name    string
		command string
	}{
		{"spaced-o", "sort -o /tmp/x data"},          // already caught
		{"attached-o", "sort -ofile data"},           // the bypass
		{"attached-o-abspath", "sort -o/tmp/x data"}, // boundary check
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := `{"command":"` + tc.command + `"}`
			if IsAutoModeSafeBash(args, cwd) {
				t.Errorf("IsAutoModeSafeBash(%q) = true; a sort that writes a file must NOT auto-approve", tc.command)
			}
		})
	}
}
