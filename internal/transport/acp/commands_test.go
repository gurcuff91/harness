package acp

import "testing"

// TestCommandsExcludedFromACPMatchesExpectedSet locks the exclusion list to
// exactly the 4 known IDs — 2 covered by native configOptions (model,
// thinking) and 2 with no ACP equivalent at all (rename, reset) — so it
// can't silently grow or shrink. "compact" and any "skill:<name>" MUST NOT
// be in here: they're the only two commands executableCommand (methods.go)
// actually executes, and hiding them from available_commands_update would
// make them undiscoverable in Zed's UI despite fully working if typed.
func TestCommandsExcludedFromACPMatchesExpectedSet(t *testing.T) {
	want := map[string]bool{"model": true, "thinking": true, "rename": true, "reset": true}
	for id := range want {
		if !commandsExcludedFromACP[id] {
			t.Errorf("%q should be excluded from available_commands_update but isn't", id)
		}
	}
	for id := range commandsExcludedFromACP {
		if !want[id] {
			t.Errorf("unexpected id %q excluded from available_commands_update", id)
		}
	}
	if len(commandsExcludedFromACP) != len(want) {
		t.Errorf("commandsExcludedFromACP has %d entries, want %d: %v", len(commandsExcludedFromACP), len(want), commandsExcludedFromACP)
	}
}
