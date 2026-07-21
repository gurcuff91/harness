package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/types"
)

// ScheduleEntry is one scheduled prompt as seen by the agent (includes audit
// fields so ScheduleList gives the model context on what has run).
type ScheduleEntry struct {
	Slug    string `json:"slug"`
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	Runs    int    `json:"runs"`
	LastRun int64  `json:"last_run,omitempty"` // Unix ms, 0 = never
}

// ScheduleStore is the interface the schedule tools use. The concrete store
// (package schedule) is injected by the agent, keeping this package free of the
// storage/cron dependency.
type ScheduleStore interface {
	Set(slug, cron, prompt, owner string) error
	// Delete removes a schedule only if it belongs to owner; a slug owned by
	// another session is treated as absent (false, nil) — no cross-session deletes.
	Delete(slug, owner string) (bool, error)
	// Entries returns only the schedules owned by owner — each session sees just
	// its own.
	Entries(owner string) []ScheduleEntry
}

// Schedule upserts a cron-scheduled prompt (create or edit by slug). owner is the
// id of the session this tool belongs to; the engine routes a fired prompt back
// to that session (empty for single-session transports). It's captured here, not
// exposed to the model.
func Schedule(store ScheduleStore, owner string) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolSchedule,
			Description: "Create or update a scheduled prompt that runs automatically on a cron schedule. The slug is a short unique id (reusing it edits that schedule). The prompt is the instruction that will run on schedule, exactly as if you sent it yourself.\n\nCron accepts a standard 5-field expression (minute hour day-of-month month day-of-week) or a descriptor: @yearly, @monthly, @weekly, @daily (=@midnight), @hourly, or @every <duration> (e.g. @every 1h30m). The minimum interval is 1 minute — anything more frequent (e.g. @every 30s) is rejected.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"slug":{"type":"string","description":"Short unique id for the schedule (kebab-case, e.g. \"daily-standup\")"},"cron":{"type":"string","description":"5-field cron (e.g. \"0 9 * * 1-5\" = weekdays 9am), or a descriptor: @daily, @hourly, @weekly, @monthly, @yearly, @every <duration>. Minimum interval: 1 minute."},"prompt":{"type":"string","description":"The prompt to run on schedule"}},"required":["slug","cron","prompt"]}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Slug   string `json:"slug"`
				Cron   string `json:"cron"`
				Prompt string `json:"prompt"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", fmt.Errorf("Schedule: invalid input: %w", err)
			}
			if err := store.Set(p.Slug, p.Cron, p.Prompt, owner); err != nil {
				return "", err
			}
			return fmt.Sprintf("Scheduled %q (%s).", p.Slug, p.Cron), nil
		},
	}
}

// ScheduleList returns this session's schedules with their cron, prompt, and
// audit fields. owner scopes the listing so a session only sees its own.
func ScheduleList(store ScheduleStore, owner string) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolScheduleList,
			Description: "List all scheduled prompts, including each one's cron expression, prompt, run count, and last run time \u2014 useful to review what's scheduled and whether it has been firing. Response is JSON.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			entries := store.Entries(owner)
			if len(entries) == 0 {
				return "No schedules.", nil
			}
			out, err := json.MarshalIndent(entries, "", "  ")
			if err != nil {
				return "", fmt.Errorf("ScheduleList: encode: %w", err)
			}
			return string(out), nil
		},
	}
}

// ScheduleDelete removes one of this session's scheduled prompts by slug. owner
// scopes it: a slug owned by another session is reported as not found (no-op).
func ScheduleDelete(store ScheduleStore, owner string) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolScheduleDelete,
			Description: "Delete a scheduled prompt by its slug when it is no longer needed.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"slug":{"type":"string","description":"The schedule slug to delete"}},"required":["slug"]}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var p struct {
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(input, &p); err != nil {
				return "", fmt.Errorf("ScheduleDelete: invalid input: %w", err)
			}
			ok, err := store.Delete(p.Slug, owner)
			if err != nil {
				return "", err
			}
			if !ok {
				return fmt.Sprintf("No schedule found with slug %q.", p.Slug), nil
			}
			return fmt.Sprintf("Deleted schedule %q.", p.Slug), nil
		},
	}
}
