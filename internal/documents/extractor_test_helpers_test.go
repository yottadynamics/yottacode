package documents

import "strings"

// containsWarning reports whether any warning contains needle. Tests use a
// substring check because production warnings include contextual page/slide
// numbers around the stable message text.
func containsWarning(warnings []string, needle string) bool {
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}

// hasSectionLabel reports whether extraction returned a section with the exact
// label. Keeping the helper in tests avoids weakening production APIs just for
// assertions.
func hasSectionLabel(sections []DocumentSection, label string) bool {
	for _, sec := range sections {
		if sec.Label == label {
			return true
		}
	}
	return false
}
