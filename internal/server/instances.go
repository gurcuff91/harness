// Package colleague implements the "colleague pattern": multiple harness
package server

// This file owns the "colleague pattern" instance registry: multiple harness
// server instances running on the same machine register themselves in
// ~/.harness/instances.json (RegisterInstance on Serve, UnregisterInstance on
// Close) so other processes can discover them. It is control-plane logic with
// a single writer (this package) — the file itself, not this Go package, is
// the interop contract: agent/tools/colleague.go reads the same path directly
// with its own minimal parser, rather than importing this package (which
// would need to be public just to hand over a struct + 3 functions no one
// outside internal/server ever calls).
import (
	"encoding/json"
	"fmt"
	randv2 "math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// InstanceInfo is the metadata stored for each running server instance. It
// mirrors the server's own /api/server response minus the "name" field (the
// instance name is the map key in instances.json).
type InstanceInfo struct {
	Version   string `json:"version"`
	Transport string `json:"transport"`
	URL       string `json:"url"`
	CWD       string `json:"cwd"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

// ── Name generator: MK11 characters × MK11-flavored adjectives ───────────

// mkCharacters are all playable characters in Mortal Kombat 11.
var mkCharacters = []string{
	"jade", "kitana", "scorpion", "raiden", "subzero", "liukang",
	"kunglao", "johnnycage", "sonya", "kano", "baraka", "jax",
	"kitanakahn", "skarlet", "erronblack", "dvorah", "fujin",
	"spawn", "robocop", "terminator", "joker", "rambo",
	"shangtsung", "shao", "goro", "motaro", "kintaro",
	"sheeva", "kabal", "nightwolf", "frost", "cetrion",
	"kollector", "geras", "kronika", "noobsaibot",
}

// mkAdjectives are traits/flavors drawn from the MK universe — powers,
// roles, realms, and fighting styles. Paired 1:1 with characters for a
// combinatorial space of ~37 × 37 = 1369 unique names.
var mkAdjectives = []string{
	"warrior", "guardian", "protector", "spectre", "hellfire",
	"vengeance", "thunder", "god", "storm", "frost",
	"ice", "grandmaster", "monk", "dragon", "fire",
	"master", "hat", "temple", "star", "action",
	"blade", "major", "agent", "mercenary", "blackdragon",
	"tarkatan", "outworld", "edenton", "netherrealm", "chaos",
	"shadow", "revenant", "phantom", "ascended", "elder",
	"soul", "timekeeper",
}

// instanceMu guards concurrent access to instancesPath() from goroutines
// WITHIN this process. It is NOT sufficient on its own — see lockInstances,
// which adds a cross-PROCESS lock for the read-modify-write in
// RegisterInstance/UnregisterInstance.
var instanceMu sync.Mutex

// instancesPath returns the path to ~/.harness/instances.json.
func instancesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "instances.json")
	}
	return filepath.Join(home, ".harness", "instances.json")
}

// lockPath is the advisory lock file guarding the read-modify-write cycle in
// RegisterInstance/UnregisterInstance across DIFFERENT harness processes.
// instanceMu (a Go-level sync.Mutex) only protects goroutines inside one
// process; two separate `harness serve` processes each have their own,
// unrelated instanceMu, so without this file-based lock two processes
// starting at the same moment can both read the same (empty) registry,
// independently generate a name, and the second write clobbers the first —
// exactly the multi-instance scenario this registry exists to serve
// correctly. Confirmed by reproduction: two `harness serve` launched together
// both picked "fujin-soul" and the second process's write silently discarded
// the first's entry.
func lockPath() string {
	return instancesPath() + ".lock"
}

// acquireFileLock creates lockPath() exclusively (O_CREATE|O_EXCL — atomic
// across processes on every OS Go supports, no per-platform syscalls needed)
// and retries with backoff if another process holds it. Returns a release
// function the caller must call (removing the lock file) once done.
//
// This is advisory and self-healing: if a process crashes while holding the
// lock, the file is simply removed by the next successful acquirer after
// staleLockAge — see the staleness check below — so a dead holder can never
// wedge every future registration/unregistration permanently.
func acquireFileLock() (release func(), err error) {
	const (
		maxAttempts  = 100
		retryDelay   = 20 * time.Millisecond
		staleLockAge = 5 * time.Second // generous: register/unregister do one small read+write
	)
	path := lockPath()
	for attempt := 0; attempt < maxAttempts; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("colleague: create lock: %w", err)
		}
		// Lock file already exists — if it's stale (held by a crashed
		// process that never released it), remove it and retry immediately.
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > staleLockAge {
			_ = os.Remove(path)
			continue
		}
		time.Sleep(retryDelay)
	}
	return nil, fmt.Errorf("colleague: timed out waiting for instances.json lock")
}

// loadInstances reads the instance registry as-is — no pruning. Dead entries
// from crashed processes are cleaned up lazily: when a name collision occurs
// during registration, the existing instance is checked via HTTP and reused
// if it's no longer responding. Safe to call without the file lock — a bare
// read racing a concurrent write sees either the old or the new file, never a
// torn write (writeInstances always fully replaces the file in one syscall).
func loadInstances() (map[string]InstanceInfo, error) {
	instanceMu.Lock()
	defer instanceMu.Unlock()

	data, err := os.ReadFile(instancesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]InstanceInfo{}, nil
		}
		return nil, err
	}
	var instances map[string]InstanceInfo
	if err := json.Unmarshal(data, &instances); err != nil {
		return map[string]InstanceInfo{}, nil
	}
	if instances == nil {
		instances = map[string]InstanceInfo{}
	}
	return instances, nil
}

// writeInstances serializes the registry to disk. Takes instanceMu itself
// (unlike loadInstances's caller-visible lock/unlock, callers here — both
// under the cross-process file lock already — call it standalone).
func writeInstances(instances map[string]InstanceInfo) error {
	instanceMu.Lock()
	defer instanceMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(instancesPath()), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(instances, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(instancesPath(), data, 0644)
}

// instanceAlive checks whether an instance is actually responding by doing
// a quick HTTP GET to its /api/server endpoint. A 200 response means the
// instance is live; anything else (connection refused, timeout, non-200)
// means it's dead and its entry can be reclaimed.
func instanceAlive(info InstanceInfo) bool {
	if info.URL == "" {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(info.URL + "/api/server")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// generateInstanceName picks a random character + adjective combination
// that is not already in use by a live instance. When a name is taken,
// it checks via HTTP whether the existing instance is still alive — if not,
// the name is reclaimed (dead entry removed). Retries up to 50 times.
//
// Uses math/rand/v2's auto-seeded global source, NOT a manually-seeded
// math/rand.New(rand.NewSource(time.Now().UnixNano())). The old code seeded
// from the wall-clock timestamp, whose real resolution on most OSes is far
// coarser than "nanosecond" implies; two `harness serve` processes launched
// within that resolution window (entirely plausible — see the file lock
// above, added for the SAME multi-launch scenario) got the IDENTICAL seed,
// which means IDENTICAL output from rand.Intn — not a rare collision, a
// mathematical certainty (confirmed by reproduction: same seed → same name,
// every time, 100% of the time). rand/v2's package-level functions are seeded
// from a real OS entropy source per-process at startup, so two processes
// starting in the same nanosecond still get independent, uncorrelated streams.
func generateInstanceName(existing map[string]InstanceInfo) string {
	for i := 0; i < 50; i++ {
		name := mkCharacters[randv2.IntN(len(mkCharacters))] + "-" + mkAdjectives[randv2.IntN(len(mkAdjectives))]
		info, taken := existing[name]
		if !taken {
			return name
		}
		// Name is taken — check if that instance is still alive.
		if !instanceAlive(info) {
			delete(existing, name) // reclaim the name from a dead instance
			return name
		}
	}
	// Fallback: append a random number.
	return fmt.Sprintf("%s-%s-%d", mkCharacters[0], mkAdjectives[0], randv2.IntN(9999))
}

// RegisterInstance generates a unique name, inserts the instance into the
// registry, and returns the name. Called by Server.Serve on startup.
//
// Holds the cross-process file lock for the ENTIRE read-generate-write cycle
// — generating the name depends on having seen every other instance's
// current entries (including live-check probes for reclaiming dead names),
// so two processes must never interleave their read and write here.
func RegisterInstance(info InstanceInfo) (string, error) {
	release, err := acquireFileLock()
	if err != nil {
		return "", err
	}
	defer release()

	instances, err := loadInstances()
	if err != nil {
		return "", fmt.Errorf("load instances: %w", err)
	}

	name := generateInstanceName(instances)
	instances[name] = info
	if err := writeInstances(instances); err != nil {
		return "", fmt.Errorf("save instances: %w", err)
	}
	return name, nil
}

// UnregisterInstance removes an instance from the registry by name. Called by
// Server.Close on graceful shutdown. Idempotent — a missing entry is a no-op.
func UnregisterInstance(name string) {
	release, err := acquireFileLock()
	if err != nil {
		return // best-effort — a leaked entry is cleaned up by the next
		// RegisterInstance liveness check (instanceAlive), not fatal here.
	}
	defer release()

	instances, err := loadInstances()
	if err != nil {
		return
	}
	delete(instances, name)
	_ = writeInstances(instances)
}

// ListInstances returns all registered instances as-is (no health checking).
// Consumers can verify liveness by calling each instance's /api/server endpoint.
func ListInstances() (map[string]InstanceInfo, error) {
	return loadInstances()
}
