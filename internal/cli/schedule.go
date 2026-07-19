package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gurcuff91/harness/agent"
)

// scheduleIcon marks each schedule in the listing (a clock), matching the TUI.
const scheduleIcon = "◷"

// RunSchedules lists all cron-scheduled prompts: slug, cron, run count, last run
// (relative), and the full prompt below. Read-only — schedules are created and
// removed by the agent via its tools.
func RunSchedules(ctx context.Context, a *agent.Agent, output string) error {
	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	data, err := c.GetSchedules()
	if err != nil {
		return fmt.Errorf("schedules: %w", err)
	}

	if output == "json" {
		fmt.Println(string(data))
		return nil
	}

	var list []struct {
		Slug    string `json:"slug"`
		Cron    string `json:"cron"`
		Prompt  string `json:"prompt"`
		Runs    int    `json:"runs"`
		LastRun int64  `json:"last_run"`
	}
	json.Unmarshal(data, &list)

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
