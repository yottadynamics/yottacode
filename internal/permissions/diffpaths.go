package permissions

import (
	"fmt"
	"strconv"
	"strings"
)

// NormalizeDiffPath decodes one Git diff path token and strips the conventional
// a/ or b/ prefix. It rejects malformed quoted paths so validation and git apply
// cannot disagree about the target.
func NormalizeDiffPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, '\t'); i >= 0 {
		raw = raw[:i]
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		decoded, err := strconv.Unquote(raw)
		if err != nil {
			return "", fmt.Errorf("malformed quoted diff path %q: %w", raw, err)
		}
		raw = decoded
	}
	if strings.HasPrefix(raw, "a/") || strings.HasPrefix(raw, "b/") {
		raw = raw[2:]
	}
	return raw, nil
}

// would touch. Invalid Git quoting returns no paths (the strict variant gives
// callers the diagnostic); callers must fail closed before applying a patch.
func ParseDiffPaths(diff string) []string {
	paths, err := ParseDiffPathsStrict(diff)
	if err != nil {
		return nil
	}
	return paths
}

// ParseDiffPathsStrict decodes Git-quoted paths and rejects ambiguous or
// malformed encodings rather than validating a different path than git apply.
func ParseDiffPathsStrict(diff string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(p string) error {
		p = strings.TrimSpace(p)
		if i := strings.IndexByte(p, '\t'); i >= 0 {
			p = p[:i]
		}
		if p == "" || p == "/dev/null" {
			return nil
		}
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			decoded, err := strconv.Unquote(p)
			if err != nil {
				return fmt.Errorf("malformed quoted diff path %q: %w", p, err)
			}
			p = decoded
		}
		if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
			p = p[2:]
		}
		if p == "" || seen[p] {
			return nil
		}
		seen[p] = true
		out = append(out, p)
		return nil
	}
	parsePair := func(s string) error {
		s = strings.TrimSpace(s)
		for i := 0; i < 2; i++ {
			if s == "" {
				return fmt.Errorf("diff --git header has fewer than two paths")
			}
			end := 0
			if s[0] == '"' {
				for end = 1; end < len(s); end++ {
					if s[end] == '"' && s[end-1] != '\\' {
						end++
						break
					}
				}
				if end > len(s) || end == 1 {
					return fmt.Errorf("malformed diff --git path")
				}
			} else if j := strings.IndexByte(s, ' '); j >= 0 {
				end = j
			} else {
				end = len(s)
			}
			if err := add(s[:end]); err != nil {
				return err
			}
			s = strings.TrimSpace(s[end:])
		}
		return nil
	}
	for _, line := range strings.Split(diff, "\n") {
		var err error
		switch {
		case strings.HasPrefix(line, "diff --git "):
			err = parsePair(strings.TrimPrefix(line, "diff --git "))
		case strings.HasPrefix(line, "+++ "):
			err = add(line[4:])
		case strings.HasPrefix(line, "--- "):
			err = add(line[4:])
		case strings.HasPrefix(line, "rename to "):
			err = add(line[len("rename to "):])
		case strings.HasPrefix(line, "rename from "):
			err = add(line[len("rename from "):])
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
