package tui

import (
	"strings"
	"testing"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
)

// fmtArgs runs formatToolArgs and strips ANSI so tests assert on logical
// content (param names are Muted, values Dimmed — styling is verified separately).
func fmtArgs(name, args string) string { return stripANSI(formatToolArgs(name, args)) }

func TestFormatToolArgsBuiltins(t *testing.T) {
	cases := map[string]struct{ name, args, want string }{
		"read primary + secondary": {
			"Read", `{"path":"a.go","offset":1,"limit":50}`, "a.go offset=1 limit=50",
		},
		"bash command bare": {
			"Bash", `{"command":"go test"}`, "go test",
		},
		"edit": {
			"Edit", `{"path":"x.go","old_text":"a","new_text":"b"}`, "x.go old_text=a new_text=b",
		},
		"fetch": {
			"Fetch", `{"url":"http://x","method":"GET"}`, "http://x method=GET",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := fmtArgs(c.name, c.args); got != c.want {
				t.Errorf("formatToolArgs(%s) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

func TestFormatToolArgsMCP(t *testing.T) {
	// Unknown tool → all params as key=value, order preserved.
	got := fmtArgs("mcp__x__y", `{"b":"2","a":"1"}`)
	if got != "b=2 a=1" {
		t.Errorf("got %q, want b=2 a=1 (source order)", got)
	}
}

func TestFormatToolArgsMultilineValue(t *testing.T) {
	// A \n inside a string value becomes a real newline.
	got := fmtArgs("Subagent", `{"prompt":"line1\nline2"}`)
	if got != "line1\nline2" {
		t.Errorf("got %q, want line1<newline>line2", got)
	}
}

func TestFormatToolArgsPartialJSON(t *testing.T) {
	// Incomplete streaming JSON → falls back to unescaped raw (no crash).
	got := formatToolArgs("Read", `{"path":"a.go`)
	if got == "" {
		t.Errorf("partial JSON should fall back to something, got empty")
	}
}

func TestFormatToolArgsNonStringValues(t *testing.T) {
	got := fmtArgs("mcp__x__y", `{"count":5,"enabled":true}`)
	if got != "count=5 enabled=true" {
		t.Errorf("got %q, want count=5 enabled=true", got)
	}
}

func TestFormatToolArgsEmpty(t *testing.T) {
	if got := fmtArgs("Read", `{}`); got != "" {
		t.Errorf("empty args should render empty, got %q", got)
	}
}

func TestFormatToolArgsStyling(t *testing.T) {
	// The param NAME is wrapped in Muted, the VALUE in Dimmed.
	raw := formatToolArgs("Read", `{"path":"a.go","offset":1}`)
	// Muted wraps "offset=", Dimmed wraps "1" and the bare primary "a.go".
	if !strings.Contains(raw, ansi.Muted("offset=")) {
		t.Errorf("param name should be Muted-styled:\n%q", raw)
	}
	if !strings.Contains(raw, ansi.Dimmed("1")) {
		t.Errorf("param value should be Dimmed:\n%q", raw)
	}
	if !strings.Contains(raw, ansi.Dimmed("a.go")) {
		t.Errorf("primary param should be Dimmed:\n%q", raw)
	}
}
