package tools

import "testing"

type validateProbeInput struct {
	Required1 string `json:"required1" validate:"required"`
	Required2 int    `json:"required2,omitempty" validate:"required"`
	Optional  string `json:"optional,omitempty"`
	Untagged  bool   `json:"untagged,omitempty"`
}

// TestRequireFieldsDetectsMissing verifies every zero-value permutation of
// the tagged fields is caught, with the JSON name (not the Go field name) in
// the error message.
func TestRequireFieldsDetectsMissing(t *testing.T) {
	cases := []struct {
		name    string
		input   validateProbeInput
		wantErr bool
		wantMsg string
	}{
		{"all present", validateProbeInput{Required1: "x", Required2: 1}, false, ""},
		{"missing string", validateProbeInput{Required2: 1}, true, "required1"},
		{"missing int (zero)", validateProbeInput{Required1: "x", Required2: 0}, true, "required2"},
		{"missing both", validateProbeInput{}, true, "required1, required2"},
		// Optional/untagged fields being empty must NEVER trigger an error —
		// only fields explicitly tagged validate:"required" are checked.
		{"optional and untagged empty, required present", validateProbeInput{Required1: "x", Required2: 1}, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := requireFields(&c.input)
			if c.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if c.wantErr && err.Error() != "missing required field: "+c.wantMsg && err.Error() != "missing required fields: "+c.wantMsg {
				t.Errorf("error message = %q, want it to mention %q", err.Error(), c.wantMsg)
			}
		})
	}
}

// TestRequireFieldsAcceptsValueOrPointer verifies the helper works whether
// called with a struct value or a pointer to one (tools call it with &args).
func TestRequireFieldsAcceptsValueOrPointer(t *testing.T) {
	empty := validateProbeInput{}
	if err := requireFields(&empty); err == nil {
		t.Errorf("pointer form: expected error for empty struct")
	}
	if err := requireFields(empty); err == nil {
		t.Errorf("value form: expected error for empty struct")
	}
}

// TestRequireFieldsUsesJSONNameNotGoName verifies the error mentions the
// wire-format field name (what the model actually sees/sends), not the Go
// struct field name.
func TestRequireFieldsUsesJSONNameNotGoName(t *testing.T) {
	err := requireFields(&validateProbeInput{Required2: 1})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !contains(msg, "required1") {
		t.Errorf("expected error to mention json name %q, got %q", "required1", msg)
	}
	if contains(msg, "Required1") {
		t.Errorf("error should not leak the Go field name %q, got %q", "Required1", msg)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
