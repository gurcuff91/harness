package agent

import (
	"encoding/json"
	"testing"

	"github.com/gurcuff91/harness/agent/store"
)

func TestSessionDetailMatchesMetaAndSerializesFlat(t *testing.T) {
	a, model := directivesTestAgent(t, store.NewInMemoryStore(), AgentOptions{})
	sess, err := a.NewSession(t.TempDir(), model)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	d := sess.Detail()
	if d.ID != sess.ID() || d.MaxIterations != sess.MaxIterations() || d.Model != sess.Model() {
		t.Fatalf("detail %+v does not match the session", d)
	}
	b, _ := json.Marshal(d)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"id", "model", "max_iterations"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing (meta must be flattened): %s", k, b)
		}
	}
	for _, k := range []string{"model_fallback", "missing_profiles", "channels"} {
		if _, ok := m[k]; ok {
			t.Errorf("key %q must be omitted when empty: %s", k, b)
		}
	}
}
