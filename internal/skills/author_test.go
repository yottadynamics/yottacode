package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewSkill_Scaffolds verifies the scaffold writes a parseable
// SKILL.md at <destRoot>/<slug>/SKILL.md. The generated frontmatter
// must round-trip through ParseSkillFile — otherwise the starter
// would land authors at "your scaffold is broken" out of the gate.
func TestNewSkill_Scaffolds(t *testing.T) {
	dest := t.TempDir()
	res, err := NewSkill(NewSkillOptions{Slug: "my-skill", DestRoot: dest})
	if err != nil {
		t.Fatalf("scaffold: %v", err)
	}
	want := filepath.Join(dest, "my-skill", "SKILL.md")
	if res.SkillFile != want {
		t.Errorf("SkillFile = %q, want %q", res.SkillFile, want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	sk, err := ParseSkillFile(data, "my-skill")
	if err != nil {
		t.Fatalf("scaffold did not parse: %v", err)
	}
	if sk.Name != "my-skill" {
		t.Errorf("scaffold name = %q", sk.Name)
	}
	if !strings.Contains(sk.Body, "TODO") {
		t.Error("scaffold body should carry TODO markers for the author to fill in")
	}
}

// TestNewSkill_RefusesOverwrite locks the --force gate. Without it,
// `skills new foo` on an existing dir would clobber the author's
// work-in-progress.
func TestNewSkill_RefusesOverwrite(t *testing.T) {
	dest := t.TempDir()
	if _, err := NewSkill(NewSkillOptions{Slug: "alpha", DestRoot: dest}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSkill(NewSkillOptions{Slug: "alpha", DestRoot: dest}); err == nil {
		t.Error("second scaffold without --force should refuse")
	}
	if _, err := NewSkill(NewSkillOptions{Slug: "alpha", DestRoot: dest, Force: true}); err != nil {
		t.Errorf("--force scaffold: %v", err)
	}
}

// TestNewSkill_InvalidSlug surfaces the spec's slug rules at the
// authoring step so a typo is caught immediately instead of at
// install/load time. Mirrors the install-path validation.
func TestNewSkill_InvalidSlug(t *testing.T) {
	cases := []string{"UPPER", "with space", "double--hyphen", ""}
	for _, slug := range cases {
		if _, err := NewSkill(NewSkillOptions{Slug: slug, DestRoot: t.TempDir()}); err == nil {
			t.Errorf("scaffold accepted invalid slug %q", slug)
		}
	}
}

// TestValidate_HappyPath confirms Validate's success path returns
// the parsed Skill with no error, so CI scripts can pipe `validate`
// output into something like `&& publish`.
func TestValidate_HappyPath(t *testing.T) {
	dir := t.TempDir()
	body := "---\nname: foo\ndescription: x\n---\nbody\n"
	if err := os.MkdirAll(filepath.Join(dir, "foo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foo", "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	res := Validate(filepath.Join(dir, "foo"))
	if res.Err != nil {
		t.Fatalf("validate: %v", res.Err)
	}
	if res.Skill.Name != "foo" {
		t.Errorf("name = %q", res.Skill.Name)
	}
}

// TestValidate_DirMismatchErrors is the regression test for the
// spec's "name must match parent dir" rule on dir-form input. File-
// form input skips the check (covered by TestValidate_FileFormSkipsDirMatch).
func TestValidate_DirMismatchErrors(t *testing.T) {
	dir := t.TempDir()
	body := "---\nname: actual\ndescription: x\n---\nbody\n"
	if err := os.MkdirAll(filepath.Join(dir, "wrong"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wrong", "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := Validate(filepath.Join(dir, "wrong")); res.Err == nil {
		t.Error("validate of dir with name/dir mismatch should error")
	}
}

// TestValidate_FileFormSkipsDirMatch is the CI / authoring-use-case
// guarantee: pointing at a bare SKILL.md doesn't enforce the
// directory-name rule, so a contributor can lint a single file
// before deciding the final slug.
func TestValidate_FileFormSkipsDirMatch(t *testing.T) {
	dir := t.TempDir()
	body := "---\nname: foo\ndescription: x\n---\nbody\n"
	skillFile := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := Validate(skillFile); res.Err != nil {
		t.Errorf("validate of bare SKILL.md should succeed: %v", res.Err)
	}
}
