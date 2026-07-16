package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/gurcuff91/harness/agent"
)

// MemoOpts carries the parsed flags for `harness memo`.
type MemoOpts struct {
	Query   string // optional full-text query; empty = list
	All     bool   // --all: search across ALL projects (no cwd filter)
	Content bool   // --content: include full content in the output
	Limit   int    // --limit N (default 10)
	Skip    int    // --skip N (default 0)
}

// RunMemo lists or searches memories for the current working directory (or all
// projects with --all). Read-only: memories are written/deleted only by the
// agent via its tools.
func RunMemo(ctx context.Context, a *agent.Agent, opts MemoOpts, output string) error {
	server, addr, err := startInternalServer(a)
	if err != nil {
		return err
	}
	defer server.Close()
	c := newClient(addr)

	// Build the query string. cwd defaults to the current directory unless --all.
	q := url.Values{}
	if !opts.All {
		if cwd, err := os.Getwd(); err == nil {
			q.Set("cwd", cwd)
		}
	}
	if opts.Query != "" {
		q.Set("query", opts.Query)
	}
	q.Set("include_content", strconv.FormatBool(opts.Content))
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Skip > 0 {
		q.Set("skip", strconv.Itoa(opts.Skip))
	}

	data, err := c.GetMemories(q.Encode())
	if err != nil {
		return fmt.Errorf("memo: %w", err)
	}

	var res struct {
		Total    int `json:"total"`
		Returned int `json:"returned"`
		Skip     int `json:"skip"`
		Limit    int `json:"limit"`
		Results  []struct {
			Slug      string  `json:"slug"`
			CWD       string  `json:"cwd"`
			Content   string  `json:"content"`
			Score     float64 `json:"score"`
			UpdatedAt int64   `json:"updated_at"`
		} `json:"results"`
	}
	json.Unmarshal(data, &res)

	if output == "json" {
		fmt.Println(string(data))
		return nil
	}

	if res.Total == 0 {
		fmt.Println("No memories found.")
		return nil
	}
	fmt.Printf("%d memories (showing %d):\n", res.Total, res.Returned)
	for _, m := range res.Results {
		line := "• " + m.Slug
		if opts.All {
			line += "  " + shortenPath(m.CWD)
		}
		line += "  " + relTime(m.UpdatedAt)
		if opts.Query != "" {
			line += fmt.Sprintf("  (score %.2f)", m.Score)
		}
		fmt.Println(line)
		if opts.Content && m.Content != "" {
			fmt.Printf("    %s\n", firstLine(m.Content))
		}
	}
	if res.Skip+res.Returned < res.Total {
		fmt.Printf("… %d more (use --skip %d)\n", res.Total-res.Skip-res.Returned, res.Skip+res.Limit)
	}
	return nil
}

// relTime renders a Unix-ms timestamp as a short relative time.
func relTime(ms int64) string {
	if ms == 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// firstLine returns the first non-empty line of s (for a compact preview).
func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
