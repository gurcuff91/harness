// Run() for `harness slack login`: the Slack account the input channels
// (--slack-channel, API `channels`) listen with. The login itself lives in
// channels/slack; the interactive prompts in slack_login.go.
package cli

func (c *slackLoginCmd) Run() error {
	ctx, cancel := signalContext()
	defer cancel()

	if c.Status {
		return slackLoginStatus(ctx)
	}
	return slackLoginInteractive(ctx)
}
