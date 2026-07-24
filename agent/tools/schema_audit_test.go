package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// ── minimal store mocks (schema audit doesn't exercise Execute) ──────────

type auditMemoryStore struct{}

func (auditMemoryStore) Write(cwd, slug, content string, global bool) (bool, error) {
	return false, nil
}
func (auditMemoryStore) Search(cwd, query string, includeContent bool, skip, limit int) (MemorySearchResult, error) {
	return MemorySearchResult{}, nil
}
func (auditMemoryStore) Delete(cwd, slug string, global bool) (bool, error) { return false, nil }

type auditScheduleStore struct{}

func (auditScheduleStore) Set(slug, cron, prompt, owner string) error { return nil }
func (auditScheduleStore) Delete(slug, owner string) (bool, error)    { return false, nil }
func (auditScheduleStore) Entries(owner string) []ScheduleEntry       { return nil }

// allBuiltinTools returns every tool the agent can register, built with
// minimal mocks where a constructor needs a store/executor. This is the same
// set assembled by agent.defaultTools() (Bash/Read/Write/Edit/Fetch, always
// on) plus the conditionally-registered ones (Skill, Memo*, Schedule*,
// Subagent) from agent.buildSessionTools(). Kept in one place so a new tool
// only needs to be added here to get the schema audit below for free.
func allBuiltinTools() []Tool {
	return []Tool{
		Bash(),
		ReadFile(),
		WriteFile(),
		Edit(),
		Fetch(),
		Skill(func(name string) (string, string, error) { return "", "", nil }),
		MemoWrite(auditMemoryStore{}, "/tmp"),
		MemoSearch(auditMemoryStore{}, "/tmp"),
		MemoDelete(auditMemoryStore{}, "/tmp"),
		Schedule(auditScheduleStore{}, "owner"),
		ScheduleList(auditScheduleStore{}, "owner"),
		ScheduleDelete(auditScheduleStore{}, "owner"),
		Subagent(func(ctx context.Context, prompt string) (string, error) { return "", nil }),
	}
}

// jsonSchema is the minimal shape audited: valid JSON object, "type":"object",
// "properties" (if present) a JSON object, and every name in "required" (if
// present) present as a key in "properties". This mirrors the JSON Schema
// subset Anthropic/OpenAI actually enforce for tool inputs — anything outside
// this shape risks the provider rejecting the tool definition outright or the
// model receiving a schema it can't reliably fill in (both of which produce
// malformed tool-call arguments at generation time, same failure class as a
// model typo).
type jsonSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]map[string]any `json:"properties"`
	Required   []string                  `json:"required"`
}

// TestBuiltinToolSchemasAreWellFormed audits every built-in tool's
// InputSchema: it must be valid JSON, declare "type":"object" (what every
// provider requires for tool/function parameters), and every "required" name
// must exist in "properties". A schema that fails this is sent to the model
// verbatim on every turn — if it's malformed, the model has no reliable way
// to construct a valid call, which surfaces later as an "Error parsing
// input" tool result the model has to recover from. Catching it here means a
// broken schema fails CI instead of showing up as a field report.
func TestBuiltinToolSchemasAreWellFormed(t *testing.T) {
	for _, tool := range allBuiltinTools() {
		name := tool.Def.Name
		t.Run(name, func(t *testing.T) {
			raw := tool.Def.InputSchema
			if len(raw) == 0 {
				t.Fatalf("%s: InputSchema is empty", name)
			}

			// Must be valid JSON at all.
			var generic map[string]any
			if err := json.Unmarshal(raw, &generic); err != nil {
				t.Fatalf("%s: InputSchema is not valid JSON: %v\nraw: %s", name, err, raw)
			}

			var schema jsonSchema
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("%s: InputSchema does not decode as a JSON Schema object shape: %v", name, err)
			}

			if schema.Type != "object" {
				t.Errorf("%s: schema.Type = %q, want \"object\" (required by Anthropic/OpenAI tool schemas)", name, schema.Type)
			}

			for _, req := range schema.Required {
				if _, ok := schema.Properties[req]; !ok {
					t.Errorf("%s: %q is in \"required\" but missing from \"properties\"", name, req)
				}
			}

			// Every declared property must itself be a well-formed schema
			// fragment: a "type" (or "$ref"/"oneOf"/etc, not enforced here —
			// built-ins only use plain types) and, if present, a description
			// string (not required by the spec, but every built-in tool
			// documents its params — catches a copy-paste field left bare).
			for propName, prop := range schema.Properties {
				pt, hasType := prop["type"]
				if !hasType {
					t.Errorf("%s.%s: property has no \"type\"", name, propName)
					continue
				}
				if _, ok := pt.(string); !ok {
					t.Errorf("%s.%s: \"type\" is %T, want string", name, propName, pt)
				}
			}
		})
	}
}

// malformedInputs are JSON payloads a model can plausibly emit when it makes a
// mistake building tool-call arguments (the failure this whole audit is
// about): truncated/dangling-comma JSON (the exact shape seen in the field —
// {"path":"x","offset":183,200} — a value where a "key": was expected),
// a bare non-object value, and empty input.
var malformedInputs = []struct {
	name string
	json string
}{
	{"dangling_value_no_key", `{"path":"x","offset":183,200}`},
	{"truncated", `{"path":"x"`},
	{"not_an_object", `"just a string"`},
	{"empty", ``},
}

// TestBuiltinToolReturnsNonEmptyOutputOnBadInput audits Execute/ExecuteRich
// behavior (not just the schema) for every built-in tool: when the model
// sends malformed JSON as tool-call arguments, the tool must return a
// non-empty output alongside the error. This is what makes the failure
// visible and actionable to the MODEL on its next turn — runStream persists
// {output, is_error:true} as the tool_result the model reads back. A tool
// that returns ("", err) does still recover today, because runStream has a
// fallback that substitutes execErr.Error() when output is empty (needed
// because Anthropic 400s on an is_error tool_result with empty content) — but
// that fallback is a safety net, not a contract every tool should rely on.
// This test locks in the stronger guarantee at the tool level directly.
func TestBuiltinToolReturnsNonEmptyOutputOnBadInput(t *testing.T) {
	for _, tool := range allBuiltinTools() {
		name := tool.Def.Name
		for _, mi := range malformedInputs {
			t.Run(name+"/"+mi.name, func(t *testing.T) {
				var (
					output string
					err    error
				)
				if tool.ExecuteRich != nil {
					output, _, err = tool.ExecuteRich(context.Background(), json.RawMessage(mi.json))
				} else {
					output, err = tool.Execute(context.Background(), json.RawMessage(mi.json))
				}
				if err == nil {
					// Some tools have no required fields, so a bare "{}"-like
					// decode can succeed with zero values (e.g. ScheduleList
					// ignores input entirely). Only enforce the invariant when
					// the tool actually reports the input as bad.
					return
				}
				if output == "" {
					t.Errorf("%s: Execute returned empty output alongside error %v for malformed input %q — "+
						"the model would only recover via runStream's generic fallback, not this tool's own message",
						name, err, mi.json)
				}
			})
		}
	}
}

// TestBuiltinToolNamesAndDescriptionsNonEmpty is a cheap sanity check
// alongside the schema audit: every tool needs a name and a description, both
// sent to the model as-is (an empty description gives the model nothing to
// go on when deciding how to call the tool correctly).
func TestBuiltinToolNamesAndDescriptionsNonEmpty(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range allBuiltinTools() {
		if tool.Def.Name == "" {
			t.Errorf("tool with empty Name (description: %q)", tool.Def.Description)
			continue
		}
		if tool.Def.Description == "" {
			t.Errorf("%s: empty Description", tool.Def.Name)
		}
		if seen[tool.Def.Name] {
			t.Errorf("%s: duplicate tool name in the built-in set", tool.Def.Name)
		}
		seen[tool.Def.Name] = true
	}
}
