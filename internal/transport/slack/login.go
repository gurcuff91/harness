package slack

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// reAPIToken extracts the xoxc session token embedded in Slack's workspace
// HTML. Slack injects the current session state as a JSON blob in the page;
// the api_token field holds the xoxc token.
var reAPIToken = regexp.MustCompile(`"api_token"\s*:\s*"(xoxc-[^"]+)"`)

// DeriveXoxC fetches the workspace URL with the xoxd cookie and extracts the
// xoxc session token from the embedded session state JSON. This is the same
// request the browser makes when loading the Slack web client.
func DeriveXoxC(ctx context.Context, workspaceURL, xoxd string) (string, error) {
	// Normalise workspace URL — strip trailing slash.
	workspaceURL = strings.TrimRight(workspaceURL, "/")

	req, err := http.NewRequestWithContext(ctx, "GET", workspaceURL, nil)
	if err != nil {
		return "", fmt.Errorf("slack login: build request: %w", err)
	}
	req.Header.Set("Cookie", "d="+xoxd)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; harness/1.0)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("slack login: GET workspace: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("slack login: workspace returned HTTP %d — check your workspace URL and xoxd cookie", resp.StatusCode)
	}

	// Read up to 1MB — the token appears near the top of the page.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("slack login: read response: %w", err)
	}

	m := reAPIToken.FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("slack login: could not find api_token in workspace page — the xoxd cookie may be invalid or expired")
	}
	return string(m[1]), nil
}

// VerifyAndSave verifies the xoxc+xoxd pair against auth.test, populates
// user_id and team, and saves the credentials to ~/.harness/slack.json.
func VerifyAndSave(ctx context.Context, workspaceURL, xoxc, xoxd string) (*Credentials, error) {
	bot := NewBot(workspaceURL, xoxc, xoxd)
	me, err := bot.AuthTest(ctx)
	if err != nil {
		return nil, fmt.Errorf("slack login: auth.test failed: %w", err)
	}

	creds := &Credentials{
		Workspace: strings.TrimRight(workspaceURL, "/"),
		XoxC:      xoxc,
		XoxD:      xoxd,
		UserID:    me.UserID,
		Team:      me.Team,
	}
	if err := SaveCredentials(creds); err != nil {
		return nil, fmt.Errorf("slack login: save credentials: %w", err)
	}
	return creds, nil
}
