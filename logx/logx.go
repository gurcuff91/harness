// Package logx is harness's structured logging: the [Logger] contract that
// server.Run and the channels (channels/slack, channels/telegram,
// channels/colleague) accept via their WithLogger option, and two
// implementations — [NewNilLogger], the silent default, and
// [NewHarnessLogger], the line format harness's own CLI prints. Implement
// Logger to route the logs anywhere else: a file, a remote collector, slog.
package logx

import (
	"fmt"
	"log"
	"strings"
)

// Logger is the structured logging contract. component names the subsystem
// emitting the line ("server", "telegram-channel", "sse"); event is a short
// machine-readable event name; kv is a flat list of alternating key, value
// pairs (values may be any type).
//
// acp deliberately has no WithLogger of its own: it never logs anything
// itself, and passes NewNilLogger() to its own in-process server.
type Logger interface {
	Info(component, event string, kv ...any)
	Warn(component, event string, kv ...any)
	Error(component, event string, kv ...any)
}

// nilLogger discards everything — see NewNilLogger.
type nilLogger struct{}

// NewNilLogger returns a Logger that discards everything — the default every
// runner and channel falls back to when no WithLogger is passed. It is also
// what the TUI and acp give their own in-process server: a log line on the
// terminal the TUI draws on (or on the stdout acp speaks JSON-RPC over) would
// corrupt it.
func NewNilLogger() Logger { return nilLogger{} }

func (nilLogger) Info(component, event string, kv ...any)  {}
func (nilLogger) Warn(component, event string, kv ...any)  {}
func (nilLogger) Error(component, event string, kv ...any) {}

// harnessLogger is harness's line format — see NewHarnessLogger.
type harnessLogger struct{}

// NewHarnessLogger returns the Logger harness's own CLI uses (serve, the
// channels): one line per event through the standard log package, so the
// timestamp and the destination are log's own (log.SetOutput, log.SetFlags):
//
//	2026/07/20 14:38:24 INFO  [telegram-channel] prompt chat=5353 user=@gus text="hi there"
//
// Levels are fixed-width so lines align; a value is quoted only when it holds
// spaces, tabs or quotes; an empty value is "".
func NewHarnessLogger() Logger { return harnessLogger{} }

func (harnessLogger) Info(component, event string, kv ...any) { emit("INFO ", component, event, kv) }

func (harnessLogger) Warn(component, event string, kv ...any) { emit("WARN ", component, event, kv) }

func (harnessLogger) Error(component, event string, kv ...any) { emit("ERROR", component, event, kv) }

func emit(level, component, event string, kv []any) {
	var b strings.Builder
	b.WriteString(level)
	b.WriteString("[")
	b.WriteString(component)
	b.WriteString("] ")
	b.WriteString(event)
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteByte(' ')
		b.WriteString(fmt.Sprint(kv[i]))
		b.WriteByte('=')
		b.WriteString(quote(fmt.Sprint(kv[i+1])))
	}
	log.Print(b.String())
}

func quote(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
