// Run() methods for `harness slack` and its nested subcommands (login,
// admin add/list/remove). Each Run() below delegates to the slack.*
// package functions and the slackLogin* helpers in slack_login.go.
//
// slackAdminCmd has no bare-positional add shortcut: Kong forbids mixing
// arg:"" with cmd:"" siblings in the same struct, so admin add/list/remove
// are genuine subcommand siblings instead.
package cli

import (
	"fmt"

	"github.com/gurcuff91/harness/internal/transport/slack"
)

func (c *slackRunCmd) Run() error {
	a := newInteractiveAgent(c.Scheduler, slack.Directive)
	ctx, cancel := signalContext()
	defer cancel()

	return slack.Run(ctx, a, slack.Options{
		Workspace: c.Workspace,
		XoxC:      c.XoxC,
		XoxD:      c.XoxD,
		Model:     c.Model,
		Thinking:  c.Thinking,
		Scheduler: c.Scheduler,
	})
}

func (c *slackLoginCmd) Run() error {
	ctx, cancel := signalContext()
	defer cancel()

	if c.Status {
		return slackLoginStatus(ctx)
	}
	return slackLoginInteractive(ctx)
}

func (c *slackAdminAddCmd) Run() error {
	if err := slack.AddAdmin(c.UserID); err != nil {
		return err
	}
	fmt.Printf("Added admin: %s\n", c.UserID)
	fmt.Println("This user can now run /new /stop /compact /thinking /model in Slack.")
	return nil
}

func (c *slackAdminListCmd) Run() error {
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
	return nil
}

func (c *slackAdminRemoveCmd) Run() error {
	if err := slack.RemoveAdmin(c.UserID); err != nil {
		return err
	}
	fmt.Printf("Removed admin: %s\n", c.UserID)
	return nil
}
