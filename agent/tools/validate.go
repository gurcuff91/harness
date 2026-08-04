package tools

import (
	"fmt"
	"reflect"
	"strings"
)

// requireFields walks a struct (or pointer to one) and reports every field
// tagged `validate:"required"` that is still at its Go zero-value ("", 0,
// false, nil slice/map/pointer) — the presence check every tool's own
// InputSchema already promises the model via its own "required" array, but a
// JSON Schema is advisory: nothing stops a model from omitting a required
// field anyway (or a caller from constructing the input struct directly).
// reflect.Value.IsZero() is stdlib-only and covers exactly the presence
// question every tool needs — deliberately NOT a general validation library:
// business rules (does this path exist? is old_text unique in the file? is
// this cron expression valid?) stay exactly where they already lived, one
// layer deeper (os.ReadFile, applyEdits, ValidateCron) — this only answers
// "is the input structurally complete".
//
// Field names in the returned error use each field's `json:"name,omitempty"`
// tag — what the model actually sent and sees in the schema — never the Go
// field name, which the model has no visibility into.
//
// NOTE on blank-vs-empty: IsZero() catches an ABSENT or empty-string field,
// not a string that is merely all-whitespace ("   "). A few tools
// (Subagent.prompt, ColleagueAsk.colleague/.prompt) additionally
// strings.TrimSpace() their own required fields for that reason — this
// helper covers "missing", not "meaningless"; callers needing the stronger
// guarantee keep their own TrimSpace check alongside the tag, not instead of
// it.
func requireFields(v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	rt := rv.Type()

	var missing []string
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if field.Tag.Get("validate") != "required" {
			continue
		}
		if rv.Field(i).IsZero() {
			missing = append(missing, jsonFieldName(field))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	plural := ""
	if len(missing) > 1 {
		plural = "s"
	}
	return fmt.Errorf("missing required field%s: %s", plural, strings.Join(missing, ", "))
}

// jsonFieldName extracts a struct field's `json:"name,omitempty"` tag name
// (stripping any comma-separated options), falling back to the Go field name
// if the field carries no json tag at all.
func jsonFieldName(field reflect.StructField) string {
	name := field.Tag.Get("json")
	if idx := strings.Index(name, ","); idx >= 0 {
		name = name[:idx]
	}
	if name == "" {
		return field.Name
	}
	return name
}
