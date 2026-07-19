package skills

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleSkillBody is the canonical fixture used across install tests —
// a minimal but spec-complete SKILL.md whose `name` is `sample`. Tests
// derive variations from this by string-substituting the name.
const sampleSkillBody = `---
name: sample
description: Sample skill for installer tests.
---
# Sample

Body content.
`

// TestInstall_LocalDir installs from a local source dir that also
// carries scripts/ and references/ subtrees; verifies the spec dirs
// are copied through and that non-spec entries (LICENSE, README) are
// dropped. Catches the "installer mirrors arbitrary source trees"
// regression class.
func TestInstall_LocalDir(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "scripts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "references", "guide.md"), []byte("guide"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "LICENSE"), []byte("MIT"), 0o600); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	res, err := Install(InstallOptions{Source: src, DestRoot: dest})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Skill.Name != "sample" {
		t.Errorf("name = %q, want sample", res.Skill.Name)
	}
	if res.SourceType != SourceLocal {
		t.Errorf("source type = %q, want local", res.SourceType)
	}
	want := filepath.Join(dest, "sample")
	if res.Dir != want {
		t.Errorf("dir = %q, want %q", res.Dir, want)
	}
	if _, err := os.Stat(filepath.Join(want, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(want, "scripts", "run.sh")); err != nil {
		t.Errorf("scripts/run.sh missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(want, "references", "guide.md")); err != nil {
		t.Errorf("references/guide.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(want, "LICENSE")); err == nil {
		t.Errorf("LICENSE was copied; installer should only copy spec dirs")
	}
}

// TestInstall_LocalDir_RewritesDirName installs from a dir whose
// on-disk name doesn't match the frontmatter `name`. The installer
// should put the result under <DestRoot>/<frontmatter-name>, not the
// source dir's name — that's how a downloaded zip with a different
// folder name gets normalized to the spec-required "name == parent
// dir" invariant.
func TestInstall_LocalDir_RewritesDirName(t *testing.T) {
	src := filepath.Join(t.TempDir(), "weird-name")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	res, err := Install(InstallOptions{Source: src, DestRoot: dest})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if filepath.Base(res.Dir) != "sample" {
		t.Errorf("install dir = %q, want parent = sample", res.Dir)
	}
}

// TestInstall_LocalFile points at a SKILL.md directly (not its parent
// dir). Common ergonomic case: `yottacode skills install ./SKILL.md`
// from inside a working dir.
func TestInstall_LocalFile(t *testing.T) {
	src := t.TempDir()
	file := filepath.Join(src, "SKILL.md")
	if err := os.WriteFile(file, []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	res, err := Install(InstallOptions{Source: file, DestRoot: dest})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.Skill.Name != "sample" {
		t.Errorf("name = %q", res.Skill.Name)
	}
}

// TestInstall_RefusesOverwrite is the safety-net test for the
// "Refuse overwrite unless --force" rule. Two installs back-to-back —
// the second must fail unless Force is set.
func TestInstall_RefusesOverwrite(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := Install(InstallOptions{Source: src, DestRoot: dest}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := Install(InstallOptions{Source: src, DestRoot: dest}); err == nil {
		t.Fatal("second install should have refused without --force")
	}
	if _, err := Install(InstallOptions{Source: src, DestRoot: dest, Force: true}); err != nil {
		t.Fatalf("force install: %v", err)
	}
}

// TestInstall_InvalidSkillFile rejects a source whose SKILL.md
// doesn't parse. The dest dir must be untouched on failure — that's
// the atomic-staging contract.
func TestInstall_InvalidSkillFile(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("not a skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := Install(InstallOptions{Source: src, DestRoot: dest}); err == nil {
		t.Fatal("expected parse error on malformed SKILL.md")
	}
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			t.Errorf("dest should be empty on failure, found %q", e.Name())
		}
	}
}

// TestInstall_URL_SingleFile exercises the HTTPS code path against an
// httptest server. URL installs are file-only by design (we can't
// enumerate sibling dirs over arbitrary HTTPS) so no resource-dir
// assertions here.
func TestInstall_URL_SingleFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/SKILL.md") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(sampleSkillBody))
	}))
	defer srv.Close()

	dest := t.TempDir()
	res, err := Install(InstallOptions{
		Source:     srv.URL + "/sample/SKILL.md",
		DestRoot:   dest,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.SourceType != SourceURL {
		t.Errorf("source type = %q, want url", res.SourceType)
	}
	if _, err := os.Stat(filepath.Join(dest, "sample", "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
}

// TestInstall_URL_RejectsNonSkillMd is the guard for the literal
// reading of the design notes: URL installs accept exactly
// `https://.../SKILL.md`, nothing else.
func TestInstall_URL_RejectsNonSkillMd(t *testing.T) {
	if _, err := Install(InstallOptions{
		Source:   "https://example.com/skills/foo",
		DestRoot: t.TempDir(),
	}); err == nil {
		t.Fatal("expected URL install to reject non-SKILL.md path")
	}
}

// TestInstall_GitHub_Shorthand exercises the GitHub Contents API
// path against a fake server. Covers: SKILL.md fetch via download_url,
// scripts/ resource dir enumeration + per-file download, and the
// owner/repo/path → contents-API URL translation.
func TestInstall_GitHub_Shorthand(t *testing.T) {
	srv := newFakeCodeload(t, map[string]string{
		"skills/sample/SKILL.md":       sampleSkillBody,
		"skills/sample/scripts/run.sh": "#!/bin/sh\necho hi\n",
	})
	defer srv.Close()

	t.Setenv("YOTTACODE_GITHUB_CODELOAD_URL", srv.URL)
	dest := t.TempDir()
	res, err := Install(InstallOptions{
		Source:     "owner/repo/skills/sample",
		DestRoot:   dest,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.SourceType != SourceGitHub {
		t.Errorf("source type = %q, want github", res.SourceType)
	}
	if _, err := os.Stat(filepath.Join(dest, "sample", "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "sample", "scripts", "run.sh")); err != nil {
		t.Errorf("scripts/run.sh missing: %v", err)
	}
}

func newFakeCodeload(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create("repo-main/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/zip/refs/heads/main") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(buf.Bytes())
	}))
}

// TestInstall_OfficialShortcut expands official/<slug> to the public
// yottacode-skills GitHub path while recording the shortcut as official
// provenance in the lockfile.
func TestInstall_OfficialShortcut(t *testing.T) {
	srv := newFakeCodeload(t, map[string]string{
		"skills/sample/SKILL.md":       sampleSkillBody,
		"skills/sample/scripts/run.sh": "#!/bin/sh\necho hi\n",
	})
	defer srv.Close()

	t.Setenv("YOTTACODE_GITHUB_CODELOAD_URL", srv.URL)
	dest := t.TempDir()
	res, err := Install(InstallOptions{
		Source:     "official/sample",
		DestRoot:   dest,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.SourceType != SourceOfficial {
		t.Errorf("source type = %q, want official", res.SourceType)
	}
	if res.Lock.Source != "official/sample" {
		t.Errorf("lock source = %q, want official/sample", res.Lock.Source)
	}
	if res.Lock.SourceType != SourceOfficial {
		t.Errorf("lock source type = %q, want official", res.Lock.SourceType)
	}
	if _, err := os.Stat(filepath.Join(dest, "sample", "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "sample", "scripts", "run.sh")); err != nil {
		t.Errorf("official resource missing: %v", err)
	}
}

func TestInstall_SkillsSHURL(t *testing.T) {
	srv := newFakeCodeload(t, map[string]string{
		"skills/find-skills/SKILL.md":           strings.Replace(sampleSkillBody, "name: sample", "name: find-skills", 1),
		"skills/find-skills/assets/example.txt": "asset",
	})
	defer srv.Close()
	t.Setenv("YOTTACODE_GITHUB_CODELOAD_URL", srv.URL)

	dest := t.TempDir()
	res, err := Install(InstallOptions{
		Source:     "https://www.skills.sh/vercel-labs/skills/find-skills",
		DestRoot:   dest,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("install skills.sh URL: %v", err)
	}
	if res.SourceType != SourceGitHub {
		t.Errorf("source type = %q, want github", res.SourceType)
	}
	if res.Lock.Source != "https://www.skills.sh/vercel-labs/skills/find-skills" {
		t.Errorf("lock source = %q", res.Lock.Source)
	}
	if _, err := os.Stat(filepath.Join(dest, "find-skills", "assets", "example.txt")); err != nil {
		t.Errorf("asset missing: %v", err)
	}
}

func TestInstall_GitHubURLForms(t *testing.T) {
	srv := newFakeCodeload(t, map[string]string{
		"skills/sample/SKILL.md":            sampleSkillBody,
		"skills/sample/references/guide.md": "guide",
	})
	defer srv.Close()
	t.Setenv("YOTTACODE_GITHUB_CODELOAD_URL", srv.URL)

	cases := []string{
		"https://github.com/owner/repo/tree/main/skills/sample",
		"https://github.com/owner/repo/blob/main/skills/sample/SKILL.md",
		"https://raw.githubusercontent.com/owner/repo/refs/heads/main/skills/sample/SKILL.md",
		"https://raw.githubusercontent.com/owner/repo/main/skills/sample/SKILL.md",
	}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			dest := t.TempDir()
			if _, err := Install(InstallOptions{Source: src, DestRoot: dest, HTTPClient: srv.Client()}); err != nil {
				t.Fatalf("install: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dest, "sample", "references", "guide.md")); err != nil {
				t.Errorf("reference missing: %v", err)
			}
		})
	}
}

// TestInstall_GitHub_TrimsTrailingSkillMd tolerates the user pasting
// the path with a trailing /SKILL.md. Same install result as the
// directory form.
func TestInstall_GitHub_TrimsTrailingSkillMd(t *testing.T) {
	srv := newFakeCodeload(t, map[string]string{
		"skills/sample/SKILL.md": sampleSkillBody,
	})
	defer srv.Close()

	t.Setenv("YOTTACODE_GITHUB_CODELOAD_URL", srv.URL)
	dest := t.TempDir()
	if _, err := Install(InstallOptions{
		Source:     "owner/repo/skills/sample/SKILL.md",
		DestRoot:   dest,
		HTTPClient: srv.Client(),
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "sample", "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
}

// TestUninstall removes a freshly-installed skill and verifies the
// directory is gone. The error-when-missing case is also covered to
// keep the CLI's error message accurate.
func TestUninstall(t *testing.T) {
	dest := t.TempDir()
	t.Setenv("YOTTACODE_HOME", dest)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(sampleSkillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallOptions{Source: src}); err != nil {
		t.Fatalf("install: %v", err)
	}
	path, err := Uninstall("sample")
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("dir still exists at %s", path)
	}
	if _, err := Uninstall("sample"); err == nil {
		t.Error("second uninstall should report missing")
	}
}

// TestClassifySource locks the source-type heuristic so refactors
// don't accidentally change which shape resolves to which type.
func TestClassifySource(t *testing.T) {
	cases := []struct {
		in   string
		want SourceType
	}{
		{"https://example.com/SKILL.md", SourceURL},
		{"http://example.com/SKILL.md", SourceURL},
		{"./local/skill", SourceLocal},
		{"/abs/path/skill", SourceLocal},
		{"../up/skill", SourceLocal},
		{"owner/repo", SourceGitHub},
		{"owner/repo/skills/foo", SourceGitHub},
		{"not a source", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := classifySource(c.in); got != c.want {
				t.Errorf("classifySource(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// newFakeGitHub spins up a GitHub-Contents-API stand-in. dirListings
// keys are repo-relative paths (e.g. "skills/sample"); the server
// answers /repos/owner/repo/contents/<key> with the matching
// []ghContent. fileBodies keys are the same repo-relative paths; the
// server answers /raw/<key> with the body. Each ghContent entry has
// its DownloadURL backfilled to point at the matching /raw/ path.
func newFakeGitHub(t *testing.T, dirListings map[string]any, fileBodies map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		prefix := "/repos/"
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		parts := strings.SplitN(rest, "/contents/", 2)
		if len(parts) != 2 || strings.Count(parts[0], "/") != 1 {
			http.NotFound(w, r)
			return
		}
		key := strings.Trim(parts[1], "/")
		listing, ok := dirListings[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		items := listing.([]ghContent)
		for i := range items {
			if items[i].Type == "file" {
				items[i].DownloadURL = server.URL + "/raw/" + items[i].Path
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	})
	mux.HandleFunc("/raw/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/raw/")
		body, ok := fileBodies[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	server = httptest.NewServer(mux)
	return server
}
