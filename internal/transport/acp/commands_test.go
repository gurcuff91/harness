package acp

import "testing"

// TestCommandsCoveredByConfigOptionsMatchesBuildConfigOptionsIDs guards
// against the two lists drifting apart: every ID buildConfigOptions hands
// out as a config option must also be in the filter set, or it would show
// up as a redundant slash command in available_commands_update again.
func TestCommandsCoveredByConfigOptionsMatchesBuildConfigOptionsIDs(t *testing.T) {
	for _, id := range []string{"model", "thinking"} {
		if !commandsCoveredByConfigOptions[id] {
			t.Errorf("config option id %q is not filtered out of available_commands_update", id)
		}
	}
	// And nothing else should be in the filter — rename/compact/reset/skills
	// have no config-option equivalent and must keep showing up as commands.
	if len(commandsCoveredByConfigOptions) != 2 {
		t.Errorf("commandsCoveredByConfigOptions has %d entries, want exactly 2 (model, thinking): %v",
			len(commandsCoveredByConfigOptions), commandsCoveredByConfigOptions)
	}
}
