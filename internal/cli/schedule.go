package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/admin"
)

// scheduleIcon marks each schedule in the listing (a clock), matching the TUI.
const scheduleIcon = "◷"

// RunSchedules lists all cron-scheduled prompts: slug, cron, run count, last run
// (relative), and the full prompt below. Read-only — schedules are created and
// removed by the agent via its tools.
func RunSchedules(ctx context.Context, ad *admin.Admin, output string) error {
	scheds, err := ad.Schedules.List("")
	if err != nil {
		return fmt.Errorf("schedules: %w", err)
	}
	// The wire shape the API always had (slug, cron, prompt, runs, last_run).
	type entry struct {
		Slug    string `json:"slug"`
		Cron    string `json:"cron"`
		Prompt  string `json:"prompt"`
		Runs    int    `json:"runs"`
		LastRun int64  `json:"last_run,omitempty"`
	}
	list := make([]entry, 0, len(scheds))
	for _, sc := range scheds {
		list = append(list, entry{sc.Slug, sc.Cron, sc.Prompt, sc.Runs, sc.LastRun})
	}

	if output == "json" {
		b, _ := json.MarshalIndent(list, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	if len(list) == 0 {
		fmt.Println("No schedules.")
		return nil
	}
	fmt.Printf("%d schedule(s):\n", len(list))
	for _, s := range list {
		last := "never"
		if s.LastRun > 0 {
			last = relTime(s.LastRun)
		}
		fmt.Printf("%s %s   %s   %d runs · %s\n", scheduleIcon, s.Slug, s.Cron, s.Runs, last)
		// Full prompt, indented, preserving line breaks.
		for _, line := range splitLines(s.Prompt) {
			fmt.Printf("  %s\n", line)
		}
	}
	return nil
}

// splitLines splits s on newlines (no trailing empty line for a trailing \n).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
