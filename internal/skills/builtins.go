package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

//go:embed all:builtins
var builtinFiles embed.FS

// LoadBuiltins parses every embedded SKILL.md under builtins/<slug>/
// and returns the resulting Skill set. Source is always ScopeBuiltin
// so diagnostics can distinguish embedded vs disk-loaded definitions.
// Parse errors would be ship-blocking (the embedded files are part of
// the binary), so we panic — the test suite catches bad frontmatter
// before release.
//
// SourcePath is set to "embed:builtins/<slug>/SKILL.md" so the path
// renders meaningfully in /help even though it does not exist on
// disk. Dir is left empty for built-ins; resources are read via the
// embedded FS through BuiltinResource, not the disk path.
func LoadBuiltins() []Skill {
	entries, err := fs.ReadDir(builtinFiles, "builtins")
	if err != nil {
		// No builtins embedded — fine, the binary simply ships
		// without a default pack. Don't panic.
		return nil
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		skillPath := path.Join("builtins", slug, "SKILL.md")
		data, err := fs.ReadFile(builtinFiles, skillPath)
		if err != nil {
			panic(fmt.Sprintf("skills: read builtin %q: %v", skillPath, err))
		}
		sk, err := ParseSkillFile(data, slug)
		if err != nil {
			panic(fmt.Sprintf("skills: parse builtin %q: %v", skillPath, err))
		}
		sk.Source = ScopeBuiltin
		sk.SourcePath = "embed:" + skillPath
		out = append(out, sk)
	}
	return out
}

// BuiltinResource reads a resource file (script, reference, asset)
// from inside a built-in skill's embedded directory. relPath is the
// resource's path relative to the skill's root (e.g.
// "references/playbook.md" or "scripts/check.sh"). Returns the bytes
// or an error if the resource isn't embedded. Disk-loaded skills do
// not flow through this path — their resources live on disk and are
// read by the model through read_file the normal way.
func BuiltinResource(slug, relPath string) ([]byte, error) {
	if strings.Contains(relPath, "..") {
		return nil, fmt.Errorf("relative path %q escapes the skill directory", relPath)
	}
	full := path.Join("builtins", slug, relPath)
	return fs.ReadFile(builtinFiles, full)
}
