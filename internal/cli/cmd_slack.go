package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/gurcuff91/harness/internal/transport/slack"
)

// cmdSlack routes slack subcommands.
//
//	harness slack login              interactive login
//	harness slack login --status     verify saved credentials
//	harness slack admin <userID>     add admin
//	harness slack admin remove <id>  remove admin
//	harness slack admin list         list admins
//	harness slack [flags]            run the transport
func cmdSlack(args []string) error {
	if len(args) > 0 && args[0] == "login" {
		return cmdSlackLogin(args[1:])
	}
	if len(args) > 0 && args[0] == "admin" {
		return cmdSlackAdmin(args[1:])
	}

	fs := flag.NewFlagSet("slack", flag.ContinueOnError)
	workspace := fs.String("workspace", os.Getenv("SLACK_WORKSPACE"), "Slack workspace URL (or set SLACK_WORKSPACE)")
	xoxc := fs.String("xoxc", os.Getenv("SLACK_XOXC"), "xoxc- session token (or set SLACK_XOXC)")
	xoxd := fs.String("xoxd", os.Getenv("SLACK_XOXD"), "xoxd- session cookie (or set SLACK_XOXD)")
	model := fs.String("model", "", "model override (provider/model)")
	thinking := fs.String("thinking", "", "thinking level override")
	scheduler := fs.Bool("scheduler", false, "run the cron scheduler engine")
	if err := fs.Parse(args); err != nil {
		return err
	}

	a := newInteractiveAgent(*scheduler, slack.Directive)
	defer a.Close()
	ctx, cancel := signalContext()
	defer cancel()

	return slack.Run(ctx, a, slack.Options{
		Workspace: *workspace,
		XoxC:      *xoxc,
		XoxD:      *xoxd,
		Model:     *model,
		Thinking:  *thinking,
		Scheduler: *scheduler,
	})
}

// cmdSlackAdmin handles `harness slack admin <userID|list|remove>`.
func cmdSlackAdmin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: harness slack admin <userID>  |  harness slack admin list  |  harness slack admin remove <userID>")
	}
	switch args[0] {
	case "list":
		admins, err := slack.ListAdmins()
		if err != nil {
			return err
		}
		if len(admins) == 0 {
			fmt.Println("No admins configured.")
			return nil
		}
		fmt.Printf("%d admin(s):\n", len(admins))
		for _, a := range admins {
			fmt.Printf("  %s\n", a)
		}
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: harness slack admin remove <userID>")
		}
		if err := slack.RemoveAdmin(args[1]); err != nil {
			return err
		}
		fmt.Printf("Removed admin: %s\n", args[1])
	default:
		// Treat the argument as a userID to add.
		userID := args[0]
		if err := slack.AddAdmin(userID); err != nil {
			return err
		}
		fmt.Printf("Added admin: %s\n", userID)
		fmt.Println("This user can now run /new /stop /compact /thinking /model in Slack.")
	}
	return nil
}

// cmdSlackLogin handles `harness slack login [--status]`.
func cmdSlackLogin(args []string) error {
	fs := flag.NewFlagSet("slack login", flag.ContinueOnError)
	status := fs.Bool("status", false, "verify saved credentials instead of logging in")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := signalContext()
	defer cancel()

	if *status {
		return slackLoginStatus(ctx)
	}
	return slackLoginInteractive(ctx)
}

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
