// Command genrpc writes docs/page/content/reference/rpc-protocol.md from
// rpc.Schema(), the same schema the TS SDK's types are generated from, so the
// reference cannot drift from the code. Run it from the module root:
//
//	go run ./docs/page/genrpc
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gurcuff91/harness/rpc"
)

const outPath = "docs/page/content/reference/rpc-protocol.md"

type obj = map[string]any

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "genrpc:", err)
		os.Exit(1)
	}
}

func run() error {
	raw, err := rpc.Schema()
	if err != nil {
		return err
	}
	var doc obj
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("---\ntitle: RPC protocol\nsection: Reference\norder: 415\nsummary: The JSON-RPC protocol of harness rpc: every method with its parameters and result, the events, the notifications and the errors.\n---\n\n")
	b.WriteString("> **Note** This page is generated from the protocol schema (`rpc/schema.json`); it cannot drift from the code. Run `make docs-rpc` to regenerate it.\n\n")
	b.WriteString(intro(num(doc["protocol_version"])))

	defs, _ := doc["$defs"].(obj)

	methods, _ := doc["methods"].([]any)
	groups := map[string][]obj{}
	var order []string
	for _, m := range methods {
		mm := m.(obj)
		name := mm["method"].(string)
		g := name
		if i := strings.Index(name, "/"); i >= 0 {
			g = name[:i]
			if g == "admin" {
				rest := name[i+1:]
				if j := strings.Index(rest, "/"); j >= 0 {
					g = "admin/" + rest[:j]
				}
			}
		}
		if _, ok := groups[g]; !ok {
			order = append(order, g)
		}
		groups[g] = append(groups[g], mm)
	}
	sort.Strings(order)
	b.WriteString("## Methods\n\n")
	for _, g := range order {
		fmt.Fprintf(&b, "### %s\n\n", groupTitle(g))
		for _, m := range groups[g] {
			writeMethod(&b, m, defs)
		}
	}

	b.WriteString("## Notifications and reverse calls\n\n")
	notes, _ := doc["notifications"].([]any)
	for _, n := range notes {
		nn := n.(obj)
		fmt.Fprintf(&b, "### `%s`\n\n%s (%s)\n\n", nn["method"], nn["doc"], strings.ReplaceAll(fmt.Sprint(nn["direction"]), "_", " "))
		if p, ok := nn["params"].(obj); ok {
			writeFields(&b, "Params", p, defs)
		}
	}

	b.WriteString("## Events\n\nEach event arrives as an `event` notification: `session_id` plus the fields below.\n\n")
	evs, _ := doc["events"].([]any)
	for _, e := range evs {
		ee := e.(obj)
		fmt.Fprintf(&b, "### `%s`\n\n%s\n\n", ee["type"], ee["doc"])
		if p, ok := ee["payload"].(obj); ok {
			writeFields(&b, "Fields", p, defs)
		}
	}

	b.WriteString("## Errors\n\n| Code | Kind | Meaning |\n|---|---|---|\n")
	errs, _ := doc["errors"].([]any)
	for _, e := range errs {
		ee := e.(obj)
		fmt.Fprintf(&b, "| `%v` | `%v` | %v |\n", ee["code"], ee["kind"], oneLine(fmt.Sprint(ee["doc"])))
	}
	b.WriteString("\n")
	return os.WriteFile(outPath, []byte(b.String()), 0o644)
}

func intro(v int) string {
	return fmt.Sprintf(``+"`harness rpc`"+` speaks JSON-RPC 2.0 over stdin and stdout, one JSON message per line. It is the protocol behind the SDKs for other languages: the client describes the agent in `+"`initialize`"+`, then drives sessions and administration. Protocol version: **%d**.

## Conventions

- **Direction.** The client sends requests; the binary answers. The binary also sends the `+"`event`"+` and `+"`tool/cancel`"+` notifications and the `+"`tool/call`"+` request to the client.
- **Names.** `+"`initialize`"+`, then `+"`<domain>/<method>`"+`: `+"`agent/*`"+`, `+"`session/*`"+` and `+"`admin/<service>/*`"+`. Fields and compound names are `+"`snake_case`"+`, like the HTTP API.
- **Start.** `+"`initialize`"+` must come first. Its `+"`protocol_version`"+` must match; otherwise the binary answers with a `+"`protocol_version`"+` error and exits.
- **One client.** Closing stdin shuts the binary down: open sessions and the agent are closed.
- **Logs.** Anything that is not protocol goes to stderr; stdout carries only messages.
- **Errors.** A JSON-RPC error with the code and `+"`data.kind`"+` listed in [Errors](#errors).

`, v)
}

func groupTitle(g string) string {
	switch g {
	case "initialize":
		return "Initialize"
	case "agent":
		return "Agent"
	case "session":
		return "Session"
	case "tool":
		return "Tools (calls to the client)"
	}
	return strings.ReplaceAll(g, "/", " / ")
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func writeMethod(b *strings.Builder, m, defs obj) {
	fmt.Fprintf(b, "#### `%s`\n\n%s\n\n", m["method"], strings.TrimSpace(fmt.Sprint(m["doc"])))
	if p, ok := m["params"].(obj); ok {
		writeFields(b, "Params", p, defs)
	}
	if r, ok := m["result"].(obj); ok {
		writeFields(b, "Result", r, defs)
	}
}

// resolve follows a $ref to its definition.
func resolve(s, defs obj) (obj, string) {
	if ref, ok := s["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		if d, ok := defs[name].(obj); ok {
			return d, name
		}
	}
	return s, ""
}

func typeOf(s, defs obj) string {
	if ref, ok := s["$ref"].(string); ok {
		return "`" + strings.TrimPrefix(ref, "#/$defs/") + "`"
	}
	if c, ok := s["const"]; ok {
		return fmt.Sprintf("`%v`", c)
	}
	switch t := s["type"].(type) {
	case string:
		switch t {
		case "array":
			if it, ok := s["items"].(obj); ok {
				return "array of " + typeOf(it, defs)
			}
			return "array"
		case "object":
			if ap, ok := s["additionalProperties"].(obj); ok {
				return "map of " + typeOf(ap, defs)
			}
			return "object"
		}
		if f, ok := s["format"].(string); ok {
			return t + " (" + f + ")"
		}
		return t
	case []any:
		var parts []string
		for _, x := range t {
			parts = append(parts, fmt.Sprint(x))
		}
		return strings.Join(parts, " or ")
	}
	return "any"
}

// writeFields prints a schema as a field table; an empty object prints nothing.
func writeFields(b *strings.Builder, label string, s, defs obj) {
	d, name := resolve(s, defs)
	props, _ := d["properties"].(obj)
	if len(props) == 0 {
		if name == "" || name == "Empty" {
			fmt.Fprintf(b, "**%s:** none.\n\n", label)
			return
		}
		fmt.Fprintf(b, "**%s:** %s.\n\n", label, typeOf(s, defs))
		return
	}
	req := map[string]bool{}
	if rs, ok := d["required"].([]any); ok {
		for _, r := range rs {
			req[fmt.Sprint(r)] = true
		}
	}
	title := label
	if name != "" {
		title += " (`" + name + "`)"
	}
	fmt.Fprintf(b, "**%s:**\n\n| Field | Type | Description |\n|---|---|---|\n", title)
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p, _ := props[k].(obj)
		desc := oneLine(fmt.Sprint(p["description"]))
		if p["description"] == nil {
			desc = ""
		}
		opt := ""
		if !req[k] {
			opt = " (optional)"
		}
		fmt.Fprintf(b, "| `%s` | %s%s | %s |\n", k, typeOf(p, defs), opt, strings.ReplaceAll(desc, "|", "\\|"))
	}
	b.WriteString("\n")
}
