package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestRequiredFieldPresenceIsEnforced is the regression test for the
// "presence validation" gap audited across all built-ins: before requireFields
// existed, an empty/absent required field either (a) had NO check at all and
// only failed by accident deep in an OS/network call (Bash.command,
// Read.path, Write.path, Edit.path, Fetch.url), or (b) fell through to a
// misleadingly "normal-looking" result that masked the invalid input
// (Skill.name, MemoDelete.slug, ScheduleDelete.slug — see each case's comment
// below for exactly what used to happen). Each case here sends the required
// field as an empty string and asserts the tool now reports it explicitly
// via requireFields' "missing required field" message, BEFORE any business
// logic (file I/O, HTTP, store lookup) runs.
func TestRequiredFieldPresenceIsEnforced(t *testing.T) {
	cases := []struct {
		name  string
		tool  Tool
		input string
	}{
		// ── Tier 1: previously caught only by accident (an OS/network error
		// that happened to also occur for an empty string) ──────────────────
		{"Bash.command", Bash(), `{"command":""}`},
		{"Read.path", ReadFile(), `{"path":""}`},
		{"Write.path", WriteFile(), `{"path":"","content":"x"}`},
		{"Edit.path", Edit(), `{"path":"","old_text":"a","new_text":"b"}`},
		{"Fetch.url", Fetch(), `{"url":""}`},

		// ── Tier 2: previously fell through to a response that LOOKED like a
		// normal, successful outcome instead of flagging bad input ──────────
		{
			// Before: with a permissive readFn, name="" could silently
			// return real skill content — nothing forced the loader to
			// reject it. Now rejected before readFn is ever called.
			"Skill.name", Skill(func(name string) (string, string, error) {
				return "should never be reached", "/dir", nil
			}), `{"name":""}`,
		},
		{
			// Before: "No memory found with slug \"\"" read exactly like a
			// legitimate "nothing to delete" outcome, not an invalid call.
			"MemoDelete.slug", MemoDelete(auditMemoryStore{}, "/tmp"), `{"slug":""}`,
		},
		{
			// Before: "No schedule found with slug \"\"" — same
			// indistinguishable-from-normal problem as MemoDelete.
			"ScheduleDelete.slug", ScheduleDelete(auditScheduleStore{}, "owner"), `{"slug":""}`,
		},

		// ── Consolidation: already had a manual check; now goes through the
		// same shared helper as every other tool for a consistent message ──
		{"MemoWrite.slug", MemoWrite(auditMemoryStore{}, "/tmp"), `{"slug":"","content":"x"}`},
		{"MemoWrite.content", MemoWrite(auditMemoryStore{}, "/tmp"), `{"slug":"x","content":""}`},
		{"Schedule.slug", Schedule(auditScheduleStore{}, "owner"), `{"slug":"","cron":"@daily","prompt":"x"}`},
		{"Schedule.cron", Schedule(auditScheduleStore{}, "owner"), `{"slug":"x","cron":"","prompt":"x"}`},
		{"Schedule.prompt", Schedule(auditScheduleStore{}, "owner"), `{"slug":"x","cron":"@daily","prompt":""}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var (
				out string
				err error
			)
			if c.tool.ExecuteRich != nil {
				out, _, err = c.tool.ExecuteRich(context.Background(), json.RawMessage(c.input))
			} else {
				out, err = c.tool.Execute(context.Background(), json.RawMessage(c.input))
			}
			if err == nil {
				t.Fatalf("%s: expected an error for empty required field, got nil (out=%q)", c.name, out)
			}
			if !strings.Contains(err.Error(), "missing required field") {
				t.Errorf("%s: expected requireFields' error message, got: %v", c.name, err)
			}
			if out == "" {
				t.Errorf("%s: output should carry the same message as the error, got empty output", c.name)
			}
		})
	}
}

// TestWriteContentEmptyIsStillLegitimate locks in the ONE deliberate
// exception the migration preserved: Write.content has NO validate:"required"
// tag — creating an empty file is a real, intentional use case, unlike an
// empty path. This must keep succeeding after the migration.
func TestWriteContentEmptyIsStillLegitimate(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/empty.txt"
	tool := WriteFile()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","content":""}`))
	if err != nil {
		t.Fatalf("expected empty content to be accepted, got error: %v (out=%q)", err, out)
	}
}

// TestSubagentAndColleagueAskStillCatchWhitespaceOnly verifies the two tools
// that need MORE than presence checking (blank-but-not-empty prompts) still
// reject whitespace-only input via their own TrimSpace check, which
// requireFields' tag alone would miss (IsZero() is false for "   ").
func TestSubagentAndColleagueAskStillCatchWhitespaceOnly(t *testing.T) {
	t.Run("Subagent whitespace-only prompt", func(t *testing.T) {
		tool := Subagent(func(ctx context.Context, prompt string) (string, error) { return "unreachable", nil })
		out, err := tool.Execute(context.Background(), json.RawMessage(`{"prompt":"   "}`))
		if err == nil {
			t.Fatalf("expected whitespace-only prompt to be rejected, got out=%q", out)
		}
	})
	t.Run("ColleagueAsk whitespace-only colleague and prompt", func(t *testing.T) {
		tool := ColleagueAsk()
		out, err := tool.Execute(context.Background(), json.RawMessage(`{"colleague":"   ","prompt":"x"}`))
		if err == nil {
			t.Fatalf("expected whitespace-only colleague to be rejected, got out=%q", out)
		}
		out, err = tool.Execute(context.Background(), json.RawMessage(`{"colleague":"x","prompt":"   "}`))
		if err == nil {
			t.Fatalf("expected whitespace-only prompt to be rejected, got out=%q", out)
		}
	})
}
