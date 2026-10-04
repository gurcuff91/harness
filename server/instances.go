package server

// This file owns the "colleague pattern" instance registry: multiple harness
// server instances running on the same machine register themselves
// (RegisterInstance on Serve, UnregisterInstance on Close) so other processes
// can discover them. Naming, liveness probing and purging live here;
// persistence goes through harness's settings store (internal/config's
// SettingsManager, namespace "instances") — the same store
// agent/tools/colleague.go reads, so they share one source of truth wherever
// settings live (~/.harness/settings.json by default).
import (
	"fmt"
	randv2 "math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/gurcuff91/harness/internal/config"
)

// InstanceInfo is the metadata stored for each running server instance. It
// mirrors the server's own /api/server response minus the "name" field (the
// instance name is the registry key) — and is the GET /api/instances shape.
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

// purgeDeadInstances probes every registered instance CONCURRENTLY (one
// goroutine per entry, same pattern agent/session.go uses for parallel tool
// execution) and removes the ones that don't answer. Returns how many were
// removed — a courtesy value for logging, callers don't need to branch on it.
//
// Why this exists: a process that never runs Server.Close() (crash, SIGKILL,
// or — the incident that surfaced this — SIGTSTP/Ctrl+Z suspending the
// process without terminating it) never calls UnregisterInstance, so its
// entry would stay forever, offering a permanently unreachable colleague to
// ColleagueList/ColleagueAsk. Reproduced live: a TUI suspended by Ctrl+Z kept
// its listening socket open (TCP connects fine) while no goroutine was
// running to answer, hanging ColleagueAsk indefinitely.
//
// Each removal is conditional on the entry still carrying the PID that was
// probed (SettingsManager.DeleteInstanceIf): a process that re-registered
// the same name while the probes ran keeps its fresh registration.
//
// Concurrent probing bounds the wall-clock cost to roughly the slowest single
// probe (~2s), not the sum — a busy dev machine can accumulate 100+ entries.
func purgeDeadInstances(settings *config.SettingsManager) (removed int) {
	type result struct {
		name  string
		pid   int
		alive bool
	}
	instances := settings.Instances()
	results := make(chan result, len(instances))
	var wg sync.WaitGroup
	for name, info := range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- result{name, info.PID, instanceAlive(fromEntry(info))}
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		if !r.alive && settings.DeleteInstanceIf(r.name, r.pid) == nil {
			removed++
		}
	}
	return removed
}

// randomInstanceName picks a random MK11 character + adjective combination.
//
// Uses math/rand/v2's auto-seeded global source, NOT a manually-seeded
// math/rand.New(rand.NewSource(time.Now().UnixNano())). Wall-clock seeding
// gave two processes launched within the clock's real resolution the
// IDENTICAL seed — and so the identical name, every time (confirmed by
// reproduction). rand/v2's package-level functions are seeded from OS
// entropy per process, so simultaneous launches get independent streams.
func randomInstanceName() string {
	return mkCharacters[randv2.IntN(len(mkCharacters))] + "-" + mkAdjectives[randv2.IntN(len(mkAdjectives))]
}

// RegisterInstance registers info under a fresh, unique name and returns the
// name. Called by Server.Serve on startup.
//
// First purges every dead entry from the whole registry (see
// purgeDeadInstances) — the only place stale entries get cleaned up, so the
// registry self-heals every time ANY harness process starts, with no
// dedicated background sweeper.
//
// Uniqueness is atomic across processes: each candidate name is claimed via
// SettingsManager.ReserveInstance, which writes only if the name is free, as
// one SettingsStore.SwapValue — two processes racing for the same name can
// never both get it; the loser just tries another. A taken name whose holder
// is dead (missed by the purge, e.g. it died a moment ago) is reclaimed too.
func RegisterInstance(info InstanceInfo) (string, error) {
	settings := config.GetSettingsManager()
	purgeDeadInstances(settings)

	entry := toEntry(info)
	for range 50 {
		name := randomInstanceName()
		if name, ok, err := claimInstanceName(settings, name, entry); err != nil || ok {
			return name, err
		}
	}
	// Fallback for an (almost) exhausted name space: append a random number.
	for range 50 {
		name := fmt.Sprintf("%s-%04d", randomInstanceName(), randv2.IntN(10000))
		if name, ok, err := claimInstanceName(settings, name, entry); err != nil || ok {
			return name, err
		}
	}
	return "", fmt.Errorf("register instance: no free name after 100 attempts")
}

// claimInstanceName tries to reserve name for entry, reclaiming it first if
// its current holder is dead. ok reports whether name is now ours.
func claimInstanceName(settings *config.SettingsManager, name string, entry config.InstanceEntry) (string, bool, error) {
	reserved, err := settings.ReserveInstance(name, entry)
	if err != nil || reserved {
		return name, reserved, err
	}
	holder, taken := settings.Instances()[name]
	if !taken || instanceAlive(fromEntry(holder)) {
		return name, false, nil
	}
	if err := settings.DeleteInstanceIf(name, holder.PID); err != nil {
		return name, false, err
	}
	reserved, err = settings.ReserveInstance(name, entry)
	return name, reserved, err
}

// UnregisterInstance removes an instance from the registry by name. Called by
// Server.Close on graceful shutdown. Idempotent — a missing entry is a no-op;
// best-effort, since a leaked entry is purged by the next RegisterInstance.
func UnregisterInstance(name string) {
	_ = config.GetSettingsManager().DeleteInstance(name)
}

// ListInstances returns all registered instances as-is (no health checking).
// Consumers can verify liveness by calling each instance's /api/server
// endpoint.
func ListInstances() (map[string]InstanceInfo, error) {
	out := map[string]InstanceInfo{}
	for name, e := range config.GetSettingsManager().Instances() {
		out[name] = fromEntry(e)
	}
	return out, nil
}

func toEntry(i InstanceInfo) config.InstanceEntry {
	return config.InstanceEntry{Version: i.Version, Transport: i.Transport, URL: i.URL, CWD: i.CWD, PID: i.PID, StartedAt: i.StartedAt}
}

func fromEntry(e config.InstanceEntry) InstanceInfo {
	return InstanceInfo{Version: e.Version, Transport: e.Transport, URL: e.URL, CWD: e.CWD, PID: e.PID, StartedAt: e.StartedAt}
}
