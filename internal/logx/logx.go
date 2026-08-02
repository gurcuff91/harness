// Package logx provides harness's own structured logger implementation for
// backend components (the HTTP server, the Telegram/Slack transports, …):
// HarnessLogger, which implements the public logx.Logger contract
// (github.com/gurcuff91/harness/logx). It renders one line per event:
//
//	<timestamp> LEVEL [component] event key=value key="value with spaces"
//
// e.g.
//
//	2026/07/20 14:38:24 INFO  [telegram] prompt chat=5353 session=dde9 text="hi there"
//
// The timestamp comes from the standard log package. Levels are fixed-width so
// lines align and are easy to scan/grep. Values are quoted only when they
// contain spaces or quotes.
//
// internal/cli is the only caller that constructs HarnessLogger{} — every
// server.Run/transports/{telegram,slack}.Run call the CLI makes passes it
// explicitly via WithLogger, so the real binary logs exactly as before this
// package's functions became a Logger implementation. Every OTHER caller
// (an SDK consumer, or a transport's own in-process server) gets
// logx.NilLogger{} by default instead — see that type's doc comment.
package logx

import (
	"fmt"
	"log"
	"strings"

	publiclogx "github.com/gurcuff91/harness/logx"
)

// HarnessLogger is harness's own Logger implementation — this package's
// historical line-oriented format, now behind the public logx.Logger
// interface instead of package-level functions.
type HarnessLogger struct{}

// compile-time assertion — HarnessLogger must satisfy the public contract.
var _ publiclogx.Logger = HarnessLogger{}

// Info logs an event at INFO level for the given component. kv is a flat list of
// alternating key, value pairs (values may be any type; rendered with %v).
func (HarnessLogger) Info(component, event string, kv ...any) { emit("INFO ", component, event, kv) }

// Warn logs at WARN level.
func (HarnessLogger) Warn(component, event string, kv ...any) { emit("WARN ", component, event, kv) }

// Error logs at ERROR level.
func (HarnessLogger) Error(component, event string, kv ...any) { emit("ERROR", component, event, kv) }

// emit renders and prints one log line. Odd trailing keys (no value) are skipped.
func emit(level, component, event string, kv []any) {
	var b strings.Builder
	b.WriteString(level)
	b.WriteString("[")
	b.WriteString(component)
	b.WriteString("] ")
	b.WriteString(event)
	for i := 0; i+1 < len(kv); i += 2 {
		key := fmt.Sprint(kv[i])
		val := fmt.Sprint(kv[i+1])
		b.WriteByte(' ')
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(quote(val))
	}
	log.Print(b.String())
}

// quote wraps a value in double quotes when it contains whitespace or quotes,
// escaping embedded quotes; otherwise returns it bare. Empty becomes "".
func quote(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
