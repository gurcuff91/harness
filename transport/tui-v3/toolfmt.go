package tuiv3

import (
	"encoding/json"
	"fmt"
	"strings"
)

// primaryParam maps a built-in tool to the argument shown bare (without a
// "key=" label) right after the tool name — e.g. Read shows the path directly.
// Tools not listed (including all MCP tools) render every param as key=value.
var primaryParam = map[string]string{
	"Bash":     "command",
	"Read":     "path",
	"Write":    "path",
	"Edit":     "path",
	"Fetch":    "url",
	"Skill":    "name",
	"Subagent": "prompt",
}

// kvPair is one decoded argument, preserving JSON key order.
type kvPair struct {
	key string
	val string
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
		pairs = append(pairs, kvPair{key: key, val: renderValue(raw)})
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
		// unescaped args so the user still sees something sane.
		return unescapeArgs(strings.TrimSpace(argsJSON))
	}

	primary := primaryParam[name]
	var parts []string
	for _, p := range pairs {
		if p.key == primary {
			// Primary param shown bare. Bash gets a "$ " shell prefix.
			if name == "Bash" {
				parts = append([]string{"$ " + p.val}, parts...)
			} else {
				parts = append([]string{p.val}, parts...)
			}
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", p.key, p.val))
	}
	return strings.Join(parts, " ")
}
