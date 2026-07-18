package catalog

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// models.dev is a community-maintained catalog of ~140 providers and their
// models, including per-deployment context windows — the same source the
// hermes agent uses. It is the reliable way to size windows for hosted
// OpenAI-compatible providers (NVIDIA NIM, Together, Fireworks, …) whose
// own /v1/models lists only ids and whose backends stall on overflow
// probes. One fetch covers every model; we cache it to disk so it works
// offline after the first run, and never bake the data into the binary.
//
// Crucially the window is matched PER PROVIDER, by base-URL host: the same
// model id (e.g. deepseek-ai/deepseek-v4-pro) has very different windows
// across hosts (65K on one, 1M on NVIDIA), so we look it up only under the
// models.dev provider whose `api` URL host matches the caller's base URL.

const modelsDevURL = "https://models.dev/api.json"

// embeddedModelsDevJSON is a full models.dev snapshot committed to the repo
// and compiled into the binary — the offline-first floor (resolution tier
// below in-memory/disk/network). It guarantees window lookups work on a
// fresh, offline install. Regenerate + commit with:
//
//	go run ./cmd/yotta-models refresh-modelsdev
//
// It's trimmed to the fields we use (provider id/api + per-model
// limit.context/output), so it's a fraction of the raw ~2MB api.json while
// still covering every provider and model.
//
//go:embed models-dev.gen.json
var embeddedModelsDevJSON []byte

// modelsDevDiskTTL is how long the on-disk cache is trusted without a
// network re-fetch. Windows change rarely, so a day keeps CLI invocations
// fast (no per-call HTTP) while staying reasonably current.
const modelsDevDiskTTL = 24 * time.Hour

// modelsDevMemTTL bounds the in-process cache so a long-lived TUI session
// eventually refreshes.
const modelsDevMemTTL = 1 * time.Hour

// modelsDevHTTPTimeout caps a single fetch. The payload is ~2MB.
var modelsDevHTTPTimeout = 15 * time.Second

// modelsDevURLOverride lets tests point the fetch at an httptest server.
var modelsDevURLOverride string

// modelsDevCachePathFn resolves the disk cache path; a var for tests.
var modelsDevCachePathFn = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".yottacode", "models-dev-cache.json"), nil
}

// modelsDevProvider / modelsDevModel cover only the fields we read; the
// real payload carries much more (cost, capabilities, …).
type modelsDevLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

type modelsDevModel struct {
	Limit modelsDevLimit `json:"limit"`
}

type modelsDevProvider struct {
	ID     string                    `json:"id"`
	API    string                    `json:"api"`
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevCatalog map[string]modelsDevProvider

// diskCacheEnvelope wraps the catalog with a fetch timestamp for TTL.
type diskCacheEnvelope struct {
	FetchedAt time.Time        `json:"fetched_at"`
	Catalog   modelsDevCatalog `json:"catalog"`
}

var (
	modelsDevMu       sync.Mutex
	modelsDevCache    modelsDevCatalog
	modelsDevCacheAt  time.Time
	modelsDevTriedNet bool // avoid hammering the network within one process when it's down
)

// loadModelsDev returns the catalog by resolution tier: in-memory cache →
// fresh disk cache → network fetch → stale disk cache → embedded snapshot.
// The embedded snapshot is the offline-first floor, so this never returns
// nil unless the committed snapshot is somehow empty/corrupt.
func loadModelsDev() modelsDevCatalog {
	modelsDevMu.Lock()
	defer modelsDevMu.Unlock()

	if modelsDevCache != nil && time.Since(modelsDevCacheAt) < modelsDevMemTTL {
		return modelsDevCache
	}

	// Fresh disk cache → use without touching the network.
	if env, ok := readModelsDevDisk(); ok && time.Since(env.FetchedAt) < modelsDevDiskTTL {
		modelsDevCache, modelsDevCacheAt = env.Catalog, env.FetchedAt
		return modelsDevCache
	}

	// Network fetch (once per process if it keeps failing).
	if !modelsDevTriedNet {
		modelsDevTriedNet = true
		if cat, err := fetchModelsDevNet(); err == nil && len(cat) > 0 {
			modelsDevCache, modelsDevCacheAt = cat, time.Now()
			writeModelsDevDisk(diskCacheEnvelope{FetchedAt: modelsDevCacheAt, Catalog: cat})
			return modelsDevCache
		}
	}

	// Network failed/skipped → fall back to a stale disk cache if any.
	if modelsDevCache == nil {
		if env, ok := readModelsDevDisk(); ok {
			modelsDevCache, modelsDevCacheAt = env.Catalog, env.FetchedAt
		}
	}

	// Offline-first floor: the committed snapshot embedded in the binary.
	// Guarantees window lookups work on a fresh, offline install.
	if modelsDevCache == nil {
		if cat := loadEmbeddedModelsDev(); len(cat) > 0 {
			modelsDevCache, modelsDevCacheAt = cat, time.Now()
		}
	}
	return modelsDevCache
}

// loadEmbeddedModelsDev parses the snapshot compiled into the binary. The
// embedded file is a diskCacheEnvelope (same shape the disk cache writes),
// so the generator and the runtime cache share one format.
func loadEmbeddedModelsDev() modelsDevCatalog {
	var env diskCacheEnvelope
	if err := json.Unmarshal(embeddedModelsDevJSON, &env); err != nil {
		return nil
	}
	return env.Catalog
}

// WriteModelsDevSnapshot fetches the live models.dev catalog and writes it
// (trimmed to the fields we use) to path as a diskCacheEnvelope. Used by
// `yotta-models refresh-modelsdev` to regenerate the committed embedded
// snapshot. Returns the number of providers written.
func WriteModelsDevSnapshot(path string) (int, error) {
	cat, err := fetchModelsDevNet()
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	// Warm the in-memory cache so a same-process consumer (e.g. the refresh
	// backfill that runs right after) reuses this fetch instead of hitting
	// the network again.
	modelsDevMu.Lock()
	modelsDevCache, modelsDevCacheAt, modelsDevTriedNet = cat, now, true
	modelsDevMu.Unlock()
	env := diskCacheEnvelope{FetchedAt: now, Catalog: cat}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return 0, err
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return 0, err
	}
	return len(cat), nil
}

func fetchModelsDevNet() (modelsDevCatalog, error) {
	u := modelsDevURL
	if modelsDevURLOverride != "" {
		u = modelsDevURLOverride
	}
	client := &http.Client{Timeout: modelsDevHTTPTimeout}
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	var cat modelsDevCatalog
	if err := json.Unmarshal(body, &cat); err != nil {
		return nil, err
	}
	return cat, nil
}

func readModelsDevDisk() (diskCacheEnvelope, bool) {
	path, err := modelsDevCachePathFn()
	if err != nil {
		return diskCacheEnvelope{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return diskCacheEnvelope{}, false
	}
	var env diskCacheEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || len(env.Catalog) == 0 {
		return diskCacheEnvelope{}, false
	}
	return env, true
}

func writeModelsDevDisk(env diskCacheEnvelope) {
	path, err := modelsDevCachePathFn()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	b, err := json.Marshal(env)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		_ = os.Rename(tmp, path)
	}
}

// WarmModelsDev triggers a TTL-gated load of the models.dev catalog — a
// no-op when the in-memory or on-disk cache is still fresh. Call it in the
// background at startup so the first real window lookup never waits on the
// ~2MB fetch, and so a stale local copy is refreshed once a day.
func WarmModelsDev() { _ = loadModelsDev() }

// RefreshModelsDev forces a network re-fetch of the models.dev catalog,
// updating the in-memory and on-disk caches regardless of TTL. Returns the
// number of providers loaded. Used by an explicit refresh flag/command.
func RefreshModelsDev() (int, error) {
	cat, err := fetchModelsDevNet()
	if err != nil {
		return 0, err
	}
	now := time.Now()
	modelsDevMu.Lock()
	modelsDevCache, modelsDevCacheAt, modelsDevTriedNet = cat, now, true
	modelsDevMu.Unlock()
	writeModelsDevDisk(diskCacheEnvelope{FetchedAt: now, Catalog: cat})
	return len(cat), nil
}

// ModelsDevWindowByProvider returns the context window for a model under a
// specific models.dev provider id (e.g. "openai", "anthropic", "google"),
// or 0. Unlike ModelsDevWindow (host-matched), this looks the provider up
// by NAME — needed for the curated labs whose models.dev entry carries no
// `api` URL to host-match against. Used by the catalog refresh to backfill
// windows a provider's own API omits (notably OpenAI, whose /v1/models
// returns no context length).
func ModelsDevWindowByProvider(providerID, model string) int {
	ctx, _ := ModelsDevLimitsByProvider(providerID, model)
	return ctx
}

// ModelsDevLimitsByProvider returns both limits models.dev records for a
// model under a specific provider id: the context window and the
// max-output cap, either of which is 0 when unknown.
//
// Output matters as much as context and is just as often missing from a
// vendor's own API — OpenAI's /v1/models reports neither, and MaxOutput
// is what sizes an extended-thinking budget. Returning the pair keeps
// both backfills on one snapshot lookup.
func ModelsDevLimitsByProvider(providerID, model string) (contextWindow, maxOutput int) {
	if strings.TrimSpace(providerID) == "" || strings.TrimSpace(model) == "" {
		return 0, 0
	}
	cat := loadModelsDev()
	if cat == nil {
		return 0, 0
	}
	prov, ok := cat[providerID]
	if !ok {
		return 0, 0
	}
	return modelLimitsFrom(prov, model)
}

// ModelsDevModelsByProvider returns model IDs from the local models.dev
// snapshot for a provider, filtered by prefix. This backs picker lists with
// a fresh offline catalog when the generated provider catalog lags a vendor
// release; it never touches the network beyond the normal models.dev cache.
func ModelsDevModelsByProvider(providerID, prefix string) []Model {
	if strings.TrimSpace(providerID) == "" {
		return nil
	}
	cat := loadModelsDev()
	if cat == nil {
		return nil
	}
	prov, ok := cat[providerID]
	if !ok {
		return nil
	}
	out := make([]Model, 0, len(prov.Models))
	for id, m := range prov.Models {
		if prefix != "" && !strings.HasPrefix(id, prefix) && !strings.HasPrefix(id, "google/"+prefix) {
			continue
		}
		out = append(out, Model{
			ID:            id,
			DisplayName:   titleModelID(id),
			Provider:      providerID,
			ContextWindow: m.Limit.Context,
			MaxOutput:     m.Limit.Output,
		})
	}
	sortModelsByID(out)
	return out
}

func sortModelsByID(ms []Model) {
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].ID < ms[j].ID })
}

func titleModelID(id string) string {
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' || r == '/' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// ModelsDevWindow returns the context window for a model on the provider
// whose base URL host matches a models.dev provider's `api` host, or 0 when
// the catalog is unavailable or has no matching entry. Matching by host
// keeps the per-deployment value (NVIDIA's 1M for deepseek-v4-pro) instead
// of some other host's listing of the same id.
func ModelsDevWindow(baseURL, model string) int {
	host := hostname(baseURL)
	if host == "" || strings.TrimSpace(model) == "" {
		return 0
	}
	cat := loadModelsDev()
	if cat == nil {
		return 0
	}
	for _, prov := range cat {
		if hostname(prov.API) != host {
			continue
		}
		if w := modelWindowFrom(prov, model); w > 0 {
			return w
		}
	}
	return 0
}

// modelWindowFrom looks a model up in a provider's set, exact first then
// case-insensitive (provider model keys and our ids are usually identical,
// but casing varies across hosts — e.g. DeepSeek-V4-Pro vs deepseek-v4-pro).
func modelWindowFrom(prov modelsDevProvider, model string) int {
	ctx, _ := modelLimitsFrom(prov, model)
	return ctx
}

// modelLimitsFrom resolves a model's limits within one provider: exact id
// first, then case-insensitively. Both limits come from the same entry —
// looking them up separately could pair one model's context with
// another's output if the two matches ever disagreed.
func modelLimitsFrom(prov modelsDevProvider, model string) (contextWindow, maxOutput int) {
	// An entry carrying neither limit is no better than no entry, so keep
	// looking — the case-insensitive pass may find a populated twin.
	if m, ok := prov.Models[model]; ok && (m.Limit.Context > 0 || m.Limit.Output > 0) {
		return m.Limit.Context, m.Limit.Output
	}
	lm := strings.ToLower(model)
	for id, m := range prov.Models {
		if strings.ToLower(id) == lm && (m.Limit.Context > 0 || m.Limit.Output > 0) {
			return m.Limit.Context, m.Limit.Output
		}
	}
	return 0, 0
}

// hostname returns the lowercased host of a URL, tolerating a bare host or
// a missing scheme.
func hostname(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
