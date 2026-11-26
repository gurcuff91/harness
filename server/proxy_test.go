package server

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/gurcuff91/harness/logx"
	"github.com/gurcuff91/harness/types"
)

// newTestProxy builds a SessionProxy without a real *agent.Session — onDrop
// only touches the logger. logger
// defaults to logx.NewNilLogger() — the safer default, and what the TUI's
// in-process server actually uses.
func newTestProxy() *SessionProxy {
	return &SessionProxy{logger: logx.NewNilLogger()}
}

// captureLog redirects the standard logger's output during fn, so tests can
// assert whether logx (which writes through log.Print) produced anything.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(oldW)
		log.SetFlags(oldF)
	}()
	fn()
	return strings.TrimRight(buf.String(), "\n")
}

// The fan-out itself (deltas dropped when a client's buffer is full, control
// events waiting a bounded time and never silently lost, a slow client never
// delaying another) is agent.Session.Watch's, and is tested there
// (agent/watch_test.go). The server only reports what a watcher dropped.

// TestDroppedControlEventSilentWithNilLogger is the regression test for the
// TUI-corruption hazard: the TUI's in-process server always runs with
// logx.NewNilLogger() because it shares stdout/stderr with the raw-mode
// terminal renderer — ANY unconditional log line from the agent's own
// event-emitting goroutine (which is what calls onDrop) would corrupt the
// display.
func TestDroppedControlEventSilentWithNilLogger(t *testing.T) {
	p := newTestProxy() // logger defaults to logx.NewNilLogger()
	out := captureLog(t, func() { p.onDrop(types.Event{Type: types.EventStop}) })
	if out != "" {
		t.Errorf("onDrop logged with a NilLogger — this would corrupt the TUI's raw-mode render: %q", out)
	}
}

// Standalone `harness serve` injects a real Logger and DOES want a dropped
// control event visible.
func TestDroppedControlEventLogsWithRealLogger(t *testing.T) {
	p := newTestProxy()
	p.logger = fakeLogger{}
	out := captureLog(t, func() { p.onDrop(types.Event{Type: types.EventStop}) })
	if !strings.Contains(out, "control_event_dropped") {
		t.Errorf("expected a control_event_dropped warning with a real logger, got: %q", out)
	}
}

// fakeLogger routes through the standard log package (like
// logx.NewHarnessLogger does) so captureLog can observe it; the server itself
// never builds a logger — its caller passes one in (WithLogger).
type fakeLogger struct{}

func (fakeLogger) Info(component, event string, kv ...any)  { log.Print(component, " ", event) }
func (fakeLogger) Warn(component, event string, kv ...any)  { log.Print(component, " ", event) }
func (fakeLogger) Error(component, event string, kv ...any) { log.Print(component, " ", event) }
