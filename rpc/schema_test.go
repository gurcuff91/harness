package rpc

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/gurcuff91/harness/types"
)

func TestSchemaFileIsUpToDate(t *testing.T) {
	want, err := Schema()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("schema.json")
	if err != nil {
		t.Fatalf("rpc/schema.json is missing: run `go run ./rpc/gen`")
	}
	if !bytes.Equal(got, want) {
		t.Fatal("rpc/schema.json is stale: run `go run ./rpc/gen`")
	}
}

func TestSchemaIsDeterministicAndValidJSON(t *testing.T) {
	a, _ := Schema()
	b, _ := Schema()
	if !bytes.Equal(a, b) {
		t.Fatal("Schema() is not deterministic")
	}
	var doc struct {
		ProtocolVersion int `json:"protocol_version"`
		Methods         []struct {
			Method string         `json:"method"`
			Doc    string         `json:"doc"`
			Params map[string]any `json:"params"`
			Result map[string]any `json:"result"`
		} `json:"methods"`
		Events []struct{ Type string } `json:"events"`
		Errors []struct{ Kind string } `json:"errors"`
		Defs   map[string]any          `json:"$defs"`
	}
	if err := json.Unmarshal(a, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ProtocolVersion != ProtocolVersion || len(doc.Methods) == 0 || len(doc.Events) == 0 || len(doc.Errors) == 0 {
		t.Fatalf("incomplete document")
	}
	have := map[string]bool{}
	for _, m := range doc.Methods {
		have[m.Method] = true
		if m.Doc == "" {
			t.Errorf("%s has no doc", m.Method)
		}
	}
	for _, m := range []string{"initialize", "session/new", "session/close", "agent/shutdown", "tool/call"} {
		if !have[m] {
			t.Errorf("method %s missing from the schema", m)
		}
	}
	// every $ref resolves
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if r, ok := x["$ref"].(string); ok {
				name := r[len("#/$defs/"):]
				if d, ok := doc.Defs[name]; !ok || d == nil {
					t.Errorf("dangling $ref %s", r)
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	var whole any
	json.Unmarshal(a, &whole)
	walk(whole)
}

// The table is a set: no duplicate methods, every op complete.
func TestTableIsWellFormed(t *testing.T) {
	idx := opIndex() // panics on a duplicate
	for m, op := range idx {
		if op.Doc == "" || op.Params == nil || op.Result == nil {
			t.Errorf("%s: incomplete op", m)
		}
	}
}

// Every event type types.WireEvent exposes has an entry in eventDocs, and
// every field WireEvent puts in a payload is documented there.
func TestEveryWireEventIsDocumented(t *testing.T) {
	docs := map[string]eventDoc{}
	for _, d := range eventDocs {
		docs[d.Type] = d
	}
	seen := map[string]bool{}
	for et := types.EventTurnStart; et <= types.EventRetry; et++ {
		e := types.Event{Type: et, Output: "o", Details: map[string]any{"a": 1}, Images: []types.ImageRef{{}}}
		p, ok := types.WireEvent(e)
		if !ok {
			continue
		}
		typ := p["type"].(string)
		seen[typ] = true
		d, ok := docs[typ]
		if !ok {
			t.Errorf("event %q is exposed by WireEvent but missing from eventDocs", typ)
			continue
		}
		fields := map[string]bool{}
		for _, f := range d.Fields {
			fields[f.Name] = true
		}
		for k := range p {
			if k != "type" && !fields[k] {
				t.Errorf("event %q: field %q is not documented", typ, k)
			}
		}
		for _, f := range d.Fields {
			if _, has := p[f.Name]; !has && !f.Optional {
				t.Errorf("event %q documents %q but WireEvent does not send it", typ, f.Name)
			}
		}
	}
	for typ := range docs {
		if !seen[typ] {
			t.Errorf("eventDocs lists %q, which WireEvent does not produce", typ)
		}
	}
}

func TestErrorDocsMatchTheCodeTable(t *testing.T) {
	for _, e := range errorDocs {
		if code, ok := kindCodes[e.Kind]; !ok || code != e.Code {
			t.Errorf("error kind %q: doc says %d, table says %d (%v)", e.Kind, e.Code, code, ok)
		}
	}
	if len(errorDocs) != len(kindCodes) {
		t.Errorf("errorDocs has %d kinds, kindCodes %d", len(errorDocs), len(kindCodes))
	}
}

// ── generator unit tests ─────────────────────────────────────────────────

type embedded struct {
	Inner int `json:"inner"`
}

type node struct {
	Name     string  `json:"name"`
	Children []*node `json:"children,omitempty"`
}

type shapes struct {
	embedded
	Required   string    `json:"required"`
	Optional   string    `json:"optional,omitempty"`
	Zero       time.Time `json:"zero,omitzero"`
	Skipped    string    `json:"-"`
	unexported string
	Ptr        *int              `json:"ptr"`
	When       time.Time         `json:"when"`
	Dur        time.Duration     `json:"dur"`
	Raw        json.RawMessage   `json:"raw"`
	Any        any               `json:"any"`
	Bytes      []byte            `json:"bytes"`
	Map        map[string]string `json:"map"`
	List       []float64         `json:"list"`
	Tree       node              `json:"tree"`
}

func TestSchemaGeneratorShapes(t *testing.T) {
	g := &schemaGen{defs: map[string]any{}, names: map[reflect.Type]string{}, used: map[string]reflect.Type{}}
	ref := g.schemaOf(reflect.TypeFor[shapes]())
	if ref["$ref"] != "#/$defs/shapes" {
		t.Fatalf("ref = %v", ref)
	}
	s := g.defs["shapes"].(map[string]any)
	props := s["properties"].(map[string]any)
	for _, k := range []string{"inner", "required", "optional", "zero", "ptr", "when", "dur", "raw", "any", "bytes", "map", "list", "tree"} {
		if _, ok := props[k]; !ok {
			t.Errorf("property %q missing", k)
		}
	}
	for _, k := range []string{"Skipped", "unexported", "embedded", "-"} {
		if _, ok := props[k]; ok {
			t.Errorf("property %q must not be there", k)
		}
	}
	req := map[string]bool{}
	for _, r := range s["required"].([]string) {
		req[r] = true
	}
	if !req["required"] || !req["inner"] || req["optional"] || req["zero"] {
		t.Errorf("required = %v", req)
	}
	if props["when"].(map[string]any)["format"] != "date-time" || props["dur"].(map[string]any)["type"] != "integer" {
		t.Error("time/duration mapping")
	}
	if len(props["raw"].(map[string]any)) != 0 || len(props["any"].(map[string]any)) != 0 {
		t.Error("RawMessage and any must be unconstrained")
	}
	if props["bytes"].(map[string]any)["type"] != "string" {
		t.Error("[]byte must be a string")
	}
	// recursion resolves through $defs
	n := g.defs["node"].(map[string]any)["properties"].(map[string]any)["children"].(map[string]any)
	if n["items"].(map[string]any)["$ref"] != "#/$defs/node" {
		t.Errorf("recursive type = %v", n)
	}
}
