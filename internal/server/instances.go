package server

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// InstanceInfo is the metadata stored for each running server instance.
// It mirrors serverInfo minus the "name" field (the instance name is the
// map key in instances.json).
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

// instanceMu guards concurrent access to ~/.harness/instances.json across
// multiple harness processes running on the same machine.
var instanceMu sync.Mutex

// instancesPath returns the path to ~/.harness/instances.json.
func instancesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "instances.json")
	}
	return filepath.Join(home, ".harness", "instances.json")
}

// loadInstances reads the instance registry as-is — no pruning. Dead entries
// from crashed processes are cleaned up lazily: when a name collision occurs
// during registration, the existing instance is checked via HTTP and reused
// if it's no longer responding.
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

// writeInstances serializes the registry to disk (caller holds instanceMu).
func writeInstances(instances map[string]InstanceInfo) error {
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
func generateInstanceName(existing map[string]InstanceInfo) string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := 0; i < 50; i++ {
		name := mkCharacters[r.Intn(len(mkCharacters))] + "-" + mkAdjectives[r.Intn(len(mkAdjectives))]
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
	return fmt.Sprintf("%s-%s-%d", mkCharacters[0], mkAdjectives[0], r.Intn(9999))
}

// RegisterInstance generates a unique name, inserts the instance into the
// registry, and returns the name. Called by Server.Serve on startup.
func RegisterInstance(info InstanceInfo) (string, error) {
	instances, err := loadInstances()
	if err != nil {
		return "", fmt.Errorf("load instances: %w", err)
	}

	instanceMu.Lock()
	defer instanceMu.Unlock()

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
	instanceMu.Lock()
	defer instanceMu.Unlock()

	data, err := os.ReadFile(instancesPath())
	if err != nil {
		return
	}
	var instances map[string]InstanceInfo
	if err := json.Unmarshal(data, &instances); err != nil {
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