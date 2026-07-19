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
	Set(slug, cron, prompt string) error
	Delete(slug string) (bool, error)
	Entries() []ScheduleEntry
}

// Schedule upserts a cron-scheduled prompt (create or edit by slug).
func Schedule(store ScheduleStore) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolSchedule,
			Description: "Create or update a scheduled prompt that runs automatically on a cron schedule. The slug is a short unique id (reusing it edits that schedule). The cron is a standard 5-field expression (minute hour day-of-month month day-of-week; @daily/@hourly/@weekly and @every <duration> also work). The prompt is the instruction that will run on schedule, exactly as if you sent it yourself.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"slug":{"type":"string","description":"Short unique id for the schedule (kebab-case, e.g. \"daily-standup\")"},"cron":{"type":"string","description":"5-field cron expression, e.g. \"0 9 * * 1-5\" (weekdays 9am), or @daily / @every 1h"},"prompt":{"type":"string","description":"The prompt to run on schedule"}},"required":["slug","cron","prompt"]}`),
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
			if err := store.Set(p.Slug, p.Cron, p.Prompt); err != nil {
				return "", err
			}
			return fmt.Sprintf("Scheduled %q (%s).", p.Slug, p.Cron), nil
		},
	}
}

// ScheduleList returns all schedules with their cron, prompt, and audit fields.
func ScheduleList(store ScheduleStore) Tool {
	return Tool{
		Def: types.ToolDef{
			Name:        ToolScheduleList,
			Description: "List all scheduled prompts, including each one's cron expression, prompt, run count, and last run time \u2014 useful to review what's scheduled and whether it has been firing. Response is JSON.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			entries := store.Entries()
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

// ScheduleDelete removes a scheduled prompt by slug.
func ScheduleDelete(store ScheduleStore) Tool {
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
			ok, err := store.Delete(p.Slug)
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
