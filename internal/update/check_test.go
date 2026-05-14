package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.2.0", 0},
		{"0.2.1", "0.2.0", 1},
		{"0.2.0", "0.2.1", -1},
		{"0.3.0", "0.2.9", 1},
		{"1.0.0", "0.99.99", 1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.1", "1.0.0-rc.2", -1},
		{"1.0.0-dev", "1.0.0-rc.1", -1},
		{"v0.2.0", "0.2.0", 0},
	}
	for _, tc := range cases {
		a := tc.a
		if len(a) > 0 && a[0] == 'v' {
			a = a[1:]
		}
		b := tc.b
		if len(b) > 0 && b[0] == 'v' {
			b = b[1:]
		}
		got, err := compareSemver(a, b)
		if err != nil {
			t.Fatalf("compareSemver(%q, %q) error: %v", a, b, err)
		}
		if got != tc.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", a, b, got, tc.want)
		}
	}
}

func TestCompareSemver_Garbage(t *testing.T) {
	for _, in := range []string{"", "1", "1.2", "1.2.x", "foo"} {
		if _, err := compareSemver(in, "1.0.0"); err == nil {
			t.Errorf("compareSemver(%q, ...) expected error", in)
		}
	}
}

func TestParseReleaseJSON(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "release.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var rel rawRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rel.TagName != "v0.3.0" {
		t.Errorf("TagName = %q, want v0.3.0", rel.TagName)
	}
	if rel.HTMLURL != "https://github.com/yottadynamics/yottacode/releases/tag/v0.3.0" {
		t.Errorf("HTMLURL = %q", rel.HTMLURL)
	}
}

func TestCheckBackground_CacheHit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withFetcher(t, func(ctx context.Context) (rawRelease, error) {
		t.Fatalf("fetcher should not be called on cache hit")
		return rawRelease{}, nil
	})
	if err := writeCache(cacheRecord{
		LastChecked:   time.Now(),
		LatestVersion: "99.0.0",
		ReleaseURL:    "https://example.com/v99",
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	r, ok := waitResult(t, CheckBackground(context.Background()))
	if !ok {
		t.Fatalf("expected a Result, got closed channel")
	}
	if r.Latest != "99.0.0" || !r.NewVersion {
		t.Errorf("unexpected result: %+v", r)
	}
}

func TestCheckBackground_FetchAndCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	withFetcher(t, func(ctx context.Context) (rawRelease, error) {
		return rawRelease{
			TagName: "v0.3.0",
			HTMLURL: "https://github.com/yottadynamics/yottacode/releases/tag/v0.3.0",
		}, nil
	})
	r, ok := waitResult(t, CheckBackground(context.Background()))
	if !ok {
		t.Fatalf("expected a Result")
	}
	if r.Latest != "0.3.0" {
		t.Errorf("Latest = %q, want 0.3.0", r.Latest)
	}
	rec, fresh := readCache(time.Now())
	if !fresh || rec.LatestVersion != "0.3.0" {
		t.Errorf("cache not written: fresh=%v rec=%+v", fresh, rec)
	}
}

func TestCheckBackground_FetchError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	withFetcher(t, func(ctx context.Context) (rawRelease, error) {
		return rawRelease{}, errors.New("boom")
	})
	_, ok := waitResult(t, CheckBackground(context.Background()))
	if ok {
		t.Fatalf("expected closed channel on fetch error")
	}
}

func withFetcher(t *testing.T, f fetcher) {
	t.Helper()
	prev := defaultFetcher
	defaultFetcher = f
	t.Cleanup(func() { defaultFetcher = prev })
}

func waitResult(t *testing.T, ch <-chan Result) (Result, bool) {
	t.Helper()
	select {
	case r, ok := <-ch:
		return r, ok
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for Result")
		return Result{}, false
	}
}
