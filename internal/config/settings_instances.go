package config

import (
	"encoding/json"
	"fmt"
)

// ── Server instances (colleague registry) ────────────────────────────────
//
// Every running harness server registers itself here so other processes can
// discover it (ColleagueList/ColleagueAsk). Naming, liveness probing and
// purging live in server/instances.go; this is only typed persistence.
//
// Store layout: namespace "instances", key = instance name, value = InstanceEntry.

const nsInstances = "instances"

// InstanceEntry is one registered server instance.
type InstanceEntry struct {
	Version   string `json:"version"`
	Transport string `json:"transport"`
	URL       string `json:"url"`
	CWD       string `json:"cwd"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

// vacantInstance is the legacy marker older harness versions (0.81–0.84)
// wrote for a vacated name, before SwapValue could delete atomically. Readers
// still treat it as absent, ReserveInstance as free, and purges remove it
// (see DeleteVacantInstances) — it is never written anymore.
const vacantInstance = `null`

// Instances returns every registered instance, keyed by name (vacated names
// are skipped).
func (m *SettingsManager) Instances() map[string]InstanceEntry {
	out := map[string]InstanceEntry{}
	entries, err := m.store.List(nsInstances)
	if err != nil {
		return out
	}
	for name, raw := range entries {
		if info, ok := decodeInstance(raw); ok {
			out[name] = info
		}
	}
	return out
}

// decodeInstance decodes a stored instance value, reporting false for a
// vacated (or undecodable) entry.
func decodeInstance(raw []byte) (InstanceEntry, bool) {
	var info *InstanceEntry
	if json.Unmarshal(raw, &info) != nil || info == nil {
		return InstanceEntry{}, false
	}
	return *info, true
}

// ReserveInstance registers info under name ONLY if that name is free —
// atomic across processes (SettingsStore.SwapValue), so two processes racing
// for the same name can never both get it. Returns whether the reservation
// succeeded; false means the name is taken (pick another).
func (m *SettingsManager) ReserveInstance(name string, info InstanceEntry) (reserved bool, err error) {
	encoded, err := json.Marshal(info)
	if err != nil {
		return false, fmt.Errorf("config: encode instance %q: %w", name, err)
	}
	err = m.store.SwapValue(nsInstances, name, func(raw []byte, found bool) ([]byte, bool, error) {
		if found {
			if _, live := decodeInstance(raw); live {
				reserved = false
				return nil, false, nil
			}
		}
		reserved = true
		return encoded, true, nil
	})
	return reserved, err
}

// DeleteInstanceIf removes the instance registered under name, but only if it
// still belongs to pid — i.e. it's still the exact registration a liveness
// probe found dead. A process that re-registered the same name in the
// meantime (with its own pid) is left alone. The check and the removal are
// ONE atomic step (SwapValue's delete form), so a re-registration can never
// slip in between them.
func (m *SettingsManager) DeleteInstanceIf(name string, pid int) error {
	return m.store.SwapValue(nsInstances, name, func(raw []byte, found bool) ([]byte, bool, error) {
		cur, live := decodeInstance(raw)
		if !found || !live || cur.PID != pid {
			return nil, false, nil
		}
		return nil, true, nil // delete
	})
}

// DeleteVacantInstances removes every legacy vacant marker (see
// vacantInstance) left by older harness versions, each one atomically and
// only if it's still vacant — a name re-registered meanwhile is kept.
// Returns how many were removed.
func (m *SettingsManager) DeleteVacantInstances() (removed int) {
	entries, err := m.store.List(nsInstances)
	if err != nil {
		return 0
	}
	for name, raw := range entries {
		if _, live := decodeInstance(raw); live {
			continue
		}
		deleted := false
		err := m.store.SwapValue(nsInstances, name, func(raw []byte, found bool) ([]byte, bool, error) {
			if _, live := decodeInstance(raw); !found || live {
				return nil, false, nil
			}
			deleted = true
			return nil, true, nil // delete
		})
		if err == nil && deleted {
			removed++
		}
	}
	return removed
}

// DeleteInstance removes the instance registered under name unconditionally
// (graceful shutdown of the instance itself). Deleting a missing name is not
// an error.
func (m *SettingsManager) DeleteInstance(name string) error {
	return m.store.Delete(nsInstances, name)
}
