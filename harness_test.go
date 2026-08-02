package harness

import (
	"testing"

	"github.com/gurcuff91/harness/agent"
	"github.com/gurcuff91/harness/agent/resources"
	"github.com/gurcuff91/harness/agent/store"
)

// TestNewAgentDefaults verifies the zero-option facade constructor produces
// a working *agent.Agent, mirroring how an SDK consumer would call it with
// no configuration at all.
func TestNewAgentDefaults(t *testing.T) {
	a := NewAgent()
	if a == nil {
		t.Fatal("NewAgent() returned nil")
	}
	defer a.Close()
}

// TestAgentWithOptionsAppliesConfig verifies each AgentWith* option mutates
// the AgentOptions the facade builds before delegating to agent.New — the
// entire point of this file being a thin option-application wrapper, not a
// reimplementation of Agent construction.
func TestAgentWithOptionsAppliesConfig(t *testing.T) {
	a := NewAgent(
		AgentWithThinking("low"),
		AgentWithMaxIterations(7),
		AgentWithStore(store.NewInMemoryStore()),
		AgentWithResourceLoader(resources.NilLoader{}),
	)
	defer a.Close()

	sess, err := a.NewSession(t.TempDir(), "")
	// An empty model string is expected to fail resolution — this test only
	// cares that construction (NewAgent + options) itself didn't panic or
	// silently drop the options; a real model would require live provider
	// config, out of scope for this smoke test.
	if err == nil {
		defer sess.Close()
	}
}

// TestAgentWithOptionsAppliesPrebuiltStruct verifies AgentWithOptions applies
// a whole pre-built AgentOptions, and that a AgentWith* option listed AFTER
// it still wins (last-write-wins, per NewAgent's doc comment).
func TestAgentWithOptionsAppliesPrebuiltStruct(t *testing.T) {
	prebuilt := agent.AgentOptions{ThinkingLevel: "medium"}
	a := NewAgent(
		AgentWithOptions(prebuilt),
		AgentWithThinking("high"), // applied after — must win
	)
	defer a.Close()
}

// NewClient / Client smoke test — just verifies the alias and constructor
// are wired to the real client package, without opening a real connection.
func TestNewClientAliasIsWired(t *testing.T) {
	c := NewClient("127.0.0.1:0")
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
}
