package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Upstream describes how to reach a provider's NATIVE API on behalf of a
// caller: the base URL, the credentials already resolved into headers, which
// paths may be called, and (for the one provider whose backend demands it) a
// body rewrite. It is what lets harness act as a transparent proxy — a client
// speaks the provider's own dialect (Anthropic Messages, OpenAI Chat
// Completions, Codex Responses…) and never sees, holds or refreshes a
// credential.
//
// Every Provider implements Upstream() so the proxy covers providers added at
// runtime too; the headers come from the SAME builders CompleteStream uses, so
// the two paths cannot drift.
type Upstream struct {
	// BaseURL is the provider's native root, without a trailing slash. The
	// proxied path is appended to it verbatim (after Allowed accepts it), so
	// "/api/providers/{name}/proxy/v1/messages" reaches BaseURL+"/v1/messages".
	BaseURL string

	// Headers are the authentication / identity headers to send, already
	// resolved (an OAuth token is refreshed by Upstream() itself). They REPLACE
	// whatever the caller sent under the same name, and any credential header
	// the caller sent that is not in this map is dropped. (anthropic-beta is
	// the one exception: the caller's values are merged into these.)
	Headers map[string]string

	// Defaults are headers sent only when the caller didn't send its own —
	// protocol-version pins a vendor SDK sets itself (anthropic-version).
	Defaults map[string]string

	// Allowed reports whether the proxy may forward method+path (path is
	// relative to BaseURL, always starting with "/", already cleaned). An
	// allowlist, not a wildcard: an OAuth token must not become a key to
	// every endpoint of the account.
	Allowed func(method, path string) bool

	// Rewrite, if set, transforms the request body of a POST before it is
	// forwarded (path is the relative path). It returns the new body and the
	// extra/overriding headers that depend on it. Only claude-oauth needs it.
	Rewrite func(path string, body []byte, headers map[string]string) ([]byte, error)

	// RewriteResponse, if set, transforms each JSON value of the response
	// before it reaches the caller: the whole body of an application/json
	// response, or the payload of every "data:" line of an event stream. It
	// must return its input unchanged when there is nothing to do (it runs on
	// every event). Only claude-oauth needs it (tool names back to harness's).
	RewriteResponse func(jsonValue []byte) []byte

	// Client is the provider's own HTTP client (the dedicated provider
	// transport: dead-connection detection, header timeout).
	Client *http.Client
}

// ErrUpstreamNotAllowed is returned (wrapped) for a method+path outside the
// provider's allowlist.
var ErrUpstreamNotAllowed = errors.New("path not allowed for this provider")

// ErrNoUpstream is returned by Upstream() when the provider has no native API
// to proxy to (or isn't usable right now — e.g. no credentials).
var ErrNoUpstream = errors.New("provider has no upstream available")

// allowList builds an Allowed func from "METHOD /path" entries; a path ending
// in "/*" matches any single or nested suffix under that prefix.
func allowList(entries ...string) func(method, path string) bool {
	return func(method, path string) bool {
		for _, e := range entries {
			m, p, ok := strings.Cut(e, " ")
			if !ok || m != method {
				continue
			}
			if strings.HasSuffix(p, "/*") {
				if strings.HasPrefix(path, strings.TrimSuffix(p, "*")) {
					return true
				}
				continue
			}
			if path == p {
				return true
			}
		}
		return false
	}
}

// openAIStyleAllow is the allowlist of every OpenAI Chat Completions-dialect
// provider, relative to its BaseURL (which already carries any "/v1").
var openAIStyleAllow = allowList("POST /chat/completions", "GET /models")

// CleanProxyPath normalizes the path the proxy was asked for: it must be
// absolute, free of dot segments and encoded separators, so an allowlist
// match really is a match on what will be requested. Returns "" if unusable.
func CleanProxyPath(p string) string {
	if p == "" || p[0] != '/' || strings.ContainsAny(p, "\\\x00") {
		return ""
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return ""
		}
		dec, err := url.PathUnescape(seg)
		if err != nil || strings.ContainsAny(dec, "/\\") || dec == ".." || dec == "." {
			return ""
		}
		for _, r := range dec {
			if r < 0x20 || r == 0x7f {
				return "" // NUL and other control characters, however encoded
			}
		}
	}
	return p
}

// mergeBetaTokens unions comma-separated anthropic-beta lists, first-seen order.
func mergeBetaTokens(lists ...string) string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, t := range strings.Split(l, ",") {
			if t = strings.TrimSpace(t); t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return strings.Join(out, ",")
}

// ── reasoning_split injection (MiniMax-dialect backends) ─────────────────

// injectReasoningSplit is the proxy's equivalent of what a session does for a
// MiniMax-compatible backend: ask for "reasoning_split": true. Without it the
// backend emits its thinking INLINE, as literal <think>…</think> wrapped around
// the answer inside "content"; with it the thinking goes to the separate
// reasoning_content / reasoning_details fields and "content" is just the
// answer. A session always sends the flag (MiniMax.CompleteStream, custom
// providers declared with reasoning_split), but a proxy client forwards its own
// body and so keeps tripping over the inline tags.
//
// It only acts on a chat-completions JSON object that does not already say
// anything about reasoning_split — a caller that sends it (true OR false)
// decides — and edits the text rather than re-encoding the document: the flag is
// spliced in right after the opening brace, so every other byte the caller sent
// reaches the backend untouched. Anything that is not a JSON object is passed
// through as-is (the backend answers with its own error, not harness).
func injectReasoningSplit(path string, body []byte, _ map[string]string) ([]byte, error) {
	if path != "/chat/completions" {
		return body, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil || top == nil {
		return body, nil
	}
	if _, set := top["reasoning_split"]; set {
		return body, nil
	}
	open := bytes.IndexByte(body, '{') // the object's own brace: leading whitespace only precedes it
	rest := body[open+1:]
	sep := ","
	if bytes.HasPrefix(bytes.TrimLeft(rest, " \t\r\n"), []byte("}")) {
		sep = "" // empty object: no comma
	}
	out := make([]byte, 0, len(body)+len(`"reasoning_split":true,`))
	out = append(out, body[:open+1]...)
	out = append(out, `"reasoning_split":true`...)
	out = append(out, sep...)
	return append(out, rest...), nil
}

// ── Anthropic OAuth (Claude Code identity) rewrite ───────────────────────

// claudeCodeIdentity is the system block Anthropic's backend requires on a
// subscription (OAuth) request — see buildSystemBlocks.
const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

// rewriteClaudeOAuthBody turns a plain Anthropic Messages request into one the
// Claude Code subscription backend accepts, exactly as CompleteStream does for
// a session:
//
//   - the Claude Code identity block is prepended to "system" (absent, a
//     string, or an array of blocks);
//   - tool names are mapped to Claude Code's (tools[].name, tool_use blocks in
//     assistant messages, and tool_choice.name) — the SAME mapping a session
//     uses (mapToolNameToCC);
//   - the billing header is derived from the first user message and merged
//     into headers, as is the interleaved-thinking beta when applicable.
//
// Every other field passes through untouched, byte-for-byte where possible
// (the body is decoded to a map of raw JSON values, only the touched keys are
// re-encoded).
func rewriteClaudeOAuthBody(path string, body []byte, headers map[string]string) ([]byte, error) {
	if path != "/v1/messages" && path != "/v1/messages/count_tokens" {
		return body, nil // /v1/models etc.: nothing to rewrite
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("body is not a JSON object: %w", err)
	}

	// system: prepend the identity block, keeping the caller's own.
	identity := map[string]any{"type": "text", "text": claudeCodeIdentity, "cache_control": map[string]string{"type": "ephemeral"}}
	var blocks []any
	if raw, ok := top["system"]; ok && string(raw) != "null" {
		var asString string
		if err := json.Unmarshal(raw, &asString); err == nil {
			if asString != "" {
				blocks = []any{map[string]any{"type": "text", "text": asString}}
			}
		} else {
			var asBlocks []json.RawMessage
			if err := json.Unmarshal(raw, &asBlocks); err != nil {
				return nil, fmt.Errorf("system must be a string or an array of blocks: %w", err)
			}
			for _, b := range asBlocks {
				blocks = append(blocks, b)
			}
		}
	}
	top["system"] = mustJSON(append([]any{identity}, blocks...))

	// tools[].name and tool_choice.name → Claude Code names.
	if raw, ok := top["tools"]; ok {
		var tools []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &tools); err != nil {
			return nil, fmt.Errorf("tools must be an array of objects: %w", err)
		}
		for _, t := range tools {
			if n, ok := jsonString(t["name"]); ok {
				t["name"] = mustJSON(mapToolNameToCC(n))
			}
		}
		top["tools"] = mustJSON(tools)
	}
	if raw, ok := top["tool_choice"]; ok {
		var tc map[string]json.RawMessage
		if json.Unmarshal(raw, &tc) == nil {
			if n, ok := jsonString(tc["name"]); ok {
				tc["name"] = mustJSON(mapToolNameToCC(n))
				top["tool_choice"] = mustJSON(tc)
			}
		}
	}

	// messages: tool_use names inside assistant turns, and collect wire
	// messages for the billing header.
	var wire []json.RawMessage
	if raw, ok := top["messages"]; ok {
		var msgs []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return nil, fmt.Errorf("messages must be an array of objects: %w", err)
		}
		for _, m := range msgs {
			if content, ok := m["content"]; ok {
				var parts []map[string]json.RawMessage
				if json.Unmarshal(content, &parts) == nil {
					changed := false
					for _, p := range parts {
						if t, _ := jsonString(p["type"]); t == "tool_use" {
							if n, ok := jsonString(p["name"]); ok {
								p["name"] = mustJSON(mapToolNameToCC(n))
								changed = true
							}
						}
					}
					if changed {
						m["content"] = mustJSON(parts)
					}
				}
			}
			wire = append(wire, mustJSON(m))
		}
		top["messages"] = mustJSON(msgs)
	}

	if bh := buildBillingHeader(wire); bh != "" {
		headers["x-anthropic-billing-header"] = bh
	}
	if t, ok := top["thinking"]; ok {
		// Interleaved-thinking beta only for the legacy "enabled" shape, the
		// same condition CompleteStream applies (adaptive models carry
		// output_config instead).
		var th struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(t, &th) == nil && th.Type == "enabled" {
			headers["anthropic-beta"] += ",interleaved-thinking-2025-05-14"
		}
	}
	return marshalNoEscape(top)
}

func mustJSON(v any) json.RawMessage {
	b, err := marshalNoEscape(v)
	if err != nil {
		// Only re-encoding values just decoded from JSON, or built from
		// strings: cannot fail.
		panic(err)
	}
	return b
}

// marshalNoEscape encodes v as JSON WITHOUT altering text a caller sent: no
// HTML escaping (<, >, & stay as written) — and, unlike json.Marshal, it also
// keeps that promise for json.RawMessage values, which Marshal re-compacts
// AND re-escapes regardless of the encoder's setting (it runs them through
// its own compact(escape=true)). It walks maps, slices and raw messages itself
// and delegates only leaves (strings, numbers, bools, nil) to an unescaped
// encoder.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := encodeNoEscape(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeNoEscape(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case json.RawMessage:
		if len(t) == 0 {
			buf.WriteString("null")
			return nil
		}
		buf.Write(t) // already valid JSON, exactly as the caller wrote it
		return nil
	case map[string]json.RawMessage:
		return encodeObject(buf, len(t), func(emit func(string, any) error) error {
			for _, k := range sortedKeys(t) {
				if err := emit(k, t[k]); err != nil {
					return err
				}
			}
			return nil
		})
	case map[string]any:
		return encodeObject(buf, len(t), func(emit func(string, any) error) error {
			for _, k := range sortedKeys(t) {
				if err := emit(k, t[k]); err != nil {
					return err
				}
			}
			return nil
		})
	case map[string]string:
		return encodeObject(buf, len(t), func(emit func(string, any) error) error {
			for _, k := range sortedKeys(t) {
				if err := emit(k, t[k]); err != nil {
					return err
				}
			}
			return nil
		})
	case []map[string]json.RawMessage:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeNoEscape(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case []json.RawMessage:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeNoEscape(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeNoEscape(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	default: // string, number, bool, nil
		var leaf bytes.Buffer
		enc := json.NewEncoder(&leaf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return err
		}
		buf.Write(bytes.TrimRight(leaf.Bytes(), "\n"))
		return nil
	}
}

func encodeObject(buf *bytes.Buffer, n int, each func(emit func(string, any) error) error) error {
	buf.WriteByte('{')
	first := true
	err := each(func(k string, v any) error {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		if err := encodeNoEscape(buf, k); err != nil {
			return err
		}
		buf.WriteByte(':')
		return encodeNoEscape(buf, v)
	})
	buf.WriteByte('}')
	return err
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// unmapClaudeOAuthResponse is the inverse of the request rewrite: tool names in
// Claude Code's vocabulary (WebFetch, mcp__x__Foo) go back to the caller's
// (Fetch, Foo) — in a message's content[] and in a stream's content_block.
// Anything else, and any value that isn't a tool_use, is returned untouched.
func unmapClaudeOAuthResponse(v []byte) []byte {
	if !bytes.Contains(v, []byte(`"tool_use"`)) {
		return v // fast path: the vast majority of events (text deltas…)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(v, &top); err != nil {
		return v
	}
	changed := false
	fix := func(obj map[string]json.RawMessage) {
		if t, _ := jsonString(obj["type"]); t != "tool_use" {
			return
		}
		if n, ok := jsonString(obj["name"]); ok {
			if u := unmapToolNameFromCC(n); u != n {
				obj["name"] = mustJSON(u)
				changed = true
			}
		}
	}
	if raw, ok := top["content_block"]; ok { // stream: content_block_start
		var cb map[string]json.RawMessage
		if json.Unmarshal(raw, &cb) == nil {
			if fix(cb); changed {
				top["content_block"] = mustJSON(cb)
			}
		}
	}
	if raw, ok := top["content"]; ok { // non-stream message
		var parts []map[string]json.RawMessage
		if json.Unmarshal(raw, &parts) == nil {
			before := changed
			for _, p := range parts {
				fix(p)
			}
			if changed != before || changed {
				top["content"] = mustJSON(parts)
			}
		}
	}
	if !changed {
		return v
	}
	out, err := marshalNoEscape(top)
	if err != nil {
		return v
	}
	return out
}
