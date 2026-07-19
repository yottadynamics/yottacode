package skills

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOfficialSource validates the public catalog shortcut. The helper is
// intentionally tiny, but it is the contract shared by CLI, TUI, and lockfile
// update paths, so malformed slugs must fail before any network request.
func TestOfficialSource(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "plain slug",
			in:   "test-driven-development",
			want: "yottadynamics/yottacode-skills/skills/test-driven-development",
		},
		{
			name: "official prefix",
			in:   "official/test-driven-development",
			want: "yottadynamics/yottacode-skills/skills/test-driven-development",
		},
		{name: "bad slug", in: "Bad/Slug", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OfficialSource(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("OfficialSource: %v", err)
			}
			if got != tc.want {
				t.Errorf("OfficialSource(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeOfficialSource confirms non-official installs pass through
// untouched while official/<slug> expands to the GitHub source path.
func TestNormalizeOfficialSource(t *testing.T) {
	got, official, err := NormalizeOfficialSource("official/diagnose")
	if err != nil {
		t.Fatalf("NormalizeOfficialSource: %v", err)
	}
	if !official {
		t.Fatal("official shortcut was not detected")
	}
	if got != "yottadynamics/yottacode-skills/skills/diagnose" {
		t.Errorf("expanded source = %q", got)
	}

	got, official, err = NormalizeOfficialSource("owner/repo/skills/foo")
	if err != nil {
		t.Fatalf("NormalizeOfficialSource passthrough: %v", err)
	}
	if official {
		t.Fatal("non-official source should not be marked official")
	}
	if got != "owner/repo/skills/foo" {
		t.Errorf("passthrough source = %q", got)
	}
}

func TestOfficialCatalogListIsOfflineWithCacheFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	rows, err := ListOfficialCatalog(OfficialCatalogOptions{})
	if err != nil {
		t.Fatalf("ListOfficialCatalog: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("embedded catalog should provide offline rows")
	}

	cached := []Skill{{Name: "cached-skill", Description: "from local cache", Source: ScopeOfficial}}
	if err := saveOfficialCatalogCache(cached); err != nil {
		t.Fatalf("save cache: %v", err)
	}
	rows, err = ListOfficialCatalog(OfficialCatalogOptions{HTTPClient: http.DefaultClient})
	if err != nil {
		t.Fatalf("ListOfficialCatalog cached: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "cached-skill" {
		t.Fatalf("rows = %#v, want cached-skill only", rows)
	}
	if rows[0].Body != "" || rows[0].Source != ScopeOfficial || !strings.Contains(rows[0].SourcePath, "cached-skill") {
		t.Fatalf("cached row not normalized: %#v", rows[0])
	}
}

func TestRefreshOfficialCatalogWritesCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("YOTTACODE_HOME", home)
	t.Setenv("HOME", home)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/yottadynamics/yottacode-skills/contents/skills":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "remote-skill", "type": "dir"}})
		case "/repos/yottadynamics/yottacode-skills/contents/skills/remote-skill":
			_ = json.NewEncoder(w).Encode([]map[string]string{{
				"name":         "SKILL.md",
				"type":         "file",
				"download_url": srv.URL + "/raw/remote-skill/SKILL.md",
			}})
		case "/raw/remote-skill/SKILL.md":
			_, _ = w.Write([]byte("---\nname: remote-skill\ndescription: Remote catalog skill.\n---\nBody\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("YOTTACODE_GITHUB_API_URL", srv.URL)

	rows, err := RefreshOfficialCatalog(OfficialCatalogOptions{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("RefreshOfficialCatalog: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "remote-skill" || rows[0].Body != "" {
		t.Fatalf("rows = %#v", rows)
	}

	data, err := os.ReadFile(filepath.Join(home, "skills", OfficialCatalogCacheName))
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if !strings.Contains(string(data), "remote-skill") || strings.Contains(string(data), "Body\": \"Body") {
		t.Fatalf("cache should contain metadata only: %s", data)
	}
}
