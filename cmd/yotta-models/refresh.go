package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/yottadynamics/yottacode/internal/catalog"
)

// refresh fetches each provider whose key is in the environment and
// rewrites the catalog file at outPath. Providers without keys are
// skipped — their existing entries (if any) are preserved.
//
// The output is sorted by (provider, ID) so diffs across runs are
// minimal: only model adds/removes/metadata changes show up, not
// reordering noise.
func refresh(outPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	existing := loadExisting(outPath)

	type fetchResult struct {
		provider string
		models   []catalog.Model
		err      error
	}
	var results []fetchResult

	if k := os.Getenv("ANTHROPIC_API_KEY"); k != "" {
		ms, err := fetchAnthropic(ctx, k)
		results = append(results, fetchResult{"anthropic", ms, err})
	} else {
		fmt.Fprintln(os.Stderr, "skip anthropic: ANTHROPIC_API_KEY not set")
	}
	if k := os.Getenv("OPENAI_API_KEY"); k != "" {
		ms, err := fetchOpenAI(ctx, k)
		results = append(results, fetchResult{"openai", ms, err})
	} else {
		fmt.Fprintln(os.Stderr, "skip openai: OPENAI_API_KEY not set")
	}
	if k := os.Getenv("GEMINI_API_KEY"); k != "" {
		ms, err := fetchGemini(ctx, k)
		results = append(results, fetchResult{"gemini", ms, err})
	} else {
		fmt.Fprintln(os.Stderr, "skip gemini: GEMINI_API_KEY not set")
	}

	merged := make(map[string][]catalog.Model)
	for _, m := range existing.Models {
		merged[m.Provider] = append(merged[m.Provider], m)
	}
	hardErr := false
	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "fetch %s failed: %v (keeping existing entries)\n", r.provider, r.err)
			hardErr = true
			continue
		}
		fmt.Fprintf(os.Stderr, "fetched %s: %d models\n", r.provider, len(r.models))
		merged[r.provider] = r.models
	}

	out := catalog.File{
		GeneratedAt: time.Now().UTC().Truncate(time.Second),
		Models:      flatten(merged),
	}
	if err := writeJSON(outPath, out); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d models total)\n", outPath, len(out.Models))
	if hardErr {
		return fmt.Errorf("one or more providers failed")
	}
	return nil
}

// loadExisting reads the catalog file if present. Errors are tolerated
// — a corrupt or missing file just means we start fresh. The point of
// loading is to preserve entries for providers we're not refreshing
// this run (key not set), not to recover state.
func loadExisting(path string) catalog.File {
	raw, err := os.ReadFile(path)
	if err != nil {
		return catalog.File{}
	}
	var f catalog.File
	if err := json.Unmarshal(raw, &f); err != nil {
		fmt.Fprintf(os.Stderr, "warning: existing %s is unparseable, ignoring: %v\n", path, err)
		return catalog.File{}
	}
	return f
}

// flatten collapses the per-provider map into a single slice sorted
// by (provider, ID) for stable diffs.
func flatten(byProv map[string][]catalog.Model) []catalog.Model {
	out := make([]catalog.Model, 0)
	provs := make([]string, 0, len(byProv))
	for p := range byProv {
		provs = append(provs, p)
	}
	sort.Strings(provs)
	for _, p := range provs {
		ms := byProv[p]
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
		out = append(out, ms...)
	}
	return out
}

func writeJSON(path string, f catalog.File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}
