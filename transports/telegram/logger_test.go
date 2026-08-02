package telegram

import "testing"

// recordingLogger is a minimal logx.Logger that records every call — used
// to verify WithLogger genuinely reaches this transport's own log lines,
// without depending on internal/logx (this package must not import it —
// only internal/cli constructs internal/logx.NewHarnessLogger() and passes it
// in via WithLogger).
type recordingLogger struct {
	lines []string
}

func (r *recordingLogger) Info(component, event string, kv ...any) {
	r.lines = append(r.lines, component+" "+event)
}
func (r *recordingLogger) Warn(component, event string, kv ...any)  { r.Info(component, event, kv...) }
func (r *recordingLogger) Error(component, event string, kv ...any) { r.Info(component, event, kv...) }

// TestWithLoggerSetsOptionsLogger verifies WithLogger populates
// Options.logger with exactly the given Logger — the mechanism Run relies
// on to thread a caller-supplied Logger into the Transport it builds (see
// runWithOptions, which copies opts.logger into Transport.logger and passes
// logx.NewNilLogger() — never this one — to the transport's own in-process
// server, so logs aren't duplicated between the two layers).
func TestWithLoggerSetsOptionsLogger(t *testing.T) {
	var o Options
	rec := &recordingLogger{}
	WithLogger(rec)(&o)
	if o.logger != rec {
		t.Errorf("WithLogger did not set Options.logger to the given logger")
	}
	// Sanity: the recorder itself behaves like a real Logger.
	o.logger.Info("telegram", "test_event")
	if len(rec.lines) != 1 || rec.lines[0] != "telegram test_event" {
		t.Errorf("recorder did not capture the Info call: %v", rec.lines)
	}
}

// TestOptionsLoggerIsNilBeforeRunAppliesItsDefault verifies Options.logger
// stays nil when WithLogger isn't among the applied options — Run itself
// (not buildOptions/each Option) is responsible for defaulting a nil logger
// to logx.NewNilLogger(), exactly like every other runner in this codebase
// (server.Run, slack.Run).
func TestOptionsLoggerIsNilBeforeRunAppliesItsDefault(t *testing.T) {
	var o Options
	for _, opt := range []Option{WithToken("x")} { // no WithLogger
		opt(&o)
	}
	if o.logger != nil {
		t.Errorf("Options.logger = %v, want nil before Run applies its own default", o.logger)
	}
}
