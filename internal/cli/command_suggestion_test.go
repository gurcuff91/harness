package cli

import "testing"

// The rule is Claude Code's, and it is NOT "distance <= 2" — the first draft of it was. Every case
// below was run against the real `claude` binary (v2.1.202) on 2026-11-16: it answered either
// `unknown command "<word>"` + `Did you mean …` (suggest = true) or went on to treat the word as a
// prompt (`Not logged in · Please run /login`, suggest = false). The results are pinned here as
// DATA, so the rule cannot drift into something plausible but different (two edits would swallow
// real one-word messages: `doct`, `updt`, `agnt` are messages in Claude Code).
//
// Claude's commands at the time: agents auth auto-mode doctor gateway install mcp plugin plugins project setup-token ultrareview update
func TestSuggestionRuleMatchesClaudeCodesMeasuredBehaviour(t *testing.T) {
	cmds := []string{"agents", "auth", "auto-mode", "doctor", "gateway", "install", "mcp", "plugin", "plugins", "project", "setup-token", "ultrareview", "update"}
	for _, c := range []struct {
		w string
		d bool
	}{
		{"doctr", true}, {"docto", true}, {"dctor", true}, {"octor", true}, {"docotr", true}, {"doctors", true}, {"xdoctor", true},
		{"mcpp", true}, {"mc", true}, {"mp", true}, {"cp", true}, {"mpc", true}, {"cmp", true}, {"mcc", true},
		{"plugn", true}, {"plugni", true}, {"pluginss", true}, {"plugns", true},
		{"updat", true}, {"updte", true}, {"upate", true}, {"pdate", true}, {"udpate", true}, {"upadte", true},
		{"instal", true}, {"insall", true}, {"instll", true}, {"nstall", true}, {"intall", true}, {"isntall", true},
		{"aut", true}, {"ath", true}, {"autx", true}, {"autth", true}, {"auht", true}, {"atuh", true}, {"uth", true}, {"authh", true},
		{"agent", true}, {"agnts", true},
		{"DOCTR", true}, {"Mcpp", true}, {"MCP", true}, {"Mcp", true}, {"Doctr", true}, {"docto-r", true}, {"dotcor", true},
		{"doctxr", true}, {"mcpx", true},
		// prompt in Claude
		{"doct", false}, {"doc", false}, {"dor", false}, {"dctr", false}, {"dcotr", false}, {"dotcro", false},
		{"docters", false}, {"xxdoctor", false}, {"doctorxx", false}, {"doctorxxx", false}, {"ocdtor", false},
		{"updt", false}, {"upd", false}, {"pdat", false}, {"inst", false}, {"insal", false}, {"inall", false},
		{"agnt", false}, {"agen", false}, {"ages", false}, {"at", false}, {"ah", false}, {"m", false}, {"ab", false}, {"xy", false}, {"zz", false},
		{"xyz", false}, {"ahu", false}, {"xxxx", false}, {"dtr", false}, {"dcr", false}, {"plgn", false}, {"plug", false},
		{"doctr1", false}, {"doctr.", false}, {"sesions", false}, {"help", false}, {"version", false}, {"fix", false}, {"hello", false}, {"OK", false},
	} {
		_, got := suggestCommand(c.w, cmds)
		if got != c.d {
			t.Errorf("%q: harness's rule says suggest=%v, Claude Code's real binary says %v", c.w, got, c.d)
		}
	}
}
