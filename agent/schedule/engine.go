package schedule

import (
	"context"
	"sync"
	"time"
)

// FireFunc is invoked by the engine when a schedule is due. It receives the
// schedule's slug, prompt, and owner (the session id to route the prompt to;
// empty for single-session transports).
type FireFunc func(slug, prompt, owner string)

// tickInterval is how often the engine re-evaluates schedules. It's sub-minute
// (the finest cron granularity is 1 minute) so no minute is ever skipped; the
// window check below prevents any double-firing between ticks.
const tickInterval = 30 * time.Second

// Engine runs scheduled prompts. A single goroutine wakes on a ticker, reads the
// CURRENT schedules from the store each time, and fires any whose OWNER is
// currently watched (see AddSession) and whose next run falls in the elapsed
// window. Reading fresh every tick means schedules added, edited, or removed
// via the tools take effect immediately — no restart needed.
//
// Multiple harness processes may each run their OWN Engine over the SAME
// shared schedules.json (one per --scheduler-enabled instance) — this is the
// intended deployment shape, not an edge case: e.g. one instance running the
// TUI and another running `harness slack --scheduler`, both alive at once.
// Without the watched-session filter below, every engine would evaluate
// EVERY schedule regardless of which instance actually has that schedule's
// owner session active — a race where whichever process's tick happens to
// land first wins the fire+RecordRun, even if it has no live session to
// deliver the prompt to at all (a real, field-reported bug: a schedule
// fired-and-recorded by an instance with no matching active session, while
// the instance that DID have it sat silent). AddSession/DelSession/
// ClearSessions let the owning Agent tell this engine exactly which
// sessions are its own to fire into — a schedule whose owner isn't watched
// is skipped entirely (never fired, never RecordRun'd), so it stays
// available for whichever OTHER engine actually has that session live.
type Engine struct {
	store  *Store
	fire   FireFunc
	cancel context.CancelFunc

	// watchMu guards watched ONLY — deliberately independent of Store.mu
	// (a different concern: which owners THIS engine may act on, not the
	// store's own data). Starts empty: a freshly constructed Engine watches
	// nothing and therefore fires nothing until sessions are added, even
	// before Start is called.
	watchMu sync.Mutex
	watched map[string]bool
}

// NewEngine builds an engine over the given store. It starts with no
// watched sessions — see AddSession.
func NewEngine(store *Store, fire FireFunc) *Engine {
	return &Engine{store: store, fire: fire, watched: map[string]bool{}}
}

// AddSession makes sessionID eligible to receive prompts this engine fires —
// a schedule whose owner isn't watched is skipped entirely during evaluate,
// so an engine with no watched sessions does nothing at all. Safe to call
// before or after Start, and safe to call concurrently with evaluate
// running in the background (guarded by watchMu, independent of any store
// lock). A no-op for the empty string — that's the sentinel for a stale,
// pre-owner schedule (see store.go), never a real session id worth
// watching.
func (e *Engine) AddSession(sessionID string) {
	if sessionID == "" {
		return
	}
	e.watchMu.Lock()
	e.watched[sessionID] = true
	e.watchMu.Unlock()
}

// DelSession stops sessionID from receiving prompts this engine fires (e.g.
// when that session closes) — a no-op if it wasn't watched.
func (e *Engine) DelSession(sessionID string) {
	e.watchMu.Lock()
	delete(e.watched, sessionID)
	e.watchMu.Unlock()
}

// ClearSessions removes every watched session, returning the engine to its
// initial do-nothing state.
func (e *Engine) ClearSessions() {
	e.watchMu.Lock()
	e.watched = map[string]bool{}
	e.watchMu.Unlock()
}

// Watching reports whether sessionID is currently eligible for this engine
// to fire into. Exported as a read-only introspection point (e.g. for a
// caller confirming registerSession's wiring actually reached the engine)
// — mutating watched state always goes through AddSession/DelSession/
// ClearSessions, never through this.
func (e *Engine) Watching(sessionID string) bool {
	e.watchMu.Lock()
	defer e.watchMu.Unlock()
	return e.watched[sessionID]
}

// Start launches the polling goroutine and returns immediately. It runs until
// ctx is cancelled (or Stop cancels it).
func (e *Engine) Start(ctx context.Context) {
	ctx, e.cancel = context.WithCancel(ctx)
	go e.run(ctx)
}

// Stop halts the engine. In-flight fires are not interrupted.
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
}

// run is the single polling loop. startedAt anchors schedules that have never
// run (so a fresh schedule fires relative to when the engine came up, and past
// due times are NOT replayed — the simple, no-catch-up policy).
func (e *Engine) run(ctx context.Context) {
	startedAt := time.Now()
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			e.evaluate(startedAt, now)
		}
	}
}

// evaluate fires every WATCHED schedule (see AddSession) whose next run time
// (computed from its OWN last run) has arrived. Anchoring on each job's last
// run — not a shared moving cursor — is what makes both absolute crons
// ("* * * * *") and relative ones ("@every 1m", a ConstantDelaySchedule whose
// Next is relative to its argument) fire correctly: for @every,
// Next(lastRun) = lastRun+interval, which a moving cursor would forever push
// out of reach.
//
// A schedule whose owner isn't currently watched by THIS engine is skipped
// BEFORE any cron parsing, firing, or RecordRun — it never touches the
// shared store at all for that schedule, leaving it entirely to whichever
// other engine (if any) does watch its owner. This is what makes several
// engines safely share one schedules.json: each only ever acts on its own
// slice, so there's no race to "win" a fire for a schedule two engines both
// happened to evaluate in the same tick.
//
// A schedule that has never run uses startedAt as its anchor, so it fires one
// interval after the engine starts (past-due times aren't replayed). Schedules
// are read fresh each tick, so additions/edits/deletions apply immediately.
// Invalid crons (from hand-edited files) are skipped.
func (e *Engine) evaluate(startedAt, now time.Time) {
	for _, sc := range e.store.List() {
		if !e.Watching(sc.Owner) {
			continue
		}
		sched, err := parser.Parse(sc.Cron)
		if err != nil {
			continue
		}
		anchor := startedAt
		if sc.LastRun > 0 {
			anchor = time.UnixMilli(sc.LastRun)
		}
		next := sched.Next(anchor)
		if !next.After(now) { // due: its next run from the anchor has arrived
			e.fire(sc.Slug, sc.Prompt, sc.Owner)
			_ = e.store.RecordRun(sc.Slug, sc.Owner, now.UnixMilli())
		}
	}
}
