package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/gurcuff91/harness/internal/transport/slack"
)

// slackLoginStatus verifies the saved credentials are still valid.
func slackLoginStatus(ctx context.Context) error {
	creds, err := slack.LoadCredentials()
	if err != nil {
		return fmt.Errorf("read credentials: %w", err)
	}
	if creds == nil {
		fmt.Println("Not logged in. Run 'harness slack login' to authenticate.")
		return nil
	}
	// Quick verify via auth.test.
	bot := slack.NewBot(creds.Workspace, creds.XoxC, creds.XoxD)
	me, err := bot.AuthTest(ctx)
	if err != nil {
		fmt.Printf("✗ Credentials invalid or expired: %s\n", err)
		fmt.Println("  Run 'harness slack login' to re-authenticate.")
		return nil
	}
	fmt.Printf("✓ Logged in as %s  (team: %s)\n", me.UserID, me.Team)
	fmt.Printf("  workspace: %s\n", creds.Workspace)
	return nil
}

// slackLoginInteractive runs the interactive login flow:
//  1. Ask for workspace URL
//  2. Ask for xoxd cookie
//  3. Derive xoxc from workspace page
//  4. Verify with auth.test
//  5. Save to ~/.harness/slack.json
func slackLoginInteractive(ctx context.Context) error {
	r := bufio.NewReader(os.Stdin)

	fmt.Println("Connecting to Slack using your browser session.")
	fmt.Println()

	// Step 1 — workspace URL.
	fmt.Print("Workspace URL (e.g. https://myco.slack.com): ")
	workspace, _ := r.ReadString('\n')
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return fmt.Errorf("workspace URL is required")
	}
	if !strings.HasPrefix(workspace, "http") {
		workspace = "https://" + workspace
	}

	// Step 2 — xoxd cookie.
	fmt.Println("\nTo get your xoxd cookie:")
	fmt.Println("  1. Open Slack in your browser (app.slack.com or your workspace URL)")
	fmt.Println("  2. Press F12 → Application → Cookies → app.slack.com")
	fmt.Println("  3. Copy the value of the 'd' cookie (starts with xoxd-...)")
	fmt.Print("\nPaste your xoxd cookie: ")
	xoxd, _ := r.ReadString('\n')
	xoxd = strings.TrimSpace(xoxd)
	if xoxd == "" {
		return fmt.Errorf("xoxd cookie is required")
	}
	// Accept with or without the "d=" prefix the user might accidentally include.
	xoxd = strings.TrimPrefix(xoxd, "d=")

	// Step 3 — derive xoxc.
	fmt.Println("\nDeriving session token from workspace...")
	xoxc, err := slack.DeriveXoxC(ctx, workspace, xoxd)
	if err != nil {
		return err
	}

	// Step 4 — verify + save.
	fmt.Println("Verifying credentials with Slack...")
	creds, err := slack.VerifyAndSave(ctx, workspace, xoxc, xoxd)
	if err != nil {
		return err
	}

	fmt.Printf("\n✓ Authenticated as %s  (team: %s)\n", creds.UserID, creds.Team)
	fmt.Printf("✓ Credentials saved to ~/.harness/slack.json\n")
	fmt.Println("\nYou can now run: harness slack")
	return nil
}
