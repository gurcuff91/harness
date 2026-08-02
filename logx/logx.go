// Package logx defines the structured logging contract harness's backend
// runners (server.Run, transports/telegram.Run, transports/slack.Run) accept
// via their WithLogger option — implement [Logger] to route harness's
// backend logs anywhere: stdout, a file, a remote collector. See
// internal/logx.NewHarnessLogger for the real line-oriented implementation
// harness's own CLI uses, and [NewNilLogger] here for the silent default
// every Run falls back to when no logger is configured.
package logx

// Logger is the structured logging contract every Run (server.Run,
// transports/telegram.Run, transports/slack.Run) accepts via a WithLogger
// option. component names the subsystem emitting the line (e.g. "server",
// "telegram", "sse"); event is a short machine-readable event name; kv is a
// flat list of alternating key, value pairs (values may be any type).
//
// transports/acp deliberately has no WithLogger of its own — it never logs
// anything itself, and passes NewNilLogger() to its own in-process server
// internally without exposing the option to its caller.
type Logger interface {
	Info(component, event string, kv ...any)
	Warn(component, event string, kv ...any)
	Error(component, event string, kv ...any)
}

// nilLogger discards everything — see NewNilLogger. Unexported: construct
// one only through the constructor, so callers depend on the Logger
// interface rather than this concrete (and, being stateless, otherwise
// trivially reimplementable) type.
type nilLogger struct{}

// NewNilLogger returns a Logger that discards everything — the default
// every Run falls back to when no WithLogger is passed (e.g. an SDK
// consumer that never configured one). It is also what every transport
// (telegram, slack, acp) and internal/tui pass to the in-process
// server.Server they each start for themselves: the TRANSPORT is the one
// logging (via its own injected Logger), so its inner server must stay
// silent rather than duplicating — or, for the TUI specifically,
// corrupting the raw-mode terminal render with unconditional log lines
// sharing its stdout/stderr.
func NewNilLogger() Logger { return nilLogger{} }

func (nilLogger) Info(component, event string, kv ...any)  {}
func (nilLogger) Warn(component, event string, kv ...any)  {}
func (nilLogger) Error(component, event string, kv ...any) {}
