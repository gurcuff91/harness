package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gurcuff91/harness/types"
)

// ── Remote model registry (OpenRouter catalog) ────────────────────────────
//
// The catalog must ALWAYS be available fast, and a slow or broken registry
// server must never block anything (it's reached from Agent.Models() →
// GET /api/models, i.e. startup). Observed live: the plain catalog URL was
// served from a broken CDN cache entry — 200, then the body stalled forever
// — while any query string bypassed it. So:
//
//  1. every request carries a fresh cache-buster (?ts=<unix-millis>);
//  2. one timeout covers headers AND body (registryHTTPTimeout);
//  3. a failed attempt (timeout, 5xx, truncated/malformed body) is retried
//     with a new cache-buster and a short backoff;
//  4. if every attempt fails, the last good catalog on disk
//     (~/.harness/cache/model-registry.json, rewritten on each success) is
//     used — never an empty registry — and the hardcoded registry still
//     backs everything as before;
//  5. a failure is never latched: a background refresher keeps retrying
//     (backoff) until a fetch succeeds, and swaps the fresh catalog in;
//  6. with a disk copy present, it's served immediately and the network
//     fetch happens in the background — callers never wait on the network.
//
// The disk file is a CACHE (deletable at any time, re-fetched), only ever a
// fallback — never the primary source.

// registryConfig is the tunable fetch configuration — read once per round
// into a local copy, so a running refresher never reads it while it's being
// changed (tests swap it).
type registryConfig struct {
	url            string
	timeout        time.Duration
	retryDelays    []time.Duration // between attempts → 1+len attempts per round
	refreshBackoff []time.Duration // background refresher, between failed rounds
	cachePath      func() string   // disk cache location ("" disables it)
}

var (
	registryURL         = "https://openrouter.ai/api/v1/models"
	registryHTTPTimeout = 15 * time.Second
	registryRetryDelays = []time.Duration{time.Second, 2 * time.Second} // between attempts → 3 attempts
	// registryRefreshBackoff is the background refresher's wait between
	// rounds after a failed round (each round = the full attempt sequence).
	registryRefreshBackoff = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}
	// registryCachePath returns the disk cache location ("" disables it).
	registryCachePath = defaultRegistryCachePath
)

type registryState struct {
	mu         sync.Mutex
	gen        int            // bumped by reset; a refresher of an older gen exits
	cfg        registryConfig // snapshot taken at first load
	loaded     bool           // first load done (from disk or network)
	reg        map[string]types.ModelMeta
	fresh      bool // reg came from a successful network fetch this process
	refreshing bool // a background refresher is running
	onUpdate   []func()
	version    atomic.Int64 // bumped every time reg is replaced
}

var registry = &registryState{}

func defaultRegistryCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".harness", "cache", "model-registry.json")
}

// getRemoteRegistry returns the current catalog, never blocking indefinitely
// and never returning nil:
//   - first call with a disk copy: returns it immediately and refreshes from
//     the network in the background;
//   - first call without one: fetches (bounded by timeout + retries, ~50s
//     worst case); on failure returns an empty map (the hardcoded registry
//     still applies) and keeps retrying in the background.
//
// Later calls return the latest catalog instantly.
func getRemoteRegistry() map[string]types.ModelMeta {
	r := registry
	r.mu.Lock()
	if r.loaded {
		reg := r.reg
		r.mu.Unlock()
		return reg
	}
	r.cfg = currentRegistryConfig()
	if disk := loadRegistryCache(r.cfg); disk != nil {
		r.reg, r.loaded = disk, true
		r.version.Add(1)
		r.startRefreshLocked()
		r.mu.Unlock()
		return disk
	}
	// No disk copy: fetch synchronously, holding the lock so concurrent first
	// callers wait for this one fetch instead of each starting their own.
	defer r.mu.Unlock()
	if reg, err := fetchRegistryWithRetry(r.cfg); err == nil {
		r.reg, r.loaded, r.fresh = reg, true, true
		r.version.Add(1)
		saveRegistryCache(r.cfg, reg)
		return reg
	}
	r.reg, r.loaded = map[string]types.ModelMeta{}, true
	r.startRefreshLocked()
	return r.reg
}

// RegistryVersion changes every time the catalog in use is replaced — lets a
// caller that derived data from it (cached model prices) know to recompute.
func RegistryVersion() int64 { return registry.version.Load() }

// OnRegistryUpdate registers fn to run (in its own goroutine) whenever a
// background refresh replaces the catalog — e.g. to re-price cached models.
func OnRegistryUpdate(fn func()) {
	registry.mu.Lock()
	registry.onUpdate = append(registry.onUpdate, fn)
	registry.mu.Unlock()
}

// startRefreshLocked launches the background refresher unless one is
// running or the catalog is already fresh. Caller holds r.mu.
func (r *registryState) startRefreshLocked() {
	if r.refreshing || r.fresh {
		return
	}
	r.refreshing = true
	go r.refreshLoop(r.gen, r.cfg)
}

// refreshLoop fetches until one round succeeds, backing off between rounds,
// then swaps the fresh catalog in, persists it and notifies subscribers.
func (r *registryState) refreshLoop(gen int, cfg registryConfig) {
	for round := 0; ; round++ {
		reg, err := fetchRegistryWithRetry(cfg)
		r.mu.Lock()
		if r.gen != gen { // state was reset: this refresher is obsolete
			r.mu.Unlock()
			return
		}
		if err == nil {
			r.reg, r.loaded, r.fresh, r.refreshing = reg, true, true, false
			r.version.Add(1)
			subs := append([]func(){}, r.onUpdate...)
			r.mu.Unlock()
			saveRegistryCache(cfg, reg)
			for _, fn := range subs {
				go fn()
			}
			return
		}
		r.mu.Unlock()
		time.Sleep(cfg.refreshBackoff[min(round, len(cfg.refreshBackoff)-1)])
	}
}

func currentRegistryConfig() registryConfig {
	return registryConfig{
		url:            registryURL,
		timeout:        registryHTTPTimeout,
		retryDelays:    registryRetryDelays,
		refreshBackoff: registryRefreshBackoff,
		cachePath:      registryCachePath,
	}
}

// fetchRegistryWithRetry runs up to 1+len(registryRetryDelays) attempts.
func fetchRegistryWithRetry(cfg registryConfig) (map[string]types.ModelMeta, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		reg, err := fetchRemoteRegistryOnce(cfg)
		if err == nil {
			return reg, nil
		}
		lastErr = err
		if attempt >= len(cfg.retryDelays) {
			return nil, lastErr
		}
		time.Sleep(cfg.retryDelays[attempt])
	}
}

// fetchRemoteRegistryOnce is one attempt: a fresh cache-buster, one timeout
// over headers AND body, and a rejected result on a non-200, a truncated or
// malformed body, or an empty catalog.
func fetchRemoteRegistryOnce(cfg registryConfig) (map[string]types.ModelMeta, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	url := cfg.url + "?ts=" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body) // bounded by ctx's deadline
	if err != nil {
		return nil, fmt.Errorf("registry: read body: %w", err)
	}
	reg := parseRegistry(data)
	if len(reg) == 0 {
		return nil, errors.New("registry: malformed or empty catalog")
	}
	return reg, nil
}

// ── Disk cache ────────────────────────────────────────────────────────────

func loadRegistryCache(cfg registryConfig) map[string]types.ModelMeta {
	path := cfg.cachePath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var reg map[string]types.ModelMeta
	if json.Unmarshal(data, &reg) != nil || len(reg) == 0 {
		return nil
	}
	return reg
}

// saveRegistryCache writes atomically (temp file + rename); best effort.
func saveRegistryCache(cfg registryConfig, reg map[string]types.ModelMeta) {
	path := cfg.cachePath()
	if path == "" || len(reg) == 0 {
		return
	}
	data, err := json.Marshal(reg)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".model-registry-*.json")
	if err != nil {
		return
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
	}
}

// resetRegistryForTest drops all registry state (tests only).
func resetRegistryForTest() {
	registry.mu.Lock()
	registry.gen++ // any running refresher exits at its next check
	registry.loaded, registry.reg, registry.fresh, registry.refreshing, registry.onUpdate = false, nil, false, false, nil
	registry.mu.Unlock()
}
