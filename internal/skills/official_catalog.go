package skills

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OfficialCatalogOptions configures official catalog reads. HTTPClient is used
// only by RefreshOfficialCatalog; ListOfficialCatalog is intentionally offline.
type OfficialCatalogOptions struct {
	HTTPClient *http.Client
}

// OfficialCatalogCacheName is the metadata cache file under UserSkillsDir(). It
// stores only safe browse metadata; installing still fetches real skill bytes
// from the yottacode-skills repository.
const OfficialCatalogCacheName = "catalog.json"

// OfficialCatalogCache is the on-disk metadata cache for the Official tab.
type OfficialCatalogCache struct {
	Version   int       `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	Skills    []Skill   `json:"skills"`
}

// ListOfficialCatalog returns the cached official metadata catalog, falling
// back to bundled metadata when no cache exists. It never touches the network,
// so opening /skills -> Catalog is instant and immune to GitHub rate limits.
func ListOfficialCatalog(opts OfficialCatalogOptions) ([]Skill, error) {
	_ = opts
	if rows, err := loadOfficialCatalogCache(); err == nil && len(rows) > 0 {
		return rows, nil
	}
	return embeddedOfficialCatalog(), nil
}

// RefreshOfficialCatalog fetches the public yottacode-skills catalog metadata
// from GitHub and writes ~/.yottacode/skills/catalog.json. It is an explicit
// user action; normal Catalog browsing remains offline.
func RefreshOfficialCatalog(opts OfficialCatalogOptions) ([]Skill, error) {
	rows, err := fetchOfficialCatalog(opts)
	if err != nil {
		return nil, err
	}
	if err := saveOfficialCatalogCache(rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// embeddedOfficialCatalog derives metadata from bundled fallback skills. Bodies
// are stripped so callers do not accidentally treat the embedded copy as the
// install source for the Official tab.
func embeddedOfficialCatalog() []Skill {
	out := LoadBuiltins()
	for i := range out {
		out[i].Source = ScopeOfficial
		out[i].SourcePath, _ = OfficialSource(out[i].Name)
		out[i].Body = ""
		out[i].Dir = ""
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func officialCatalogCachePath() (string, error) {
	dir, err := UserSkillsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, OfficialCatalogCacheName), nil
}

func loadOfficialCatalogCache() ([]Skill, error) {
	path, err := officialCatalogCachePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cache OfficialCatalogCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("parse official catalog cache: %w", err)
	}
	for i := range cache.Skills {
		cache.Skills[i].Source = ScopeOfficial
		cache.Skills[i].SourcePath, _ = OfficialSource(cache.Skills[i].Name)
		cache.Skills[i].Body = ""
		cache.Skills[i].Dir = ""
	}
	sort.Slice(cache.Skills, func(i, j int) bool { return cache.Skills[i].Name < cache.Skills[j].Name })
	return cache.Skills, nil
}

func saveOfficialCatalogCache(rows []Skill) error {
	path, err := officialCatalogCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	cacheRows := make([]Skill, len(rows))
	copy(cacheRows, rows)
	for i := range cacheRows {
		cacheRows[i].Body = ""
		cacheRows[i].Dir = ""
	}
	cache := OfficialCatalogCache{
		Version:   1,
		UpdatedAt: time.Now().UTC(),
		Skills:    cacheRows,
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func fetchOfficialCatalog(opts OfficialCatalogOptions) ([]Skill, error) {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// Use the repository archive instead of the Contents API. Refreshing the
	// catalog used to make one API request per directory and skill file, which
	// exhausted GitHub's unauthenticated rate limit quickly for a public repo.
	archiveURL := githubCodeloadURL(OfficialSkillsOwner, OfficialSkillsRepo, "heads", "main")
	body, err := httpGet(client, archiveURL, "application/zip")
	if err != nil {
		return nil, fmt.Errorf("download official skills archive: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("read official skills archive: %w", err)
	}

	var out []Skill
	for _, file := range zr.File {
		name, ok := officialSkillArchiveName(file.Name)
		if !ok || file.FileInfo().IsDir() {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open official skill %s: %w", name, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, 5*1024*1024))
		closeErr := rc.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read official skill %s: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close official skill %s: %w", name, closeErr)
		}
		sk, err := ParseSkillFile(data, name)
		if err != nil {
			return nil, fmt.Errorf("parse official skill %s: %w", name, err)
		}
		sk.Source = ScopeOfficial
		sk.SourcePath, _ = OfficialSource(sk.Name)
		sk.Body = ""
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// officialSkillArchiveName extracts a top-level skill name from a GitHub zip
// entry such as repo-main/skills/seo-audit/SKILL.md. Other files are ignored so
// the catalog remains one metadata row per skill directory.
func officialSkillArchiveName(name string) (string, bool) {
	name = strings.TrimPrefix(path.Clean(name), "./")
	marker := "/skills/"
	idx := strings.Index(name, marker)
	if idx < 0 || !strings.HasSuffix(name, "/SKILL.md") {
		return "", false
	}
	rel := strings.TrimPrefix(name[idx+len(marker):], "/")
	parts := strings.Split(rel, "/")
	if len(parts) != 2 || parts[1] != "SKILL.md" || parts[0] == "" {
		return "", false
	}
	return parts[0], true
}
