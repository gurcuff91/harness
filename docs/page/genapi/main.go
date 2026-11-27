// Command genapi writes docs/page/content/reference/http-api.md from the
// OpenAPI document the server itself serves, so the page cannot drift from
// the code. Run it from the module root:
//
//	go run ./docs/page/genapi
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
	"github.com/gurcuff91/harness/server"
)

const outPath = "docs/page/content/reference/http-api.md"

type obj = map[string]any

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "genapi:", err)
		os.Exit(1)
	}
}

func run() error {
	a := agent.New(agent.AgentOptions{
		Store:          store.NewInMemoryStore(),
		ResourceLoader: resources.Nil(),
	})
	srv, err := server.Start(a, "127.0.0.1:0", server.ServerOptions{})
	if err != nil {
		return err
	}
	defer srv.Close() //nolint:errcheck

	resp, err := http.Get("http://" + srv.Addr() + "/api/docs/openapi.json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("openapi.json: HTTP %d", resp.StatusCode)
	}
	var spec obj
	if err := json.Unmarshal(raw, &spec); err != nil {
		return err
	}
	md := render(spec)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outPath, []byte(md), 0o644)
}

const intro = `---
title: HTTP API
section: Reference
order: 410
summary: Every endpoint of the Harness HTTP/SSE server, generated from its OpenAPI document.
---
This page is generated from the OpenAPI document the server serves at ` + "`/api/docs/openapi.json`" + `. An interactive version is served at ` + "`/api/docs`" + `. Regenerate it with ` + "`make docs-api`" + `.

## Conventions

- **Base URL.** ` + "`harness serve <addr>`" + ` listens on the address you give it; the in-process servers of the TUI and ACP use a random localhost port. Every path below starts with ` + "`/api`" + `.
- **Authentication.** A server started with a secret (` + "`harness serve --secret`" + `, ` + "`HARNESS_SECRET`" + `, or ` + "`server.WithSecret`" + `) requires it on every request except ` + "`GET /api/server`" + ` and the API docs. Send it in the ` + "`X-Harness-Sec`" + ` header or as ` + "`Authorization: Bearer <secret>`" + `. A missing or wrong secret is answered with 401. Without a secret the server has no authentication.
- **Errors.** Error responses are JSON: ` + "`{\"error\": {\"message\": \"...\", \"details\": {...}}}`" + `. ` + "`details`" + ` is optional.
- **Events.** A session's events are delivered as Server-Sent Events (` + "`text/event-stream`" + `): each event is a ` + "`data: <json>`" + ` line followed by a blank line, and the JSON has a ` + "`type`" + ` field (` + "`turn_start`, `text`, `thinking`, `tool_call`, `tool_result`, `tokens`, `error`, `turn_end`" + `, and so on). See the events endpoint below.

`

func render(spec obj) string {
	var b strings.Builder
	b.WriteString(intro)

	comps := obj{}
	if c, ok := spec["components"].(obj); ok {
		if s, ok := c["schemas"].(obj); ok {
			comps = s
		}
	}

	type op struct {
		method, path string
		o            obj
	}
	byTag := map[string][]op{}
	paths, _ := spec["paths"].(obj)
	for p, item := range paths {
		im, _ := item.(obj)
		for m, o := range im {
			om, ok := o.(obj)
			if !ok || !isMethod(m) {
				continue
			}
			tag := "other"
			if ts, ok := om["tags"].([]any); ok && len(ts) > 0 {
				tag, _ = ts[0].(string)
			}
			byTag[tag] = append(byTag[tag], op{strings.ToUpper(m), p, om})
		}
	}

	// Tag order: as declared in the spec, then any undeclared ones sorted.
	var tags []string
	desc := map[string]string{}
	seen := map[string]bool{}
	if ts, ok := spec["tags"].([]any); ok {
		for _, t := range ts {
			tm, _ := t.(obj)
			n, _ := tm["name"].(string)
			d, _ := tm["description"].(string)
			desc[n] = d
			if len(byTag[n]) > 0 {
				tags = append(tags, n)
				seen[n] = true
			}
		}
	}
	var rest []string
	for t := range byTag {
		if !seen[t] {
			rest = append(rest, t)
		}
	}
	sort.Strings(rest)
	tags = append(tags, rest...)

	for _, tag := range tags {
		ops := byTag[tag]
		sort.Slice(ops, func(i, j int) bool {
			if ops[i].path != ops[j].path {
				return ops[i].path < ops[j].path
			}
			return methodRank(ops[i].method) < methodRank(ops[j].method)
		})
		fmt.Fprintf(&b, "## %s\n\n", strings.ToUpper(tag[:1])+tag[1:])
		if d := desc[tag]; d != "" {
			fmt.Fprintf(&b, "%s.\n\n", strings.TrimSuffix(clean(d), "."))
		}
		for _, o := range ops {
			fmt.Fprintf(&b, "### `%s %s`\n\n", o.method, o.path)
			if s, _ := o.o["summary"].(string); s != "" {
				fmt.Fprintf(&b, "**%s**\n\n", clean(s))
			}
			if d, _ := o.o["description"].(string); d != "" {
				fmt.Fprintf(&b, "%s\n\n", clean(d))
			}
			if sec, ok := o.o["security"].([]any); ok && len(sec) == 0 {
				b.WriteString("No secret required.\n\n")
			}
			renderParams(&b, o.o, comps)
			renderBody(&b, o.o, comps)
			renderResponses(&b, o.o, comps)
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func isMethod(m string) bool {
	switch m {
	case "get", "post", "put", "patch", "delete", "head", "options":
		return true
	}
	return false
}

func methodRank(m string) int {
	for i, x := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		if x == m {
			return i
		}
	}
	return 9
}

// clean flattens text to a single line of Markdown-safe prose.
func clean(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// cell makes text safe inside a table cell.
func cell(s string) string {
	s = clean(s)
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

func renderParams(b *strings.Builder, o obj, comps obj) {
	ps, _ := o["parameters"].([]any)
	if len(ps) == 0 {
		return
	}
	b.WriteString("Parameters:\n\n| Name | In | Type | Required | Description |\n|---|---|---|---|---|\n")
	for _, p := range ps {
		pm, _ := p.(obj)
		pm = resolve(pm, comps)
		name, _ := pm["name"].(string)
		in, _ := pm["in"].(string)
		req, _ := pm["required"].(bool)
		sch, _ := pm["schema"].(obj)
		d, _ := pm["description"].(string)
		d += enumNote(resolve(sch, comps))
		fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s |\n", name, in, typeOf(sch), yesNo(req), cell(d))
	}
	b.WriteString("\n")
}

func renderBody(b *strings.Builder, o obj, comps obj) {
	rb, _ := o["requestBody"].(obj)
	if rb == nil {
		return
	}
	content, _ := rb["content"].(obj)
	b.WriteString("Request body")
	if r, _ := rb["required"].(bool); r {
		b.WriteString(" (required)")
	}
	b.WriteString(":\n\n")
	for _, ct := range sortedKeys(content) {
		cm, _ := content[ct].(obj)
		sch, _ := cm["schema"].(obj)
		fmt.Fprintf(b, "Content type: `%s`.\n\n", ct)
		renderSchema(b, sch, comps)
	}
	if len(content) == 0 {
		b.WriteString("No fields documented.\n\n")
	}
}

func renderResponses(b *strings.Builder, o obj, comps obj) {
	rs, _ := o["responses"].(obj)
	if len(rs) == 0 {
		return
	}
	b.WriteString("Responses:\n\n| Status | Description | Body |\n|---|---|---|\n")
	for _, code := range sortedKeys(rs) {
		rm, _ := rs[code].(obj)
		rm = resolve(rm, comps)
		d, _ := rm["description"].(string)
		body := "-"
		if content, ok := rm["content"].(obj); ok && len(content) > 0 {
			var parts []string
			for _, ct := range sortedKeys(content) {
				cm, _ := content[ct].(obj)
				sch, _ := cm["schema"].(obj)
				parts = append(parts, fmt.Sprintf("`%s` %s", ct, typeOf(sch)))
			}
			body = strings.Join(parts, "; ")
		}
		fmt.Fprintf(b, "| %s | %s | %s |\n", code, cell(d), body)
	}
	b.WriteString("\n")
	// Field tables for the first JSON success body that has properties.
	for _, code := range sortedKeys(rs) {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		rm, _ := rs[code].(obj)
		rm = resolve(rm, comps)
		content, _ := rm["content"].(obj)
		cm, _ := content["application/json"].(obj)
		sch, _ := cm["schema"].(obj)
		if sch == nil {
			continue
		}
		if flat := fields(sch, comps); len(flat) > 0 {
			fmt.Fprintf(b, "Response %s fields:\n\n", code)
			writeFields(b, flat)
		}
	}
}

func renderSchema(b *strings.Builder, sch obj, comps obj) {
	flat := fields(sch, comps)
	if len(flat) == 0 {
		fmt.Fprintf(b, "Type: %s.\n\n", typeOf(sch))
		return
	}
	writeFields(b, flat)
}

type field struct{ name, typ, req, desc string }

func writeFields(b *strings.Builder, fs []field) {
	b.WriteString("| Field | Type | Required | Description |\n|---|---|---|---|\n")
	for _, f := range fs {
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n", f.name, f.typ, f.req, cell(f.desc))
	}
	b.WriteString("\n")
}

// fields flattens the properties of an object schema (after resolving $ref one
// level; an array's item schema is described the same way).
func fields(sch obj, comps obj) []field {
	sch = resolve(sch, comps)
	if sch == nil {
		return nil
	}
	if t, _ := sch["type"].(string); t == "array" {
		items, _ := sch["items"].(obj)
		return fields(items, comps)
	}
	props, _ := sch["properties"].(obj)
	required := map[string]bool{}
	if rq, ok := sch["required"].([]any); ok {
		for _, r := range rq {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	var out []field
	for _, n := range sortedKeys(props) {
		pm, _ := props[n].(obj)
		d, _ := resolve(pm, comps)["description"].(string)
		d += enumNote(resolve(pm, comps))
		out = append(out, field{n, typeOf(pm), yesNo(required[n]), d})
	}
	return out
}

func resolve(s obj, comps obj) obj {
	if s == nil {
		return nil
	}
	if ref, ok := s["$ref"].(string); ok {
		name := ref[strings.LastIndex(ref, "/")+1:]
		if c, ok := comps[name].(obj); ok {
			return c
		}
	}
	return s
}

func typeOf(s obj) string {
	if s == nil {
		return "-"
	}
	if ref, ok := s["$ref"].(string); ok {
		return "`" + ref[strings.LastIndex(ref, "/")+1:] + "`"
	}
	t, _ := s["type"].(string)
	switch t {
	case "array":
		items, _ := s["items"].(obj)
		return "array of " + typeOf(items)
	case "":
		return "object"
	}
	if f, _ := s["format"].(string); f != "" {
		return t + " (" + f + ")"
	}
	return t
}

func enumNote(s obj) string {
	en, ok := s["enum"].([]any)
	if !ok || len(en) == 0 {
		return ""
	}
	var vs []string
	for _, e := range en {
		vs = append(vs, fmt.Sprintf("`%v`", e))
	}
	return " One of: " + strings.Join(vs, ", ") + "."
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func sortedKeys(m obj) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
