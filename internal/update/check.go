// Package update polls GitHub once a day for newer yottacode releases
// and caches the answer under ~/.yottacode/cache/update-check.json.
// Callers spin up CheckBackground at startup and react to the at-most-
// one Result that arrives — failures are silent so the update path
// never blocks or annoys.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yottadynamics/yottacode/internal/version"
)

const releasesLatestURL = "https://api.github.com/repos/yottadynamics/yottacode/releases/latest"

// Result is what CheckBackground delivers when a check succeeds.
type Result struct {
	Current    string // e.g. "0.2.0"
	Latest     string // e.g. "0.3.0", no leading "v"
	URL        string // GitHub release HTML URL
	NewVersion bool   // Latest > Current per semver
}

type rawRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

type fetcher func(ctx context.Context) (rawRelease, error)

var defaultFetcher fetcher = fetchFromGitHub

// CheckBackground starts an async check. The returned channel receives
// exactly one Result on success, then closes. On any failure (no HOME,
// network error, parse error, ...) the channel closes with no value.
// Silent failure is intentional: an update check must never block
// startup or annoy.
func CheckBackground(ctx context.Context) <-chan Result {
	ch := make(chan Result, 1)
	go func() {
		defer close(ch)
		now := time.Now()
		if rec, fresh := readCache(now); fresh {
			ch <- newResult(rec.LatestVersion, rec.ReleaseURL)
			return
		}
		rel, err := defaultFetcher(ctx)
		if err != nil {
			return
		}
		latest := strings.TrimPrefix(rel.TagName, "v")
		if latest == "" {
			return
		}
		_ = writeCache(cacheRecord{
			LastChecked:   now,
			LatestVersion: latest,
			ReleaseURL:    rel.HTMLURL,
		})
		ch <- newResult(latest, rel.HTMLURL)
	}()
	return ch
}

func newResult(latest, url string) Result {
	cmp, err := compareSemver(latest, version.Current)
	newer := err == nil && cmp > 0
	return Result{
		Current:    version.Current,
		Latest:     latest,
		URL:        url,
		NewVersion: newer,
	}
}

func fetchFromGitHub(ctx context.Context) (rawRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesLatestURL, nil)
	if err != nil {
		return rawRelease{}, err
	}
	req.Header.Set("User-Agent", "yottacode/"+version.Current)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return rawRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rawRelease{}, fmt.Errorf("github: status %d", resp.StatusCode)
	}
	var rel rawRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return rawRelease{}, err
	}
	return rel, nil
}

// compareSemver returns -1 / 0 / +1 for a < b / a == b / a > b. Accepts
// MAJOR.MINOR.PATCH with optional `-pre` suffix. A version with a
// pre-release suffix is treated as less than the same numeric version
// without one (semver §11, simplified — pre-release segments are
// compared lexically rather than dot-segmented).
func compareSemver(a, b string) (int, error) {
	aNums, aPre, err := splitSemver(a)
	if err != nil {
		return 0, err
	}
	bNums, bPre, err := splitSemver(b)
	if err != nil {
		return 0, err
	}
	for i := range 3 {
		if aNums[i] != bNums[i] {
			if aNums[i] < bNums[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case aPre == "" && bPre == "":
		return 0, nil
	case aPre == "" && bPre != "":
		return 1, nil
	case aPre != "" && bPre == "":
		return -1, nil
	case aPre < bPre:
		return -1, nil
	case aPre > bPre:
		return 1, nil
	}
	return 0, nil
}

func splitSemver(s string) ([3]int, string, error) {
	var nums [3]int
	s = strings.TrimSpace(s)
	if s == "" {
		return nums, "", errors.New("empty version")
	}
	pre := ""
	if dash := strings.IndexByte(s, '-'); dash >= 0 {
		pre = s[dash+1:]
		s = s[:dash]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return nums, "", fmt.Errorf("invalid semver: %q", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, "", fmt.Errorf("invalid semver component: %q", p)
		}
		nums[i] = n
	}
	return nums, pre, nil
}
