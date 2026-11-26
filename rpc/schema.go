package rpc

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Schema returns the JSON document describing the protocol: every method with
// its params and result as JSON Schema (2020-12 subset), the events and the
// errors. It is built by reflection over the Go types of the operation table,
// and is deterministic (methods sorted by name, object keys sorted), so the
// committed rpc/schema.json can be checked for staleness byte by byte.
// `go run ./rpc/gen` writes it.
func Schema() ([]byte, error) {
	ops := table()
	sort.Slice(ops, func(i, j int) bool { return ops[i].Method < ops[j].Method })

	type method struct {
		Method string         `json:"method"`
		Doc    string         `json:"doc"`
		Params map[string]any `json:"params"`
		Result map[string]any `json:"result"`
	}
	defs := map[string]any{}
	g := &schemaGen{defs: defs, names: map[reflect.Type]string{}, used: map[string]reflect.Type{}}
	methods := make([]method, 0, len(ops))
	for _, op := range ops {
		methods = append(methods, method{Method: op.Method, Doc: op.Doc, Params: g.schemaOf(op.Params), Result: g.schemaOf(op.Result)})
	}
	for _, c := range clientCalls {
		methods = append(methods, method{Method: c.Method, Doc: c.Doc, Params: g.schemaOf(c.Params), Result: g.schemaOf(c.Result)})
	}
	events := make([]map[string]any, 0, len(eventDocs))
	for _, e := range eventDocs {
		props := map[string]any{"type": map[string]any{"const": e.Type}, "session_id": map[string]any{"type": "string"}}
		required := []string{"type", "session_id"}
		for _, f := range e.Fields {
			props[f.Name] = f.Schema
			if !f.Optional {
				required = append(required, f.Name)
			}
		}
		sort.Strings(required)
		events = append(events, map[string]any{
			"type": e.Type, "doc": e.Doc,
			"payload": map[string]any{"type": "object", "properties": props, "required": required},
		})
	}
	errs := make([]map[string]any, 0, len(errorDocs))
	for _, e := range errorDocs {
		errs = append(errs, map[string]any{"kind": e.Kind, "code": e.Code, "doc": e.Doc})
	}
	doc := map[string]any{
		"$schema":          "https://json-schema.org/draft/2020-12/schema",
		"protocol_version": ProtocolVersion,
		"methods":          methods,
		"notifications":    notificationDocs(g),
		"events":           events,
		"errors":           errs,
		"$defs":            defs,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type schemaGen struct {
	defs  map[string]any
	names map[reflect.Type]string // named struct types already given a $defs entry
	used  map[string]reflect.Type // $defs name → type, to disambiguate clashes
}

var (
	timeType     = reflect.TypeFor[time.Time]()
	durationType = reflect.TypeFor[time.Duration]()
	rawType      = reflect.TypeFor[json.RawMessage]()
)

// schemaOf is the schema of t; a struct{} with no fields (Empty) is an empty
// object schema.
func (g *schemaGen) schemaOf(t reflect.Type) map[string]any {
	if t == nil {
		return map[string]any{}
	}
	switch {
	case t == timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case t == durationType:
		return map[string]any{"type": "integer", "description": "nanoseconds"}
	case t == rawType:
		return map[string]any{}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return g.schemaOf(t.Elem())
	case reflect.Interface:
		return map[string]any{}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": g.schemaOf(t.Elem())}
	case reflect.Array:
		return map[string]any{"type": "array", "items": g.schemaOf(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": g.schemaOf(t.Elem())}
	case reflect.Struct:
		return g.structRef(t)
	}
	return map[string]any{}
}

// structRef gives a struct its $defs entry (once; recursive types resolve
// through it) and returns a $ref to it. Anonymous structs are inlined.
func (g *schemaGen) structRef(t reflect.Type) map[string]any {
	if t.Name() == "" {
		return g.structSchema(t)
	}
	name, ok := g.names[t]
	if !ok {
		name = t.Name()
		if other, clash := g.used[name]; clash && other != t {
			name = pkgBase(t.PkgPath()) + "." + t.Name()
		}
		g.names[t] = name
		g.used[name] = t
		g.defs[name] = nil // reserve before recursing
		g.defs[name] = g.structSchema(t)
	}
	return map[string]any{"$ref": "#/$defs/" + name}
}

func pkgBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// structSchema describes a struct's JSON object: json tags name the keys,
// omitempty/omitzero mark a field optional, embedded structs without a tag are
// flattened, json:"-" fields and unexported ones are skipped.
func (g *schemaGen) structSchema(t reflect.Type) map[string]any {
	props := map[string]any{}
	var required []string
	g.collect(t, props, &required)
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		sort.Strings(required)
		out["required"] = required
	}
	return out
}

func (g *schemaGen) collect(t reflect.Type, props map[string]any, required *[]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			et := f.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				g.collect(et, props, required)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		props[name] = g.schemaOf(f.Type)
		optional := strings.Contains(","+opts+",", ",omitempty,") || strings.Contains(","+opts+",", ",omitzero,")
		if !optional {
			*required = append(*required, name)
		}
	}
}

// ── events, errors, notifications ────────────────────────────────────────

// EventField documents one field of an `event` payload.
type eventField struct {
	Name     string
	Optional bool
	Schema   map[string]any
}

type eventDoc struct {
	Type   string
	Doc    string
	Fields []eventField
}

func str(name string) eventField {
	return eventField{Name: name, Schema: map[string]any{"type": "string"}}
}
func num(name string) eventField {
	return eventField{Name: name, Schema: map[string]any{"type": "number"}}
}
func intf(name string) eventField {
	return eventField{Name: name, Schema: map[string]any{"type": "integer"}}
}
func boolf(name string) eventField {
	return eventField{Name: name, Schema: map[string]any{"type": "boolean"}}
}

// eventDocs lists every event types.WireEvent exposes, with its fields (those
// besides `type` and `session_id`). A test checks it against WireEvent for
// every event type.
var eventDocs = []eventDoc{
	{"turn_start", "A turn started.", nil},
	{"turn_end", "The turn finished; the session is ready for the next prompt.", nil},
	{"loop_start", "One ReAct iteration started.", []eventField{intf("loop")}},
	{"loop_end", "One ReAct iteration finished.", []eventField{intf("loop")}},
	{"thinking", "A fragment of the model's reasoning.", []eventField{str("delta")}},
	{"thinking_end", "The reasoning block finished.", nil},
	{"text", "A fragment of the response text.", []eventField{str("delta")}},
	{"text_end", "The response text finished.", nil},
	{"tool_start", "A tool call was announced (name and id known, arguments not yet).", []eventField{str("tool_name"), str("tool_id")}},
	{"tool_args", "A fragment of a tool call's arguments.", []eventField{str("tool_name"), str("tool_id"), str("delta")}},
	{"tool_call", "A tool call's arguments are complete; it is about to run.", []eventField{str("tool_name"), str("tool_id"), str("tool_args")}},
	{"tool_result", "A tool finished. duration is in milliseconds.", []eventField{str("tool_name"), str("tool_id"), str("output"), num("duration"), boolf("is_error")}},
	{"received_prompt", "An immediate prompt was received (also the echo of prompts from other origins).", promptEchoFields()},
	{"follow_up_start", "A queued prompt is about to be processed.", promptEchoFields()},
	{"retry", "A transient network failure is being retried.", []eventField{intf("attempt"), intf("max"), str("error")}},
	{"tokens", "Token usage: live context (input, context_usage, context_window) and session totals.", []eventField{
		intf("input"), num("context_usage"), intf("context_window"), intf("total_input"), intf("total_output"),
		intf("cache_read"), intf("cache_write"), num("cost_usd")}},
	{"error", "Something broke in the turn.", []eventField{str("message"), {Name: "details", Optional: true, Schema: map[string]any{"type": "object"}}}},
	{"max_iterations_reached", "The iteration budget ran out (the model summarized its progress).", []eventField{intf("max_iterations")}},
	{"compact_start", "Compaction started.", nil},
	{"compact_end", "Compaction finished.", []eventField{str("summary")}},
	{"stop", "The turn was stopped by the user (not an error).", nil},
}

func promptEchoFields() []eventField {
	return []eventField{str("text"), str("origin"), intf("message_index"),
		{Name: "images", Optional: true, Schema: map[string]any{"type": "array", "items": map[string]any{}}}}
}

type errorDoc struct {
	Kind string
	Code int
	Doc  string
}

// errorDocs lists the error kinds with their codes (the table of errors.go).
var errorDocs = []errorDoc{
	{"not_found", CodeNotFound, "The thing named does not exist (a session, a memory, a file)."},
	{"provider_not_found", CodeNotFound, "No provider with that name."},
	{"invalid", CodeInvalid, "Well-formed input the operation cannot accept."},
	{"unprocessable", CodeInvalid, "Well-formed input whose content is invalid (a bad thinking level, an invalid MCP server)."},
	{"invalid_export", CodeInvalid, "The session export is not valid; the message names the line."},
	{"session_active", CodeConflict, "The session is open in this process; close it first."},
	{"conflict", CodeConflict, "The request clashes with the current state (a channel already taken, a busy session)."},
	{"provider_not_removable", CodeConflict, "The provider is built in or declared in settings and cannot be unregistered."},
	{"provider_name_conflict", CodeConflict, "A custom provider name is already used by a provider registered from code."},
	{"not_connected", CodeNotConnected, "The provider has no credentials, or no model is available to run on."},
	{"upstream", CodeUpstream, "A provider or its OAuth endpoint failed."},
	{"not_configured", CodeNotConfigured, "The feature has no backing store here (e.g. no memory)."},
	{"protocol_version", CodeProtocolVersion, "initialize carried an unsupported protocol_version; data.supported lists the ones accepted. The process exits."},
	{"internal", -32603, "An unexpected failure."},
}

// clientCall documents a request the BINARY sends to the client.
type clientCall struct {
	Method         string
	Doc            string
	Params, Result reflect.Type
}

// clientCalls are the requests the binary sends (the client implements them).
var clientCalls = []clientCall{
	{"tool/call", "Sent BY THE BINARY to the client when the model calls a declared tool. Answer with the tool's result.", reflect.TypeFor[ToolCallParams](), reflect.TypeFor[ToolCallResult]()},
}

func notificationDocs(g *schemaGen) []map[string]any {
	return []map[string]any{
		{"method": "event", "direction": "binary_to_client", "doc": "A session event: {session_id, type, …} (see events).", "params": map[string]any{"type": "object", "description": "the payload of one of the events listed in events, plus session_id"}},
		{"method": "tool/cancel", "direction": "binary_to_client", "doc": "A pending tool/call was abandoned (session stop, shutdown); a late answer is ignored.", "params": g.schemaOf(reflect.TypeFor[ToolCancelParams]())},
	}
}
