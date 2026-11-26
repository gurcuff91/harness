package admin

import (
	"sync"

	"github.com/gurcuff91/harness/agent/schedule"
)

// Schedules is the read-only view of the cron-scheduled prompts. Writes belong
// to the agent's Schedule* tools (a schedule only runs in its owner session).
//
// The store is opened lazily with schedule.Open — the same call agent.New
// makes — which is a thin view over the process-global settings manager, so
// it sees exactly what the agent's own store sees (and what other processes
// wrote). It owns no resource: nothing to close.
type Schedules struct {
	ad   *Admin
	once sync.Once
	st   *schedule.Store
}

func newSchedules(ad *Admin) *Schedules { return &Schedules{ad: ad} }

// List returns the schedules sorted by slug; owner "" = every owner, else only
// those that will fire into that session.
func (s *Schedules) List(owner string) ([]schedule.Schedule, error) {
	var err error
	s.once.Do(func() { s.st, err = schedule.Open() })
	if err != nil {
		return nil, err
	}
	if s.st == nil {
		return []schedule.Schedule{}, nil
	}
	out := []schedule.Schedule{}
	for _, sc := range s.st.List() {
		if owner != "" && sc.Owner != owner {
			continue
		}
		out = append(out, sc)
	}
	return out, nil
}
