package schedule

import (
	"context"
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
// CURRENT schedules from the store each time, and fires any whose next run falls
// in the elapsed window. Reading fresh every tick means schedules added, edited,
// or removed via the tools take effect immediately — no restart needed. Only one
// transport should run an engine (guarded by --scheduler) so prompts fire once.
type Engine struct {
	store  *Store
	fire   FireFunc
	cancel context.CancelFunc
}

// NewEngine builds an engine over the given store.
func NewEngine(store *Store, fire FireFunc) *Engine {
	return &Engine{store: store, fire: fire}
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

// evaluate fires every schedule whose next run time (computed from its OWN last
// run) has arrived. Anchoring on each job's last run — not a shared moving
// cursor — is what makes both absolute crons ("* * * * *") and relative ones
// ("@every 1m", a ConstantDelaySchedule whose Next is relative to its argument)
// fire correctly: for @every, Next(lastRun) = lastRun+interval, which a moving
// cursor would forever push out of reach.
//
// A schedule that has never run uses startedAt as its anchor, so it fires one
// interval after the engine starts (past-due times aren't replayed). Schedules
// are read fresh each tick, so additions/edits/deletions apply immediately.
// Invalid crons (from hand-edited files) are skipped.
func (e *Engine) evaluate(startedAt, now time.Time) {
	for _, sc := range e.store.List() {
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
			_ = e.store.RecordRun(sc.Slug, now.UnixMilli())
		}
	}
}
