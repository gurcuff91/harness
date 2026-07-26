package cli

import (
	"flag"
	"os"

	"github.com/gurcuff91/harness/internal/transport/slack"
)

// cmdSlack runs the Slack transport.
//
//	harness slack --workspace <url> --xoxc <token> --xoxd <token> [flags]
func cmdSlack(args []string) error {
	fs := flag.NewFlagSet("slack", flag.ContinueOnError)
	workspace := fs.String("workspace", os.Getenv("SLACK_WORKSPACE"), "Slack workspace URL (or set SLACK_WORKSPACE)")
	xoxc := fs.String("xoxc", os.Getenv("SLACK_XOXC"), "xoxc- browser session token (or set SLACK_XOXC)")
	xoxd := fs.String("xoxd", os.Getenv("SLACK_XOXD"), "xoxd- browser session cookie (or set SLACK_XOXD)")
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
