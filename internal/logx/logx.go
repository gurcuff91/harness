// Package logx is harness's small structured logger for backend components
// (the HTTP server, the Telegram transport, …). It renders one line per event:
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
package logx

import (
	"fmt"
	"log"
	"strings"
)

// Info logs an event at INFO level for the given component. kv is a flat list of
// alternating key, value pairs (values may be any type; rendered with %v).
func Info(component, event string, kv ...any) { emit("INFO ", component, event, kv) }

// Warn logs at WARN level.
func Warn(component, event string, kv ...any) { emit("WARN ", component, event, kv) }

// Error logs at ERROR level.
func Error(component, event string, kv ...any) { emit("ERROR", component, event, kv) }

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
