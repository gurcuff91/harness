package acp

import (
	"encoding/json"
	"testing"
)

// TestSessionConfigOptionMarshalsCurrentValueNotValue is the regression test
// for the bug where this struct serialized its selected-value field as
// "value" instead of ACP's actual wire name "currentValue" — Zed (and
// presumably any spec-compliant client) silently declines to render a
// selector for a ConfigOption missing a recognized currentValue, so this
// never errored, it just quietly failed to show up in the UI.
func TestSessionConfigOptionMarshalsCurrentValueNotValue(t *testing.T) {
	opt := sessionConfigOption{
		ID:           "model",
		Category:     "model",
		Name:         "Model",
		Type:         "select",
		CurrentValue: "claude-oauth/claude-sonnet-5",
		Options: []sessionConfigOptionValue{
			{Value: "claude-oauth/claude-sonnet-5", Name: "claude-oauth/claude-sonnet-5"},
		},
	}
	b, err := json.Marshal(opt)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["value"]; ok {
		t.Error(`wire object must NOT have a top-level "value" key — ACP's ConfigOption has no such field`)
	}
	var got string
	if err := json.Unmarshal(m["currentValue"], &got); err != nil {
		t.Fatalf(`missing or non-string "currentValue" key: %s`, b)
	}
	if got != "claude-oauth/claude-sonnet-5" {
		t.Errorf("currentValue = %q", got)
	}
}

func TestSessionConfigOptionValueStillUsesValueKey(t *testing.T) {
	// Sanity check the OTHER type (ConfigOptionValue, one entry in Options[])
	// legitimately keeps its own "value" key — this is a different field on a
	// different type, not a contradiction of the test above.
	v := sessionConfigOptionValue{Value: "off", Name: "Off"}
	b, _ := json.Marshal(v)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["value"]; !ok {
		t.Errorf(`ConfigOptionValue must marshal a "value" key, got %s`, b)
	}
}
