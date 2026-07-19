package skills

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
	base := githubAPIBase() + "/repos/" + OfficialSkillsOwner + "/" + OfficialSkillsRepo + "/contents/skills"
	items, err := ghList(client, base)
	if err != nil {
		return nil, fmt.Errorf("list official skills: %w", err)
	}
	var out []Skill
	for _, item := range items {
		if item.Type != "dir" {
			continue
		}
		children, err := ghList(client, base+"/"+item.Name)
		if err != nil {
			return nil, fmt.Errorf("list official skill %s: %w", item.Name, err)
		}
		var skillFile *ghContent
		for i := range children {
			if children[i].Type == "file" && children[i].Name == "SKILL.md" {
				skillFile = &children[i]
				break
			}
		}
		if skillFile == nil || skillFile.DownloadURL == "" {
			continue
		}
		body, err := httpGet(client, skillFile.DownloadURL, "application/octet-stream")
		if err != nil {
			return nil, fmt.Errorf("fetch official skill %s: %w", item.Name, err)
		}
		sk, err := ParseSkillFile(body, item.Name)
		if err != nil {
			return nil, fmt.Errorf("parse official skill %s: %w", item.Name, err)
		}
		sk.Source = ScopeOfficial
		sk.SourcePath, _ = OfficialSource(sk.Name)
		sk.Body = ""
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
