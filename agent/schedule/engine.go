package schedule

import (
	"context"
	"time"

	"github.com/robfig/cron/v3"
)

// FireFunc is invoked by the engine when a schedule is due. It receives the
// schedule's slug and prompt; the transport turns the prompt into a turn.
type FireFunc func(slug, prompt string)

// Engine runs scheduled prompts using robfig/cron. Each schedule becomes a cron
// entry that, when due, calls FireFunc and records the run. Only one transport
// should run an engine (guarded by the --scheduler flag) so prompts don't fire
// twice.
type Engine struct {
	store *Store
	fire  FireFunc
	cron  *cron.Cron
}

// NewEngine builds an engine over the given store. fire is called for each due
// schedule (typically enqueues the prompt on the active session).
func NewEngine(store *Store, fire FireFunc) *Engine {
	return &Engine{
		store: store,
		fire:  fire,
		// Match the store's parser: standard 5-field cron + @descriptors, local time.
		cron: cron.New(cron.WithParser(parser)),
	}
}

// Start registers every current schedule and begins firing. Invalid expressions
// are skipped (they were validated on Set, but a hand-edited file may drift).
// Returns the number of schedules registered.
func (e *Engine) Start(ctx context.Context) int {
	n := 0
	for _, sc := range e.store.List() {
		sc := sc // capture
		if _, err := e.cron.AddFunc(sc.Cron, func() {
			e.fire(sc.Slug, sc.Prompt)
			_ = e.store.RecordRun(sc.Slug, time.Now().UnixMilli())
		}); err == nil {
			n++
		}
	}
	e.cron.Start()
	go func() {
		<-ctx.Done()
		e.cron.Stop()
	}()
	return n
}

// Stop halts the engine. In-flight fires are not interrupted.
func (e *Engine) Stop() { e.cron.Stop() }
