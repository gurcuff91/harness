package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gurcuff91/harness/internal/transport/tui/ansi"
)

// primaryParam maps a built-in tool to the argument shown bare (without a
// "key=" label) right after the tool name — e.g. Read shows the path directly.
// Tools not listed (including all MCP tools) render every param as key=value.
var primaryParam = map[string]string{
	"Bash":       "command",
	"Read":       "path",
	"Write":      "path",
	"Edit":       "path",
	"Fetch":      "url",
	"Skill":      "name",
	"Subagent":   "prompt",
	"MemoWrite":  "slug",
	"MemoSearch": "query",
	"MemoDelete": "slug",
}

// kvPair is one decoded argument, preserving JSON key order.
type kvPair struct {
	key    string
	val    string
	rawVal json.RawMessage // original JSON value, for structural inspection (e.g. array counts)
}

// parseArgsOrdered decodes a JSON object into ordered key/value pairs, keeping
// the keys in the order they appear in the source. Values are rendered as plain
// text (strings unquoted; numbers/bools/objects as compact JSON). The bool is
// false if the JSON is incomplete or not an object (e.g. mid-stream partial
// args) — distinct from a valid-but-empty object, which returns (nil, true).
func parseArgsOrdered(argsJSON string) ([]kvPair, bool) {
	s := strings.TrimSpace(argsJSON)
	if s == "" || s[0] != '{' {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(s))
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, false
	}
	var pairs []kvPair
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false // incomplete
		}
		key, _ := keyTok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false // incomplete
		}
		pairs = append(pairs, kvPair{key: key, val: renderValue(raw), rawVal: raw})
	}
	// Closing brace must be present for the object to be complete.
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	return pairs, true
}

// renderValue turns a JSON value into display text: strings are unquoted (with
// escapes like \n → real newline), everything else is compact JSON.
func renderValue(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s // already unescaped by the JSON decoder
		}
	}
	return trimmed
}

// formatToolArgs builds the dim args portion of a tool header from the complete
// args JSON. Built-ins show their primary param bare (Bash prefixes "$ "), then
// the remaining params as key=value; MCP tools render every param as key=value.
// Order follows the JSON source. Returns "" when there are no args.
func formatToolArgs(name, argsJSON string) string {
	pairs, ok := parseArgsOrdered(argsJSON)
	if !ok {
		// Not parseable (partial stream or non-object) — fall back to the raw,
		// unescaped args (dimmed) so the user still sees something sane.
		return ansi.Dimmed(unescapeArgs(strings.TrimSpace(argsJSON)))
	}

	primary := primaryParam[name]
	var parts []string
	for _, p := range pairs {
		if p.key == primary {
			// Primary param shown bare (the icon already signals the tool kind),
			// dimmed like a value.
			parts = append([]string{ansi.Dimmed(p.val)}, parts...)
			continue
		}
		// Write dumps the entire file content into 'content' — summarize it as a
		// line count so the header stays a clean one-liner (the full content still
		// flows to the model; this is display only).
		if name == "Write" && p.key == "content" {
			if n := countLines(p.val); n > 0 {
				parts = append(parts, ansi.Muted(lineCountLabel(n)))
			}
			continue
		}
		// Edit's multi-edit array and the flat old/new text are noisy to dump
		// verbatim; summarize them so the header stays a clean one-liner. The full
		// content still flows to the model — this is display only.
		if name == "Edit" {
			if p.key == "edits" {
				if n := countJSONArray(p.rawVal); n > 0 {
					parts = append(parts, ansi.Muted(editCountLabel(n)))
				}
				continue
			}
			if p.key == "old_text" || p.key == "new_text" {
				// Flat single edit: show "(1 edit)" for parity with the array form;
				// the content itself is redundant noise in the header.
				if p.key == "old_text" {
					parts = append(parts, ansi.Muted(editCountLabel(1)))
				}
				continue
			}
		}
		// Param NAME in Muted (same weight/color as the result line) to make it
		// stand out; the VALUE stays Dimmed so it reads as secondary.
		parts = append(parts, ansi.Muted(p.key+"=")+ansi.Dimmed(p.val))
	}
	return strings.Join(parts, " ")
}

// countJSONArray returns the number of elements in a JSON array value, or 0 if
// it isn't a parseable array.
func countJSONArray(raw json.RawMessage) int {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return 0
	}
	return len(arr)
}

// editCountLabel renders the "(N edit[s])" summary shown in the Edit header.
func editCountLabel(n int) string {
	if n == 1 {
		return "(1 edit)"
	}
	return fmt.Sprintf("(%d edits)", n)
}

// countLines counts the lines in s (a trailing newline doesn't add a phantom
// empty line). Empty string is 0.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

// lineCountLabel renders the "(N line[s])" summary shown in the Write header.
func lineCountLabel(n int) string {
	if n == 1 {
		return "(1 line)"
	}
	return fmt.Sprintf("(%d lines)", n)
}
